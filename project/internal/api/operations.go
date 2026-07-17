package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	var rows []map[string]any
	query.Order("rt.due_at").Find(&rows)
	ok(c, 200, rows)
}

func (s *Server) completeReview(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var input struct {
		Mastery int `json:"mastery"`
	}
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 422, "VALIDATION_FAILED", "mastery is required", nil)
		return
	}
	interval, err := domain.ReviewInterval(input.Mastery)
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", err.Error(), nil)
		return
	}
	var task domain.ReviewTask
	if s.store.DB.First(&task, id).Error != nil {
		notFound(c)
		return
	}
	now := time.Now().UTC()
	next := now.Add(interval)
	updated := false
	err = s.store.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("UPDATE review_tasks SET mastery=?,status='scheduled',due_at=?,completed_at=?,updated_at=? WHERE id=? AND status='scheduled'", input.Mastery, next, now, now, id)
		if result.Error != nil || result.RowsAffected != 1 {
			return result.Error
		}
		event := domain.ReviewEvent{ReviewTaskID: id, Action: "complete", OldMastery: task.Mastery, NewMastery: input.Mastery, OldDueAt: task.DueAt, NewDueAt: next, CreatedAt: now}
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
		fail(c, 409, "VALIDATION_FAILED", "review is not scheduled", nil)
		return
	}
	ok(c, 200, gin.H{"id": id, "mastery": input.Mastery, "due_at": next})
}

func (s *Server) skipReview(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var task domain.ReviewTask
	if s.store.DB.First(&task, id).Error != nil {
		notFound(c)
		return
	}
	now := time.Now().UTC()
	next := now.Add(24 * time.Hour)
	updated := false
	err := s.store.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("UPDATE review_tasks SET due_at=?,updated_at=? WHERE id=? AND status='scheduled'", next, now, id)
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
		fail(c, 409, "VALIDATION_FAILED", "review is not scheduled", nil)
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
		for _, route := range result.Routes {
			response, exists := result.Outputs[route]
			if !exists {
				continue
			}
			definition, _ := s.skills.Get(route)
			hash, _ := s.skills.PromptHash(route)
			_, _ = s.recorder.Record(0, definition, hash, string(body), response, time.Since(started), result.Tools[route], nil)
		}
		if runErr != nil {
			fail(c, 500, "INTERNAL_ERROR", "multi-agent workflow failed", runErr.Error())
			return
		}
		ok(c, 200, result)
		return
	}
	output, executions, err := s.skills.RunDetailed(c, c.Param("name"), body)
	if err != nil {
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
	s.store.DB.Table("agent_runs").Where("id=?", id).Take(&run)
	if len(run) == 0 {
		notFound(c)
		return
	}
	var steps, tools []map[string]any
	s.store.DB.Table("agent_steps").Where("agent_run_id=?", id).Order("id").Find(&steps)
	s.store.DB.Table("tool_calls").Where("agent_run_id=?", id).Order("id").Find(&tools)
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
	ext := strings.ToLower(header.Filename[strings.LastIndex(header.Filename, ".")+1:])
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
	if s.store.DB.Table("documents").First(&v, id).Error != nil {
		notFound(c)
		return
	}
	ok(c, 200, v)
}
func (s *Server) listDocuments(c *gin.Context) {
	var documents []domain.Document
	s.store.DB.Order("id DESC").Limit(100).Find(&documents)
	ok(c, 200, documents)
}
func (s *Server) deleteDocument(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var document domain.Document
	if s.store.DB.First(&document, id).Error != nil {
		notFound(c)
		return
	}
	if s.vectors != nil {
		if err := s.vectors.DeleteDocument(c, id); err != nil {
			fail(c, 503, "DEPENDENCY_UNAVAILABLE", "could not remove Qdrant points", nil)
			return
		}
	}
	if err := s.store.DB.Transaction(func(tx *gorm.DB) error {
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
	s.store.DB.Model(&domain.Document{}).Where("status='ready'").Count(&readyCount)
	if readyCount == 0 {
		fail(c, 404, "RAG_NO_EVIDENCE", "no ready document supports this question", nil)
		return
	}
	if s.embedding == nil || s.vectors == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "hybrid retriever is not configured", nil)
		return
	}
	vectors, err := s.embedding.Embed(c, []string{input.Question})
	if err != nil || len(vectors) != 1 {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "embedding query failed", nil)
		return
	}
	hits, err := s.vectors.Hybrid(c, vectors[0], rag.Sparse(input.Question), input.TopK)
	if err != nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "Qdrant hybrid query failed", nil)
		return
	}
	citations := make([]gin.H, 0, len(hits))
	for _, hit := range hits {
		chunkID, _ := hit.Payload["chunk_id"].(string)
		var chunk domain.DocumentChunk
		result := s.store.DB.Table("document_chunks dc").Select("dc.*").Joins("JOIN documents d ON d.id=dc.document_id").Where("dc.id=? AND d.status='ready'", chunkID).Take(&chunk)
		if result.Error != nil {
			continue
		}
		rank := len(citations) + 1
		citations = append(citations, gin.H{"source": fmt.Sprintf("S%d", rank), "document_id": chunk.DocumentID, "chunk_id": chunk.ID, "title": chunk.Title, "start_line": chunk.StartLine, "end_line": chunk.EndLine, "summary": truncate(chunk.Content, 160), "rank": rank, "score": hit.Score})
	}
	if len(citations) == 0 {
		fail(c, 404, "RAG_NO_EVIDENCE", "retrieval returned no valid ready-document evidence", nil)
		return
	}
	firstSummary, _ := citations[0]["summary"].(string)
	answer := fmt.Sprintf("关于“%s”，首条可核验证据指出：%s [S1]", input.Question, firstSummary)
	ok(c, 200, gin.H{"answer": answer, "citations": citations, "retriever": "qdrant dense+sparse+rrf"})
}
func truncate(v string, n int) string {
	r := []rune(v)
	if len(r) <= n {
		return v
	}
	return string(r[:n])
}

