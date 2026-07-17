package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/evaluation"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/skill"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/trace"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/web"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Server struct {
	store     *store.Store
	skills    *skill.Registry
	engine    *gin.Engine
	recorder  trace.Recorder
	embedding model.EmbeddingModel
	vectors   interface {
		evaluation.Retriever
		DeleteDocument(context.Context, uint64) error
	}
}

type errBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Details   any    `json:"details,omitempty"`
}

type Option func(*Server)

func WithRAG(embedding model.EmbeddingModel, vectors interface {
	evaluation.Retriever
	DeleteDocument(context.Context, uint64) error
}) Option {
	return func(server *Server) {
		server.embedding = embedding
		server.vectors = vectors
	}
}

func New(s *store.Store, skills *skill.Registry, options ...Option) *Server {
	gin.SetMode(gin.ReleaseMode)
	server := &Server{store: s, skills: skills, engine: gin.New(), recorder: trace.Recorder{DB: s.DB}}
	for _, option := range options {
		option(server)
	}
	server.engine.Use(gin.Recovery(), requestID())
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler { return s.engine }

func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			var raw [12]byte
			_, _ = rand.Read(raw[:])
			id = hex.EncodeToString(raw[:])
		}
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func ok(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"data": data, "request_id": c.GetString("request_id")})
}
func fail(c *gin.Context, status int, code, message string, details any) {
	c.AbortWithStatusJSON(status, gin.H{"error": errBody{Code: code, Message: message, RequestID: c.GetString("request_id"), Details: details}})
}

func (s *Server) routes() {
	r := s.engine
	r.Any("/", gin.WrapH(web.Handler()))
	r.GET("/static/*filepath", gin.WrapH(web.Handler()))
	r.GET("/health/live", func(c *gin.Context) { ok(c, 200, gin.H{"status": "live"}) })
	r.GET("/health/ready", s.ready)
	v1 := r.Group("/api/v1")
	v1.POST("/study-records", s.createStudyRecord)
	v1.GET("/study-records", s.listStudyRecords)
	v1.GET("/study-records/:id", s.getStudyRecord)
	v1.GET("/tasks/:id", s.getTask)
	v1.GET("/tasks/:id/detail", s.taskDetail)
	v1.GET("/tasks/:id/trace", s.trace)
	v1.POST("/tasks/:id/retry", s.retryTask)
	v1.GET("/reports/:id", s.getReport)
	v1.GET("/reports/:id/report.md", s.reportMarkdown)
	v1.GET("/review-tasks/due", s.dueReviews)
	v1.GET("/review-tasks", s.reviewTasks)
	v1.POST("/review-tasks/:id/complete", s.completeReview)
	v1.POST("/review-tasks/:id/skip", s.skipReview)
	v1.GET("/skills", func(c *gin.Context) { ok(c, 200, s.skills.List()) })
	v1.POST("/skills/:name/runs", s.runSkill)
	v1.GET("/agent-runs/:id", s.agentRun)
	v1.POST("/documents", s.uploadDocument)
	v1.GET("/documents", s.listDocuments)
	v1.GET("/documents/:id/status", s.documentStatus)
	v1.DELETE("/documents/:id", s.deleteDocument)
	v1.POST("/rag/query", s.ragQuery)
	v1.POST("/evaluations/rag", s.evaluateRAG)
	v1.GET("/evaluations/rag/:id", s.getEvaluation)
	v1.GET("/evaluations/rag/:id/report.md", s.evaluationMarkdown)
	v1.GET("/analytics/weekly", s.weekly)
}

func (s *Server) ready(c *gin.Context) {
	sqlDB, err := s.store.DB.DB()
	if err != nil || sqlDB.PingContext(c) != nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "MySQL is unavailable", nil)
		return
	}
	ok(c, 200, gin.H{"status": "ready"})
}

func parseID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, 400, "VALIDATION_FAILED", "invalid id", nil)
		return 0, false
	}
	return id, true
}

