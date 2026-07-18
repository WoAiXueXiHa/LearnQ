package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/evaluation"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/rag"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) dueReviews(c *gin.Context) {
	c.Request.URL.RawQuery = "scope=due"
	s.reviewTasks(c)
}

func (s *Server) reviewTasks(c *gin.Context) {
	scope := c.DefaultQuery("scope", "all")
	now := time.Now().UTC()
	query := s.store.DB.Table("review_tasks rt").
		Select(`rt.*, r.task_id, r.markdown_content,
			COALESCE(sr.title,'学习报告') record_title`).
		Joins("JOIN reports r ON r.id=rt.report_id").
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

func (s *Server) runSkill(c *gin.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if err != nil || !json.Valid(body) {
		fail(c, 422, "VALIDATION_FAILED", "valid JSON input is required", nil)
		return
	}
	started := time.Now()
	if c.Param("name") == "multi-agent" {
		var workflowInput struct {
			Modules []string `json:"modules"`
		}
		_ = json.Unmarshal(body, &workflowInput)
		result, runErr := s.skills.RunEinoWorkflow(c, workflowInput.Modules, body)
		runIDs := make(map[string]uint64, len(result.Routes))
		for _, route := range result.Routes {
			response, exists := result.Outputs[route]
			if !exists {
				continue
			}
			definition, _ := s.skills.Get(route)
			hash, _ := s.skills.PromptHash(route)
			runID, recordErr := s.recorder.Record(0, definition, hash, string(body), response, time.Since(started), result.Tools[route], nil)
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
	output, executions, err := s.skills.RunDetailed(c, c.Param("name"), body)
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
	runID, recordErr := s.recorder.Record(0, definition, hash, string(body), output, time.Since(started), executions, nil)
	if recordErr != nil {
		fail(c, 500, "INTERNAL_ERROR", "trace could not be persisted", nil)
		return
	}
	ok(c, 200, gin.H{"agent_run_id": runID, "output": json.RawMessage(output.Content)})
}

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

func (s *Server) uploadDocument(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", "multipart file is required", nil)
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, (5<<20)+1))
	if err != nil || !rag.ValidDocument(body) {
		fail(c, 422, "VALIDATION_FAILED", "file must be non-empty UTF-8 and at most 5 MiB", nil)
		return
	}
	if len(body) > 5<<20 {
		fail(c, 413, "VALIDATION_FAILED", "file exceeds 5 MiB limit", nil)
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
func (s *Server) listDocuments(c *gin.Context) {
	documents := make([]domain.Document, 0)
	if err := s.store.DB.Order("id DESC").Limit(100).Find(&documents).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not list documents", nil)
		return
	}
	ok(c, 200, documents)
}
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
	if s.vectors != nil {
		if err := s.vectors.DeleteDocument(c, id); err != nil {
			fail(c, 503, "DEPENDENCY_UNAVAILABLE", "could not remove Qdrant points; document remains deleting and may be retried", nil)
			return
		}
	}
	if err := s.store.DB.Transaction(func(tx *gorm.DB) error {
		if document.IndexingTaskID != 0 {
			now := time.Now().UTC()
			if err := tx.Model(&domain.AITask{}).Where("id=? AND status NOT IN ('succeeded','dead')", document.IndexingTaskID).
				Updates(map[string]any{
					"status": domain.TaskDead, "last_error": "document deleted by user",
					"lease_token": "", "lease_until": nil, "updated_at": now,
				}).Error; err != nil {
				return err
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

func (s *Server) ragQuery(c *gin.Context) {
	var input struct {
		Question string `json:"question"`
		TopK     int    `json:"top_k"`
	}
	if c.ShouldBindJSON(&input) != nil || strings.TrimSpace(input.Question) == "" {
		fail(c, 422, "VALIDATION_FAILED", "question is required", nil)
		return
	}
	if input.TopK <= 0 || input.TopK > 20 {
		input.TopK = 5
	}
	var readyCount int64
	if err := s.store.DB.Model(&domain.Document{}).Where("status='ready'").Count(&readyCount).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not check ready documents", nil)
		return
	}
	if readyCount == 0 {
		fail(c, 404, "RAG_NO_EVIDENCE", "no ready document supports this question", nil)
		return
	}
	if s.chat == nil || s.embedding == nil || s.vectors == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "RAG dependencies are not configured", nil)
		return
	}
	vectors, err := s.embedding.Embed(c, []string{input.Question})
	if err != nil {
		failModelDependency(c, err)
		return
	}
	if len(vectors) != 1 {
		fail(c, 502, "AI_INVALID_RESPONSE", "embedding query returned an invalid vector count", nil)
		return
	}
	hits, err := s.vectors.Hybrid(c, vectors[0], rag.Sparse(input.Question), input.TopK)
	if err != nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "Qdrant hybrid query failed", nil)
		return
	}
	citations := make([]gin.H, 0, len(hits))
	evidence := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		chunkID, _ := hit.Payload["chunk_id"].(string)
		var chunk domain.DocumentChunk
		result := s.store.DB.Table("document_chunks dc").Select("dc.*").Joins("JOIN documents d ON d.id=dc.document_id").Where("dc.id=? AND d.status='ready'", chunkID).Take(&chunk)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			continue
		}
		if result.Error != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not load retrieval evidence", nil)
			return
		}
		rank := len(citations) + 1
		source := fmt.Sprintf("S%d", rank)
		citations = append(citations, gin.H{"source": source, "document_id": chunk.DocumentID, "chunk_id": chunk.ID, "title": chunk.Title, "start_line": chunk.StartLine, "end_line": chunk.EndLine, "summary": truncate(chunk.Content, 160), "rank": rank, "score": hit.Score})
		evidence = append(evidence, map[string]any{
			"source": source, "title": chunk.Title, "start_line": chunk.StartLine,
			"end_line": chunk.EndLine, "content": chunk.Content,
		})
	}
	if len(citations) == 0 {
		fail(c, 404, "RAG_NO_EVIDENCE", "retrieval returned no valid ready-document evidence", nil)
		return
	}
	modelInput, err := json.Marshal(gin.H{"question": input.Question, "evidence": evidence})
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not prepare RAG evidence", nil)
		return
	}
	response, err := s.chat.Generate(c, model.ChatRequest{
		Skill:  "rag-answer",
		Prompt: `你是证据约束问答助手。只能根据输入 evidence 回答；每个事实后必须使用 [S1] 形式引用对应 source；禁止使用输入之外的事实。只返回 JSON：{"answer":"..."}。`,
		Input:  modelInput,
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
	references := regexp.MustCompile(`\[S([0-9]+)\]`).FindAllStringSubmatch(generated.Answer, -1)
	if len(references) == 0 {
		fail(c, 502, "AI_INVALID_RESPONSE", "model answer contains no evidence citation", nil)
		return
	}
	for _, reference := range references {
		index, _ := strconv.Atoi(reference[1])
		if index < 1 || index > len(citations) {
			fail(c, 502, "AI_INVALID_RESPONSE", "model answer contains an unknown evidence citation", nil)
			return
		}
	}
	ok(c, 200, gin.H{
		"answer": generated.Answer, "citations": citations,
		"retriever": "qdrant dense+sparse+rrf", "model": response.Model,
	})
}

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

func (s *Server) evaluateRAG(c *gin.Context) {
	if s.embedding == nil || s.vectors == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "retrieval dependencies are not configured", nil)
		return
	}
	body, readErr := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20))
	if readErr != nil {
		fail(c, 413, "VALIDATION_FAILED", "request body is too large or unreadable", readErr.Error())
		return
	}
	var cases []evaluation.Case
	var err error
	if len(strings.TrimSpace(string(body))) == 0 || strings.TrimSpace(string(body)) == "{}" {
		cases, err = evaluation.DefaultCases()
	} else {
		cases, err = evaluation.ReadJSONL(strings.NewReader(string(body)))
	}
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", "evaluation dataset must be valid JSONL", err.Error())
		return
	}
	_, real := s.embedding.(*model.OpenAICompatible)
	result, err := evaluation.Run(c, cases, s.embedding, s.vectors, 5, real)
	if err != nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "evaluation retrieval failed", err.Error())
		return
	}
	metrics, _ := json.Marshal(result.Retrievers)
	report := evaluation.Markdown(result)
	modelName := "learnq-fake-embedding-v1"
	if real {
		modelName = "openai-compatible"
	}
	row := map[string]any{"mode": result.Mode, "dataset_version": "v1", "embedding_model": modelName, "collection_name": "learnq_chunks", "top_k": result.TopK, "config_json": `{"dense_limit":20,"sparse_limit":20,"fusion":"rrf"}`, "metrics_json": string(metrics), "report_markdown": report, "status": "succeeded", "created_at": time.Now().UTC()}
	if err := s.store.DB.Table("rag_evaluations").Create(row).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "evaluation result could not be saved", nil)
		return
	}
	ok(c, 202, row)
}
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