func (s *Server) evaluateRAG(c *gin.Context) {
	if s.embedding == nil || s.vectors == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "retrieval dependencies are not configured", nil)
		return
	}
	body, _ := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20))
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
	s.store.DB.Table("rag_evaluations").Where("id=?", id).Take(&row)
	if len(row) == 0 {
		notFound(c)
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
	s.store.DB.Raw("SELECT COALESCE(SUM(duration_minute),0) minutes,COUNT(*) records FROM study_records WHERE created_at>=?", since).Scan(&summary)
	s.store.DB.Raw(`SELECT sm.category,COUNT(*) count FROM study_modules sm
		JOIN study_records sr ON sr.id=sm.study_record_id WHERE sr.created_at>=?
		GROUP BY sm.category ORDER BY count DESC,sm.category`, since).Scan(&categories)
	s.store.DB.Raw(`SELECT
		COALESCE(SUM(status='succeeded'),0) succeeded,
		COALESCE(SUM(status IN ('pending','queued','processing','retry_wait')),0) pending,
		COALESCE(SUM(status='dead'),0) dead
		FROM ai_tasks WHERE kind='study_report' AND created_at>=?`, since).Scan(&reports)
	s.store.DB.Raw(`SELECT DATE(sr.created_at) day,COUNT(DISTINCT sr.id) records
		FROM study_records sr JOIN study_modules sm ON sm.study_record_id=sr.id
		WHERE sr.created_at>=? AND sm.category='algorithm'
		GROUP BY DATE(sr.created_at) ORDER BY DATE(sr.created_at)`, since).Scan(&algorithmTrend)
	ok(c, 200, gin.H{
		"period_days": 7, "summary": summary, "module_categories": categories,
		"report_pipeline": reports, "algorithm_trend": algorithmTrend,
		"fact_source": "mysql",
	})
}