func (s *Server) createStudyRecord(c *gin.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if err != nil {
		fail(c, 400, "VALIDATION_FAILED", "invalid request body", nil)
		return
	}
	var input struct {
		Title           string `json:"title"`
		Summary         string `json:"summary"`
		DurationMinutes int    `json:"duration_minutes"`
		ForceCreate     bool   `json:"force_create"`
		Modules         []struct {
			Category string `json:"category"`
			Content  string `json:"content"`
		} `json:"modules"`
	}
	if json.Unmarshal(body, &input) != nil || strings.TrimSpace(input.Title) == "" || input.DurationMinutes <= 0 || len(input.Modules) == 0 {
		fail(c, 422, "VALIDATION_FAILED", "title, positive duration_minutes and modules are required", nil)
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if len(key) > 128 {
		fail(c, 422, "VALIDATION_FAILED", "Idempotency-Key exceeds 128 bytes", nil)
		return
	}
	scope := "POST:/api/v1/study-records:" + key
	hash := store.HashRequest(body)
	modules := make([]domain.StudyModule, len(input.Modules))
	for i, item := range input.Modules {
		modules[i] = domain.StudyModule{Category: item.Category, Content: item.Content}
	}
	canonical, _ := json.Marshal(struct {
		Title           string               `json:"title"`
		Summary         string               `json:"summary"`
		DurationMinutes int                  `json:"duration_minutes"`
		Modules         []domain.StudyModule `json:"modules"`
	}{strings.TrimSpace(input.Title), strings.TrimSpace(input.Summary), input.DurationMinutes, modules})
	fingerprint := store.HashRequest(canonical)
	var encoded []byte
	var statusCode = http.StatusAccepted
	var conflict bool
	err = s.store.DB.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if key != "" {
			result := tx.Exec(`INSERT IGNORE INTO idempotency_keys(scope,request_hash,status_code,response_json,created_at)
				VALUES(?,?,0,'{}',?)`, scope, hash, time.Now().UTC())
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				var saved struct {
					RequestHash, ResponseJSON string
					StatusCode                int
				}
				if err := tx.Raw(`SELECT request_hash,response_json,status_code FROM idempotency_keys
					WHERE scope=? FOR UPDATE`, scope).Scan(&saved).Error; err != nil {
					return err
				}
				if saved.RequestHash != hash {
					conflict = true
					return nil
				}
				if saved.StatusCode != 0 {
					statusCode, encoded = saved.StatusCode, []byte(saved.ResponseJSON)
					return nil
				}
			}
		}
		var record domain.StudyRecord
		var task domain.AITask
		deduplicated := false
		if !input.ForceCreate {
			result := tx.Preload("Modules").Where("content_fingerprint=? AND created_at>=?", fingerprint, time.Now().UTC().Add(-10*time.Minute)).
				Order("id DESC").First(&record)
			if result.Error == nil {
				deduplicated = true
				if err := tx.Where(`kind='study_report' AND JSON_UNQUOTE(JSON_EXTRACT(payload_json,'$.study_record_id'))=?`, record.ID).
					Order("id DESC").First(&task).Error; err != nil {
					return err
				}
			} else if result.Error != gorm.ErrRecordNotFound {
				return result.Error
			}
		}
		if !deduplicated {
			var createErr error
			record, task, createErr = store.CreateStudyRecordTx(tx, store.CreateRecord{
				Title: strings.TrimSpace(input.Title), Summary: strings.TrimSpace(input.Summary),
				DurationMinutes: input.DurationMinutes, ContentFingerprint: fingerprint, Modules: modules,
			})
			if createErr != nil {
				return createErr
			}
		}
		payload := gin.H{"data": gin.H{"study_record": record, "task_id": task.ID, "deduplicated": deduplicated}, "request_id": c.GetString("request_id")}
		encoded, _ = json.Marshal(payload)
		if key != "" {
			return tx.Exec("UPDATE idempotency_keys SET status_code=?,response_json=? WHERE scope=?",
				statusCode, string(encoded), scope).Error
		}
		return nil
	})
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not create record", nil)
		return
	}
	if conflict {
		fail(c, 409, "IDEMPOTENCY_CONFLICT", "key was used with a different request", nil)
		return
	}
	c.Data(statusCode, "application/json", encoded)
}

