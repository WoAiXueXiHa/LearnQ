package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/evaluation"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 文档与 RAG 相关上限：文档 5 MiB、multipart 包裹 6 MiB、RAG 请求体 64 KiB、
// 问题 2000 rune，RAG 全链路最长 80s 超时。
const (
	maxDocumentBytes      = 5 << 20
	maxMultipartBodyBytes = maxDocumentBytes + (1 << 20)
	maxRAGRequestBytes    = 64 << 10
	maxRAGQuestionRunes   = 2000
	skillRequestTimeout   = 80 * time.Second
	ragRequestTimeout     = 80 * time.Second
)

var citationPattern = regexp.MustCompile(`\[S([0-9]+)\]`)

// dueReviews 是 GET /api/v1/review-tasks 的 due 快捷入口：固定 scope=due 后复用 reviewTasks。
func (s *Server) dueReviews(c *gin.Context) {
	c.Request.URL.RawQuery = "scope=due"
	s.reviewTasks(c)
}

// reviewTasks 处理 GET /api/v1/review-tasks：按 scope（due/upcoming/all）列出 scheduled
// 复习任务，联表带出报告内容与学习记录标题；scope 非法时返回 422。
func (s *Server) reviewTasks(c *gin.Context) {
	scope := c.DefaultQuery("scope", "all")
	now := time.Now().UTC()
	query := s.store.DB.Table("review_tasks rt").
		Select(`rt.*, r.task_id, r.markdown_content,
			COALESCE(sr.title,'学习报告') record_title`).
		Joins("JOIN reports r ON r.id=rt.report_id").
		// study_record_id 只存在 ai_task.payload_json 而非外键列，标题需要 SQL 侧 JSON 反查。
		Joins(`LEFT JOIN ai_tasks at ON at.id=r.task_id`).
		Joins(`LEFT JOIN study_records sr ON sr.id=
			CAST(JSON_UNQUOTE(JSON_EXTRACT(at.payload_json,'$.study_record_id')) AS UNSIGNED)`).
		Where("rt.status='scheduled'")
	switch scope {
	case "due":
		query = query.Where("rt.due_at<=?", now)
	case "upcoming":
		query = query.Where("rt.due_at>?", now)
	case "all":
	default:
		fail(c, 422, "VALIDATION_FAILED", "scope must be due, upcoming or all", nil)
		return
	}
	rows := make([]map[string]any, 0)
	if err := query.Order("rt.due_at").Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not list review tasks", nil)
		return
	}
	ok(c, 200, rows)
}