func (s *Server) getStudyRecord(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var record domain.StudyRecord
	if err := s.store.DB.Preload("Modules").First(&record, id).Error; err != nil {
		notFound(c)
		return
	}
	ok(c, 200, record)
}
func (s *Server) listStudyRecords(c *gin.Context) {
	var records []domain.StudyRecord
	s.store.DB.Preload("Modules").Order("id DESC").Limit(100).Find(&records)
	type item struct {
		domain.StudyRecord
		TaskID     uint64            `json:"task_id"`
		TaskStatus domain.TaskStatus `json:"task_status"`
	}
	result := make([]item, 0, len(records))
	for _, record := range records {
		var task domain.AITask
		s.store.DB.Where(`kind='study_report' AND JSON_UNQUOTE(JSON_EXTRACT(payload_json,'$.study_record_id'))=?`, record.ID).
			Order("id DESC").First(&task)
		result = append(result, item{StudyRecord: record, TaskID: task.ID, TaskStatus: task.Status})
	}
	ok(c, 200, result)
}
func (s *Server) getTask(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var task domain.AITask
	if err := s.store.DB.First(&task, id).Error; err != nil {
		notFound(c)
		return
	}
	ok(c, 200, task)
}
func (s *Server) taskDetail(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var task domain.AITask
	if err := s.store.DB.First(&task, id).Error; err != nil {
		notFound(c)
		return
	}
	var payload struct {
		StudyRecordID uint64 `json:"study_record_id"`
		DocumentID    uint64 `json:"document_id"`
	}
	_ = json.Unmarshal([]byte(task.PayloadJSON), &payload)
	var record *domain.StudyRecord
	if payload.StudyRecordID != 0 {
		var value domain.StudyRecord
		if s.store.DB.Preload("Modules").First(&value, payload.StudyRecordID).Error == nil {
			record = &value
		}
	}
	var report *domain.Report
	var review *domain.ReviewTask
	var reportValue domain.Report
	if s.store.DB.Where("task_id=?", task.ID).First(&reportValue).Error == nil {
		report = &reportValue
		var reviewValue domain.ReviewTask
		if s.store.DB.Where("report_id=?", reportValue.ID).First(&reviewValue).Error == nil {
			review = &reviewValue
		}
	}
	traceData := s.loadTrace(task.ID)
	ok(c, 200, gin.H{
		"task": task, "study_record": record, "report": report, "review_task": review,
		"trace": traceData, "document_id": payload.DocumentID,
	})
}
func (s *Server) trace(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	ok(c, 200, s.loadTrace(id))
}

func (s *Server) loadTrace(id uint64) gin.H {
	var attempts []domain.TaskAttempt
	s.store.DB.Where("task_id=?", id).Order("execution_generation,attempt_no").Find(&attempts)
	var runs []map[string]any
	s.store.DB.Table("agent_runs").Where("task_id=?", id).Order("id").Find(&runs)
	runIDs := make([]uint64, 0, len(runs))
	for _, run := range runs {
		switch value := run["id"].(type) {
		case uint64:
			runIDs = append(runIDs, value)
		case int64:
			runIDs = append(runIDs, uint64(value))
		}
	}
	var steps, tools []map[string]any
	if len(runIDs) > 0 {
		s.store.DB.Table("agent_steps").Where("agent_run_id IN ?", runIDs).Order("id").Find(&steps)
		s.store.DB.Table("tool_calls").Where("agent_run_id IN ?", runIDs).Order("id").Find(&tools)
	}
	return gin.H{"attempts": attempts, "agent_runs": runs, "agent_steps": steps, "tool_calls": tools}
}
func (s *Server) retryTask(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	now := time.Now().UTC()
	result := s.store.DB.Exec(`UPDATE ai_tasks SET status='pending',attempt_no=0,execution_generation=execution_generation+1,
		available_at=?,last_error='',updated_at=? WHERE id=? AND status='dead'`, now, now, id)
	if result.RowsAffected != 1 {
		fail(c, 409, "TASK_NOT_RETRYABLE", "only dead tasks may be retried", nil)
		return
	}
	payload, _ := json.Marshal(gin.H{"task_id": id})
	s.store.DB.Create(&domain.OutboxEvent{AggregateID: id, EventType: "ai_task.manual_retry", PayloadJSON: string(payload), CreatedAt: now})
	ok(c, 202, gin.H{"task_id": id, "status": "pending"})
}
func (s *Server) getReport(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var v domain.Report
	if s.store.DB.First(&v, id).Error != nil {
		notFound(c)
		return
	}
	ok(c, 200, v)
}
func (s *Server) reportMarkdown(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var v domain.Report
	if s.store.DB.First(&v, id).Error != nil {
		notFound(c)
		return
	}
	c.Data(200, "text/markdown; charset=utf-8", []byte(v.MarkdownContent))
}
func notFound(c *gin.Context) { fail(c, 404, "VALIDATION_FAILED", "resource not found", nil) }