// completeReview 处理 POST /api/v1/review-tasks/:id/complete：按 mastery 计算下一次间隔，
// 条件更新（含到期校验）与审计事件同事务提交，并发或重复请求只有一个能生效。
func (s *Server) completeReview(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var input struct {
		Mastery *int `json:"mastery"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Mastery == nil {
		fail(c, 422, "VALIDATION_FAILED", "mastery is required", nil)
		return
	}
	interval, err := domain.ReviewInterval(*input.Mastery)
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", err.Error(), nil)
		return
	}
	var task domain.ReviewTask
	if lookupFailed(c, s.store.DB.First(&task, id).Error, "could not load review task") {
		return
	}
	now := time.Now().UTC()
	if task.DueAt.After(now) {
		fail(c, 409, "REVIEW_NOT_DUE", "review task is not due yet", gin.H{"due_at": task.DueAt})
		return
	}
	next := now.Add(interval)
	updated := false
	err = s.store.DB.Transaction(func(tx *gorm.DB) error {
		// 条件更新同时校验 scheduled 与到期时间，使重复点击或并发请求只有一个成功。
		// 当前计划和审计事件同事务写入，后续可以完整还原间隔变化。
		result := tx.Exec(`UPDATE review_tasks SET mastery=?,status='scheduled',due_at=?,completed_at=?,updated_at=?
			WHERE id=? AND status='scheduled' AND due_at<=?`, *input.Mastery, next, now, now, id, now)
		if result.Error != nil || result.RowsAffected != 1 {
			return result.Error
		}
		event := domain.ReviewEvent{ReviewTaskID: id, Action: "complete", OldMastery: task.Mastery, NewMastery: *input.Mastery, OldDueAt: task.DueAt, NewDueAt: next, CreatedAt: now}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		updated = true
		return nil
	})
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "review update failed", nil)
		return
	}
	if !updated {
		fail(c, 409, "REVIEW_ALREADY_HANDLED", "review task was already handled", nil)
		return
	}
	ok(c, 200, gin.H{"id": id, "mastery": *input.Mastery, "due_at": next})
}

// skipReview 处理 POST /api/v1/review-tasks/:id/skip：到期任务顺延 24 小时，同样以条件更新防重复。
func (s *Server) skipReview(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var task domain.ReviewTask
	if lookupFailed(c, s.store.DB.First(&task, id).Error, "could not load review task") {
		return
	}
	now := time.Now().UTC()
	if task.DueAt.After(now) {
		fail(c, 409, "REVIEW_NOT_DUE", "review task is not due yet", gin.H{"due_at": task.DueAt})
		return
	}
	next := now.Add(24 * time.Hour)
	updated := false
	err := s.store.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("UPDATE review_tasks SET due_at=?,updated_at=? WHERE id=? AND status='scheduled' AND due_at<=?", next, now, id, now)
		if result.Error != nil || result.RowsAffected != 1 {
			return result.Error
		}
		if err := tx.Create(&domain.ReviewEvent{ReviewTaskID: id, Action: "skip", OldMastery: task.Mastery, NewMastery: task.Mastery, OldDueAt: task.DueAt, NewDueAt: next, CreatedAt: now}).Error; err != nil {
			return err
		}
		updated = true
		return nil
	})
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "review update failed", nil)
		return
	}
	if !updated {
		fail(c, 409, "REVIEW_ALREADY_HANDLED", "review task was already handled", nil)
		return
	}
	ok(c, 200, gin.H{"id": id, "mastery": task.Mastery, "due_at": next})
}

// runSkill 处理 POST /api/v1/skills/:name/runs：同步执行 Skill 并持久化 trace；
// name=multi-agent 时走 Eino DAG，各成功路由单独落 trace。
func (s *Server) runSkill(c *gin.Context) {
	// 同步实验接口与异步报告共用 Registry 和 Trace；差别只在是否经过任务队列。
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if err != nil || !json.Valid(body) {
		fail(c, 422, "VALIDATION_FAILED", "valid JSON input is required", nil)
		return
	}
	started := time.Now()
	requestCtx, cancel := context.WithTimeout(c.Request.Context(), skillRequestTimeout)
	defer cancel()
	workflowID := c.GetString("request_id")
	if c.Param("name") == "multi-agent" {
		// 多 Agent 每条成功路由单独落 trace，局部失败仍保留其他节点的可观察结果。
		var workflowInput struct {
			Modules []string `json:"modules"`
		}
		// 解析失败仅影响 modules 裁剪，忽略错误并退回默认路由集合。
		_ = json.Unmarshal(body, &workflowInput)
		result, runErr := s.skills.RunEinoWorkflow(requestCtx, workflowInput.Modules, body)
		runIDs := make(map[string]uint64, len(result.Routes))
		for _, route := range result.Routes {
			response := result.Outputs[route]
			definition, exists := s.skills.Get(route)
			if !exists {
				continue
			}
			hash, _ := s.skills.PromptHash(route)
			var routeErr error
			if message := result.Errors[route]; message != "" {
				routeErr = errors.New(message)
			}
			latency := result.Latencies[route]
			if latency <= 0 {
				latency = time.Since(started)
			}
			runID, recordErr := s.recorder.Record(context.Background(), 0, workflowID, definition, hash, string(body), response, latency, result.Tools[route], routeErr)
			if recordErr != nil {
				fail(c, 500, "INTERNAL_ERROR", "workflow trace could not be persisted", nil)
				return
			}
			runIDs[route] = runID
		}
		if runErr != nil {
			var dependencyErr *model.DependencyError
			if errors.As(runErr, &dependencyErr) {
				failModelDependency(c, runErr)
				return
			}
			fail(c, 500, "INTERNAL_ERROR", "multi-agent workflow failed", runErr.Error())
			return
		}
		ok(c, 200, gin.H{"workflow": result, "agent_run_ids": runIDs})
		return
	}
	output, executions, err := s.skills.RunDetailed(requestCtx, c.Param("name"), body)
	if err != nil {
		var dependencyErr *model.DependencyError
		if errors.As(err, &dependencyErr) {
			failModelDependency(c, err)
			return
		}
		fail(c, 422, "VALIDATION_FAILED", err.Error(), nil)
		return
	}
	definition, _ := s.skills.Get(c.Param("name"))
	hash, _ := s.skills.PromptHash(c.Param("name"))
	runID, recordErr := s.recorder.Record(context.Background(), 0, workflowID, definition, hash, string(body), output, time.Since(started), executions, nil)
	if recordErr != nil {
		fail(c, 500, "INTERNAL_ERROR", "trace could not be persisted", nil)
		return
	}
	ok(c, 200, gin.H{"agent_run_id": runID, "output": json.RawMessage(output.Content)})
}

// agentRun 处理 GET /api/v1/agent-runs/:id：返回单次执行的 run、steps 与 tool_calls。
func (s *Server) agentRun(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var run map[string]any
	result := s.store.DB.Table("agent_runs").Where("id=?", id).Take(&run)
	if lookupFailed(c, result.Error, "could not load agent run") {
		return
	}
	var steps, tools []map[string]any
	if err := s.store.DB.Table("agent_steps").Where("agent_run_id=?", id).Order("id").Find(&steps).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load agent steps", nil)
		return
	}
	if err := s.store.DB.Table("tool_calls").Where("agent_run_id=?", id).Order("id").Find(&tools).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load tool calls", nil)
		return
	}
	ok(c, 200, gin.H{"run": run, "steps": steps, "tool_calls": tools})
}

// uploadDocument 处理 POST /api/v1/documents：multipart 上传，限 5 MiB 且仅收 md/txt/json；
// 校验内容与扩展名后写 documents 并创建 document_index 任务，返回 202。
func (s *Server) uploadDocument(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxMultipartBodyBytes)
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(c, http.StatusRequestEntityTooLarge, "VALIDATION_FAILED", "multipart request exceeds 6 MiB", nil)
			return
		}
		fail(c, 422, "VALIDATION_FAILED", "multipart file is required", nil)
		return
	}
	// multipart 解析会落盘临时文件，请求结束后及时清理。
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", "multipart file is required", nil)
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	// 多读 1 字节用于区分“恰好达到上限”和“已经超限”，并在入库前验证 UTF-8/JSON。
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", "file could not be read", nil)
		return
	}
	if len(body) > maxDocumentBytes {
		fail(c, 413, "VALIDATION_FAILED", "file exceeds 5 MiB limit", nil)
		return
	}
	if !rag.ValidDocument(body) {
		fail(c, 422, "VALIDATION_FAILED", "file must be non-empty UTF-8", nil)
		return
	}
	header.Filename = strings.TrimSpace(header.Filename)
	if header.Filename == "" || utf8.RuneCountInString(header.Filename) > 255 {
		fail(c, 422, "VALIDATION_FAILED", "filename is required and must not exceed 255 characters", nil)
		return
	}
	dot := strings.LastIndex(header.Filename, ".")
	if dot < 0 {
		fail(c, 422, "VALIDATION_FAILED", "file must have a .md, .txt or .json extension", nil)
		return
	}
	ext := strings.ToLower(header.Filename[dot+1:])
	if ext != "md" && ext != "txt" && ext != "json" {
		fail(c, 422, "VALIDATION_FAILED", "only Markdown, TXT and JSON are supported", nil)
		return
	}
	if ext == "json" && !json.Valid(body) {
		fail(c, 422, "VALIDATION_FAILED", "JSON document is invalid", nil)
		return
	}
	now := time.Now().UTC()
	sum := sha256.Sum256(body)
	doc, task, err := s.store.CreateDocument(c, domain.Document{Filename: header.Filename, MediaType: ext, ContentHash: hex.EncodeToString(sum[:]), Content: string(body), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "document save failed", nil)
		return
	}
	ok(c, 202, gin.H{"document_id": doc.ID, "indexing_task_id": task.ID, "status": "uploaded"})
}

// documentStatus 处理 GET /api/v1/documents/:id/status：返回文档当前状态行。
func (s *Server) documentStatus(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var v domain.Document
	if lookupFailed(c, s.store.DB.Table("documents").First(&v, id).Error, "could not load document") {
		return
	}
	ok(c, 200, v)
}

// listDocuments 处理 GET /api/v1/documents：按 id 倒序返回最近 100 个文档。
func (s *Server) listDocuments(c *gin.Context) {
	documents := make([]domain.Document, 0)
	if err := s.store.DB.Order("id DESC").Limit(100).Find(&documents).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not list documents", nil)
		return
	}
	ok(c, 200, documents)
}

// deleteDocument 处理 DELETE /api/v1/documents/:id：两阶段删除——先标 deleting 隔离检索证据，
// 再删 Qdrant 向量，最后同事务终止未完成任务并清理 chunks 与元数据。
func (s *Server) deleteDocument(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var document domain.Document
	if lookupFailed(c, s.store.DB.First(&document, id).Error, "could not load document") {
		return
	}
	if document.Status != "deleting" {
		// 先持久化 deleting，立即把文档从证据可见集合中隔离；若 Qdrant 暂时失败，
		// 客户端可安全重试删除，而检索端不会再信任该文档的残留向量。
		result := s.store.DB.Model(&domain.Document{}).Where("id=? AND status=?", id, document.Status).
			Updates(map[string]any{"status": "deleting", "error_message": "", "updated_at": time.Now().UTC()})
		if result.Error != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not mark document for deletion", nil)
			return
		}
		if result.RowsAffected != 1 {
			fail(c, 409, "DOCUMENT_STATE_CHANGED", "document state changed; retry deletion", nil)
			return
		}
	}
	// RAG 未配置（无 vectors）时跳过外部删除，仅清理 MySQL 元数据。
	if s.vectors != nil {
		if err := s.vectors.DeleteDocument(c, id); err != nil {
			fail(c, 503, "DEPENDENCY_UNAVAILABLE", "could not remove Qdrant points; document remains deleting and may be retried", nil)
			return
		}
	}
	if err := s.store.DB.Transaction(func(tx *gorm.DB) error {
		// 外部向量删除成功后，再原子终止未完成索引任务并清理 MySQL 元数据。
		// Qdrant 删除按 document_id 过滤且可重复，重试不会误伤其他文档。
		if document.IndexingTaskID != 0 {
			now := time.Now().UTC()
			var task domain.AITask
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, document.IndexingTaskID).Error; err != nil {
				return err
			}
			if task.Status != domain.TaskSucceeded && task.Status != domain.TaskDead {
				if task.Status == domain.TaskProcessing {
					attemptResult := tx.Model(&domain.TaskAttempt{}).
						Where("task_id=? AND execution_generation=? AND lease_token=? AND status='processing'",
							task.ID, task.ExecutionGeneration, task.LeaseToken).
						Updates(map[string]any{
							"status": "dead", "error_message": "document deleted by user", "finished_at": now,
						})
					if attemptResult.Error != nil {
						return attemptResult.Error
					}
					if attemptResult.RowsAffected != 1 {
						return errors.New("processing document task attempt is missing")
					}
				}
				taskResult := tx.Model(&domain.AITask{}).
					Where("id=? AND status=? AND execution_generation=? AND lease_token=?",
						task.ID, task.Status, task.ExecutionGeneration, task.LeaseToken).
					Updates(map[string]any{
						"status": domain.TaskDead, "last_error": "document deleted by user",
						"lease_token": "", "lease_until": nil, "updated_at": now,
					})
				if taskResult.Error != nil {
					return taskResult.Error
				}
				if taskResult.RowsAffected != 1 {
					return errors.New("document task state changed during deletion")
				}
			}
		}
		if err := tx.Exec("DELETE FROM document_chunks WHERE document_id=?", id).Error; err != nil {
			return err
		}
		return tx.Delete(&document).Error
	}); err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not delete document", nil)
		return
	}
	ok(c, 200, gin.H{"deleted": id})
}

// ragQuery 处理 POST /api/v1/rag/query：混合检索 → 以 MySQL ready 文档复核证据 →
// ChatModel 生成带 [Sn] 引用的答案并强制校验引用；各环节失败均有对应错误码。
func (s *Server) ragQuery(c *gin.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxRAGRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(c, http.StatusRequestEntityTooLarge, "VALIDATION_FAILED", "RAG request body exceeds 64 KiB", nil)
			return
		}
		fail(c, 400, "VALIDATION_FAILED", "RAG request body could not be read", nil)
		return
	}
	var input struct {
		Question string `json:"question"`
		TopK     int    `json:"top_k"`
	}
	if json.Unmarshal(body, &input) != nil || strings.TrimSpace(input.Question) == "" {
		fail(c, 422, "VALIDATION_FAILED", "question is required", nil)
		return
	}
	input.Question = strings.TrimSpace(input.Question)
	if len([]rune(input.Question)) > maxRAGQuestionRunes {
		fail(c, 422, "VALIDATION_FAILED", "question must not exceed 2000 characters", nil)
		return
	}
	// TopK 越界时静默回落默认值：检索参数属于可调项，不当作客户端错误。
	if input.TopK <= 0 || input.TopK > 20 {
		input.TopK = 5
	}
	if s.chat == nil || s.evidence == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "RAG dependencies are not configured", nil)
		return
	}
	requestCtx, cancel := context.WithTimeout(c.Request.Context(), ragRequestTimeout)
	defer cancel()
	evidenceRows, err := s.evidence.Search(requestCtx, input.Question, input.TopK)
	if err != nil {
		var dependencyErr *model.DependencyError
		if errors.As(err, &dependencyErr) {
			failModelDependency(c, err)
		} else {
			fail(c, 503, "DEPENDENCY_UNAVAILABLE", "RAG retrieval failed", nil)
		}
		return
	}
	if len(evidenceRows) == 0 {
		fail(c, 404, "RAG_NO_EVIDENCE", "retrieval returned no valid ready-document evidence", nil)
		return
	}
	citations := make([]gin.H, 0, len(evidenceRows))
	for rank, evidence := range evidenceRows {
		citations = append(citations, gin.H{
			"source": evidence.Source, "document_id": evidence.DocumentID, "chunk_id": evidence.ChunkID,
			"title": evidence.Title, "start_line": evidence.StartLine, "end_line": evidence.EndLine,
			"summary": truncate(evidence.Content, 160), "rank": rank + 1, "score": evidence.Score,
		})
	}
	modelInput, err := json.Marshal(gin.H{"question": input.Question, "evidence": evidenceRows})
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not prepare RAG evidence", nil)
		return
	}
	response, err := s.chat.Generate(requestCtx, model.ChatRequest{
		Skill:  "rag-answer",
		Prompt: `你是证据约束问答助手。只能根据输入 evidence 回答；每个事实后必须使用 [S1] 形式引用对应 source；禁止使用输入之外的事实。只返回 JSON：{"answer":"..."}。`,
		Input:  modelInput,
		ResponseSchema: json.RawMessage(`{
			"type":"object",
			"additionalProperties":false,
			"required":["answer"],
			"properties":{"answer":{"type":"string","minLength":1}}
		}`),
	})
	if err != nil {
		failModelDependency(c, err)
		return
	}
	var generated struct {
		Answer string `json:"answer"`
	}
	if json.Unmarshal([]byte(response.Content), &generated) != nil || strings.TrimSpace(generated.Answer) == "" {
		fail(c, 502, "AI_INVALID_RESPONSE", "model returned an invalid RAG answer", nil)
		return
	}
	// 模型输出视为不可信数据：只保证引用编号存在且落在本次候选范围内，
	// 不把格式校验误称为事实蕴含或语义正确性验证。
	if err := validateCitationReferences(generated.Answer, len(citations)); err != nil {
		fail(c, 502, "AI_INVALID_RESPONSE", err.Error(), nil)
		return
	}
	ok(c, 200, gin.H{
		"answer": generated.Answer, "citations": citations,
		"retriever": "qdrant dense+sparse+rrf", "model": response.Model,
	})
}

func validateCitationReferences(answer string, citationCount int) error {
	references := citationPattern.FindAllStringSubmatch(answer, -1)
	if len(references) == 0 {
		return errors.New("model answer contains no evidence citation")
	}
	for _, reference := range references {
		index, err := strconv.Atoi(reference[1])
		if err != nil || index < 1 || index > citationCount {
			return errors.New("model answer contains an unknown evidence citation")
		}
	}
	return nil
}

// failModelDependency 把模型层错误映射为 HTTP 状态：可重试依赖故障 → 503，永久失败 → 502。
func failModelDependency(c *gin.Context, err error) {
	var dependencyErr *model.DependencyError
	if errors.As(err, &dependencyErr) {
		status := http.StatusBadGateway
		if dependencyErr.Retryable {
			status = http.StatusServiceUnavailable
		}
		fail(c, status, dependencyErr.Code, "AI dependency could not generate an answer", nil)
		return
	}
	fail(c, http.StatusBadGateway, "AI_INVALID_RESPONSE", "AI dependency returned an invalid response", nil)
}

// truncate 摘要截断：优先回退到最近的句子边界标点，找不到才硬截并加省略号。
func truncate(v string, n int) string {
	v = strings.Join(strings.Fields(v), " ")
	r := []rune(v)
	if len(r) <= n {
		return v
	}
	for i := n - 1; i >= n/2; i-- {
		if strings.ContainsRune("。！？；.!?; ", r[i]) {
			return strings.TrimSpace(string(r[:i+1]))
		}
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// evaluateRAG 处理 POST /api/v1/evaluations/rag：接收含真实 chunk id 的 JSONL 数据集，
// 运行 Recall@K/NDCG 评估，并把指标与 Markdown 报告落库。
func (s *Server) evaluateRAG(c *gin.Context) {
	// 评估必须显式提供使用真实 chunk id 的 JSONL，避免把占位数据保存为可信基准。
	body, readErr := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20))
	if readErr != nil {
		fail(c, 413, "VALIDATION_FAILED", "request body is too large or unreadable", readErr.Error())
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 || strings.TrimSpace(string(body)) == "{}" {
		fail(c, 422, "VALIDATION_FAILED", "evaluation dataset with real chunk ids is required", nil)
		return
	}
	cases, err := evaluation.ReadJSONL(strings.NewReader(string(body)))
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", "evaluation dataset must be valid JSONL", err.Error())
		return
	}
	if s.embedding == nil || s.vectors == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "retrieval dependencies are not configured", nil)
		return
	}
	// 评估模式来自运行配置，而不是具体 Go 实现类型，避免替换真实 Provider 后被误标为 fake。
	real := s.aiMode == "real"
	result, err := evaluation.Run(c.Request.Context(), cases, s.embedding, s.vectors, 5, real)
	if err != nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "evaluation retrieval failed", err.Error())
		return
	}
	metrics, _ := json.Marshal(result.Retrievers)
	report := evaluation.Markdown(result)
	datasetSum := sha256.Sum256(body)
	configJSON, _ := json.Marshal(map[string]any{"dense_limit": 20, "sparse_limit": 20, "fusion": "rrf", "evaluation": "retrieval_only"})
	row := map[string]any{
		"mode": result.Mode, "dataset_version": hex.EncodeToString(datasetSum[:]),
		"embedding_model": s.embeddingModel, "collection_name": s.ragCollection,
		"top_k": result.TopK, "config_json": string(configJSON), "metrics_json": string(metrics),
		"report_markdown": report, "status": "succeeded", "created_at": time.Now().UTC(),
	}
	if err := s.store.DB.Table("rag_evaluations").Create(row).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "evaluation result could not be saved", nil)
		return
	}
	ok(c, 202, row)
}

// getEvaluation 处理 GET /api/v1/evaluations/rag/:id：返回一次评估的完整记录。
func (s *Server) getEvaluation(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var row map[string]any
	result := s.store.DB.Table("rag_evaluations").Where("id=?", id).Take(&row)
	if lookupFailed(c, result.Error, "could not load evaluation") {
		return
	}
	ok(c, 200, row)
}

// evaluationMarkdown 处理 GET /api/v1/evaluations/rag/:id/report.md：输出评估报告；
// 用 RowsAffected 区分“不存在”与“查询出错”。
func (s *Server) evaluationMarkdown(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var report string
	result := s.store.DB.Table("rag_evaluations").Select("report_markdown").Where("id=?", id).Scan(&report)
	if result.Error != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load evaluation report", nil)
		return
	}
	if result.RowsAffected == 0 {
		notFound(c)
		return
	}
	c.Data(200, "text/markdown; charset=utf-8", []byte(report))
}

// weekly 处理 GET /api/v1/analytics/weekly：近 7 天聚合（时长、分类、报告管线状态、
// algorithm 趋势），全部直接查询 MySQL 单一事实源。
func (s *Server) weekly(c *gin.Context) {
	since := time.Now().UTC().AddDate(0, 0, -7)
	var summary struct {
		Minutes int `json:"minutes"`
		Records int `json:"records"`
	}
	var categories []struct {
		Category string `json:"category"`
		Count    int    `json:"count"`
	}
	var reports struct {
		Succeeded int `json:"succeeded"`
		Pending   int `json:"pending"`
		Dead      int `json:"dead"`
	}
	var algorithmTrend []struct {
		Day     string `json:"day"`
		Records int    `json:"records"`
	}
	if err := s.store.DB.Raw("SELECT COALESCE(SUM(duration_minute),0) minutes,COUNT(*) records FROM study_records WHERE created_at>=?", since).Scan(&summary).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "weekly query failed", err.Error())
		return
	}
	if err := s.store.DB.Raw(`SELECT sm.category,COUNT(*) count FROM study_modules sm
		JOIN study_records sr ON sr.id=sm.study_record_id WHERE sr.created_at>=?
		GROUP BY sm.category ORDER BY count DESC,sm.category`, since).Scan(&categories).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "weekly query failed", err.Error())
		return
	}
	// MySQL 中 status='succeeded' 求值为 0/1，SUM 即计数；COALESCE 兜底空表为 0。
	if err := s.store.DB.Raw(`SELECT
		COALESCE(SUM(status='succeeded'),0) succeeded,
		COALESCE(SUM(status IN ('pending','queued','processing','retry_wait')),0) pending,
		COALESCE(SUM(status='dead'),0) dead
		FROM ai_tasks WHERE kind='study_report' AND created_at>=?`, since).Scan(&reports).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "weekly query failed", err.Error())
		return
	}
	if err := s.store.DB.Raw(`SELECT DATE(sr.created_at) day,COUNT(DISTINCT sr.id) records
		FROM study_records sr JOIN study_modules sm ON sm.study_record_id=sr.id
		WHERE sr.created_at>=? AND sm.category='algorithm'
		GROUP BY DATE(sr.created_at) ORDER BY DATE(sr.created_at)`, since).Scan(&algorithmTrend).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "weekly query failed", err.Error())
		return
	}
	ok(c, 200, gin.H{
		"period_days": 7, "summary": summary, "module_categories": categories,
		"report_pipeline": reports, "algorithm_trend": algorithmTrend,
		"fact_source": "mysql",
	})
}
