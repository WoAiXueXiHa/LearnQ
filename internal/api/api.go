package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/evaluation"
	"github.com/WoAiXueXiHa/LearnQ/internal/imagestore"
	appmetrics "github.com/WoAiXueXiHa/LearnQ/internal/metrics"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/WoAiXueXiHa/LearnQ/internal/skill"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
	"github.com/WoAiXueXiHa/LearnQ/internal/trace"
	"github.com/WoAiXueXiHa/LearnQ/internal/web"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Server struct {
	// Server 通过 Option 注入可选外部能力；基础 CRUD 测试无需启动 Redis/Qdrant/真实模型。
	// vectors 抽象检索与文档删除两个能力，避免 api 直接依赖 rag 的具体实现类型。
	store      *store.Store
	skills     *skill.Registry
	engine     *gin.Engine
	recorder   trace.Recorder
	chat       model.ChatModel
	imageStore *imagestore.Store
	embedding  model.EmbeddingModel
	evidence   *rag.EvidenceService
	vectors    interface {
		evaluation.Retriever
		DeleteDocument(context.Context, uint64) error
	}
	redisCheck     func(context.Context) error
	qdrantCheck    func(context.Context) error
	workerCheck    func(context.Context) (bool, error)
	queueDepth     func(context.Context) (int64, int64, error)
	metrics        *appmetrics.Recorder
	aiMode         string
	chatModel      string
	visionModel    string
	embeddingModel string
	ragCollection  string
}

// 学习记录写入的硬性上限：请求体 1 MiB、标题 255 rune、摘要与模块内容各 32 KiB、
// 模块数 50 个、时长不超过 24 小时，防止超限输入消耗过多内存与存储。
const (
	maxStudyRecordBody   = 1 << 20
	maxStudyTitleRunes   = 255
	maxStudySummaryBytes = 32 << 10
	maxModuleCount       = 50
	maxModuleCategory    = 64
	maxModuleContent     = 32 << 10
	maxDurationMinutes   = 24 * 60
)

type errBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Details   any    `json:"details,omitempty"`
}

// Option 以闭包方式注入 Server 的可选依赖，避免构造函数参数列表随能力增长而膨胀。
type Option func(*Server)

func WithRAG(chat model.ChatModel, embedding model.EmbeddingModel, vectors interface {
	evaluation.Retriever
	DeleteDocument(context.Context, uint64) error
}) Option {
	return func(server *Server) {
		server.chat = chat
		server.embedding = embedding
		server.vectors = vectors
		server.evidence = &rag.EvidenceService{DB: server.store.DB, Embedding: embedding, Vectors: vectors}
	}
}

func WithEvidence(evidence *rag.EvidenceService) Option {
	return func(server *Server) { server.evidence = evidence }
}

func WithHealthChecks(redisCheck, qdrantCheck func(context.Context) error) Option {
	return func(server *Server) {
		server.redisCheck = redisCheck
		server.qdrantCheck = qdrantCheck
	}
}

func WithWorkerCheck(check func(context.Context) (bool, error)) Option {
	return func(server *Server) {
		server.workerCheck = check
	}
}

func WithOperationalMetrics(recorder *appmetrics.Recorder, queueDepth func(context.Context) (int64, int64, error)) Option {
	return func(server *Server) {
		server.metrics = recorder
		server.queueDepth = queueDepth
	}
}

func WithImageStore(images *imagestore.Store) Option {
	return func(server *Server) { server.imageStore = images }
}

func WithRuntimeInfo(mode, chatModel, visionModel, embeddingModel string) Option {
	return func(server *Server) {
		server.aiMode = mode
		server.chatModel = chatModel
		server.visionModel = visionModel
		server.embeddingModel = embeddingModel
	}
}

func WithRAGMetadata(collection string) Option {
	return func(server *Server) { server.ragCollection = collection }
}

// New 组装 Server：开启 ReleaseMode，应用 Option 注入，挂载 Recovery 与 requestID 中间件后注册路由。
func New(s *store.Store, skills *skill.Registry, options ...Option) *Server {
	gin.SetMode(gin.ReleaseMode)
	server := &Server{store: s, skills: skills, engine: gin.New(), recorder: trace.Recorder{DB: s.DB}}
	server.engine.ContextWithFallback = true
	for _, option := range options {
		option(server)
	}
	server.engine.Use(gin.Recovery(), requestID(), metricMiddleware(server.metrics))
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler { return s.engine }

// requestID 中间件：request id 写入 gin.Context 并回写 X-Request-ID 响应头，响应体中的 request_id 复用同一标识。
func requestID() gin.HandlerFunc {
	// workflow_id 列上限为 64；只透传可安全用于日志和数据库关联的 ASCII 标识。
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if !validRequestID(id) {
			var raw [12]byte
			if _, err := rand.Read(raw[:]); err == nil {
				id = hex.EncodeToString(raw[:])
			} else {
				id = strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
			}
		}
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return false
	}
	return true
}

// ok 统一成功响应格式：{"data":…,"request_id":…}，保证所有接口响应结构一致。
func ok(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"data": data, "request_id": c.GetString("request_id")})
}

// fail 统一错误响应格式并中止 handler 链（Abort），status/code/message 由调用方按语义指定。
func fail(c *gin.Context, status int, code, message string, details any) {
	c.AbortWithStatusJSON(status, gin.H{"error": errBody{Code: code, Message: message, RequestID: c.GetString("request_id"), Details: details}})
}

// routes 注册全部路由：Web 页面与静态资源、存活/就绪探针，以及 /api/v1 业务分组。
func (s *Server) routes() {
	r := s.engine
	r.Any("/", gin.WrapH(web.Handler()))
	r.GET("/static/*filepath", gin.WrapH(web.Handler()))
	r.GET("/health/live", func(c *gin.Context) { ok(c, 200, gin.H{"status": "live"}) })
	r.GET("/health/ready", s.ready)
	r.GET("/metrics", s.prometheusMetrics)
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
	v1.POST("/documents/:id/reindex", s.reindexDocument)
	v1.DELETE("/documents/:id", s.deleteDocument)
	v1.POST("/images", s.uploadImage)
	v1.GET("/images", s.listImages)
	v1.GET("/images/:id", s.getImage)
	v1.GET("/images/:id/content", s.imageContent)
	v1.POST("/images/:id/retry", s.retryImage)
	v1.POST("/images/:id/index", s.addImageToKnowledgeBase)
	v1.DELETE("/images/:id", s.deleteImage)
	v1.POST("/rag/query", s.ragQuery)
	v1.POST("/evaluations/rag", s.evaluateRAG)
	v1.GET("/evaluations/rag/:id", s.getEvaluation)
	v1.GET("/evaluations/rag/:id/report.md", s.evaluationMarkdown)
	v1.GET("/analytics/weekly", s.weekly)
}

// ready 是就绪探针：MySQL、Redis 和 Worker 是核心链路依赖；Qdrant 作为 RAG 可选能力单独报告状态。
func (s *Server) ready(c *gin.Context) {
	// live 只回答进程是否存活；ready 才检查接流量所需依赖和 Worker 心跳。
	// 每项独立设置短超时，防止单个故障依赖拖住整个探针。
	dependencies := gin.H{"mysql": "ok", "redis": "not_configured", "qdrant": "not_configured", "worker": "not_configured"}
	unavailable := make([]string, 0, 4)
	sqlDB, err := s.store.DB.DB()
	requestCtx := c.Request.Context()
	mysqlCtx, mysqlCancel := context.WithTimeout(requestCtx, 2*time.Second)
	if err != nil || sqlDB.PingContext(mysqlCtx) != nil {
		dependencies["mysql"] = "unavailable"
		unavailable = append(unavailable, "mysql")
	}
	mysqlCancel()
	if s.redisCheck != nil {
		dependencies["redis"] = "ok"
		checkCtx, cancel := context.WithTimeout(requestCtx, 2*time.Second)
		if err := s.redisCheck(checkCtx); err != nil {
			dependencies["redis"] = "unavailable"
			unavailable = append(unavailable, "redis")
		}
		cancel()
	}
	if s.qdrantCheck != nil {
		dependencies["qdrant"] = "ok"
		checkCtx, cancel := context.WithTimeout(requestCtx, 2*time.Second)
		if err := s.qdrantCheck(checkCtx); err != nil {
			dependencies["qdrant"] = "unavailable"
		}
		cancel()
	}
	if s.workerCheck != nil {
		dependencies["worker"] = "ok"
		checkCtx, cancel := context.WithTimeout(requestCtx, 2*time.Second)
		alive, err := s.workerCheck(checkCtx)
		cancel()
		if err != nil || !alive {
			dependencies["worker"] = "unavailable"
			unavailable = append(unavailable, "worker")
		}
	}
	if len(unavailable) > 0 {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "dependencies are unavailable",
			gin.H{"status": "not_ready", "dependencies": dependencies, "unavailable": unavailable})
		return
	}
	ok(c, 200, gin.H{
		"status":       "ready",
		"dependencies": dependencies,
		"runtime": gin.H{
			"mode": s.aiMode, "chat_model": s.chatModel,
			"vision_model": s.visionModel, "embedding_model": s.embeddingModel,
			"rag_collection": s.ragCollection,
		},
	})
}

// parseID 解析路径参数 :id 为 uint64；失败时已写出 400 响应，调用方应直接 return。
func parseID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, 400, "VALIDATION_FAILED", "invalid id", nil)
		return 0, false
	}
	return id, true
}

// lookupFailed 统一单条查询的错误处理：RecordNotFound → 404，其余数据库错误 → 500。
func lookupFailed(c *gin.Context, err error, message string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		notFound(c)
		return true
	}
	fail(c, 500, "INTERNAL_ERROR", message, nil)
	return true
}

// createStudyRecord 处理 POST /api/v1/study-records：字段校验后在同一事务内写入
// study_record、ai_task 与 outbox 事件（202 异步受理）；Idempotency-Key 与内容
// 指纹构成两层去重，force_create 可显式绕过指纹复用。
func (s *Server) createStudyRecord(c *gin.Context) {
	// 用 MaxBytesReader 封顶请求体，ReadAll 超限时返回 *http.MaxBytesError 以区分 413 与其他读错。
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxStudyRecordBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(c, http.StatusRequestEntityTooLarge, "VALIDATION_FAILED", "request body exceeds 1 MiB", nil)
			return
		}
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
	if json.Unmarshal(body, &input) != nil {
		fail(c, 422, "VALIDATION_FAILED", "request body must be valid JSON", nil)
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Summary = strings.TrimSpace(input.Summary)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > maxStudyTitleRunes {
		fail(c, 422, "VALIDATION_FAILED", "title is required and must not exceed 255 characters", nil)
		return
	}
	if len([]byte(input.Summary)) > maxStudySummaryBytes {
		fail(c, 422, "VALIDATION_FAILED", "summary must not exceed 32 KiB", nil)
		return
	}
	if input.DurationMinutes <= 0 || input.DurationMinutes > maxDurationMinutes {
		fail(c, 422, "VALIDATION_FAILED", "duration_minutes must be between 1 and 1440", nil)
		return
	}
	if len(input.Modules) == 0 || len(input.Modules) > maxModuleCount {
		fail(c, 422, "VALIDATION_FAILED", "modules must contain between 1 and 50 items", nil)
		return
	}
	for i := range input.Modules {
		input.Modules[i].Category = strings.TrimSpace(input.Modules[i].Category)
		input.Modules[i].Content = strings.TrimSpace(input.Modules[i].Content)
		if input.Modules[i].Category == "" || utf8.RuneCountInString(input.Modules[i].Category) > maxModuleCategory {
			fail(c, 422, "VALIDATION_FAILED", "module category is required and must not exceed 64 characters", gin.H{"module_index": i})
			return
		}
		if input.Modules[i].Content == "" || len([]byte(input.Modules[i].Content)) > maxModuleContent {
			fail(c, 422, "VALIDATION_FAILED", "module content is required and must not exceed 32 KiB", gin.H{"module_index": i})
			return
		}
	}
	key := c.GetHeader("Idempotency-Key")
	if len(key) > 128 {
		fail(c, 422, "VALIDATION_FAILED", "Idempotency-Key exceeds 128 bytes", nil)
		return
	}
	// 幂等作用域为“路由 + key”：不同接口即使复用同一 key 也不会互相污染记录。
	scope := "POST:/api/v1/study-records:" + key
	// 幂等键约束“同一个请求重放返回原响应”；request hash 不同则明确冲突，
	// 避免调用方误复用 key 时静默接受另一份内容。
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
	}{input.Title, input.Summary, input.DurationMinutes, modules})
	fingerprint := store.HashRequest(canonical)
	var encoded []byte
	var statusCode = http.StatusAccepted
	var conflict bool
	err = s.store.DB.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if key != "" {
			// INSERT IGNORE 借助唯一键完成首次抢占：影响 1 行表示本请求持有该 key；
			// 影响 0 行说明 key 已存在，随后读回已存记录决定是重放还是 409。
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
				// key 已存在且请求已完成：直接重放缓存的首次响应，不再执行任何业务写入。
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
			// 内容指纹去重与传输层幂等不同：即使没有 key，10 分钟内相同规范化内容
			// 也复用记录；调用方可用 force_create 明确保留重复学习事件。
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
				Title: input.Title, Summary: input.Summary,
				DurationMinutes: input.DurationMinutes, ContentFingerprint: fingerprint, Modules: modules,
			})
			if createErr != nil {
				return createErr
			}
		}
		payload := gin.H{
			"data": gin.H{
				"study_record": record,
				"task_id":      task.ID,
				"deduplicated": deduplicated,
			},
			"request_id": c.GetString("request_id"),
		}
		encoded, _ = json.Marshal(payload)
		if key != "" {
			// 业务写入与幂等响应缓存同事务提交，不会出现“记住了响应但记录/任务不存在”。
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
	// 新建与重放共用事务内序列化的同一响应体，保证相同 key 的响应字节级一致。
	c.Data(statusCode, "application/json", encoded)
}

// getStudyRecord 处理 GET /api/v1/study-records/:id：预加载 modules 后返回单条记录。
func (s *Server) getStudyRecord(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var record domain.StudyRecord
	if lookupFailed(c, s.store.DB.Preload("Modules").First(&record, id).Error, "could not load study record") {
		return
	}
	ok(c, 200, record)
}

// listStudyRecords 处理 GET /api/v1/study-records：倒序取最近 100 条，
// 并反查每条记录最新的 study_report 任务以附带任务状态。
func (s *Server) listStudyRecords(c *gin.Context) {
	var records []domain.StudyRecord
	if err := s.store.DB.Preload("Modules").Order("id DESC").Limit(100).Find(&records).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not list study records", nil)
		return
	}
	type item struct {
		domain.StudyRecord
		TaskID     uint64            `json:"task_id"`
		TaskStatus domain.TaskStatus `json:"task_status"`
	}
	recordIDs := make([]uint64, 0, len(records))
	for _, record := range records {
		recordIDs = append(recordIDs, record.ID)
	}
	var tasks []domain.AITask
	if len(recordIDs) > 0 {
		if err := s.store.DB.Where(`kind='study_report' AND CAST(JSON_UNQUOTE(JSON_EXTRACT(payload_json,'$.study_record_id')) AS UNSIGNED) IN ?`, recordIDs).
			Order("id").Find(&tasks).Error; err != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not list study record tasks", nil)
			return
		}
	}
	// 任务按 id 升序遍历，map 覆盖后保留最新一次报告任务。
	latest := make(map[uint64]domain.AITask, len(tasks))
	for _, task := range tasks {
		var payload struct {
			StudyRecordID uint64 `json:"study_record_id"`
		}
		if json.Unmarshal([]byte(task.PayloadJSON), &payload) == nil {
			latest[payload.StudyRecordID] = task
		}
	}
	result := make([]item, 0, len(records))
	for _, record := range records {
		task, exists := latest[record.ID]
		if !exists {
			result = append(result, item{StudyRecord: record, TaskID: 0, TaskStatus: "pending"})
			continue
		}
		result = append(result, item{StudyRecord: record, TaskID: task.ID, TaskStatus: task.Status})
	}
	ok(c, 200, result)
}

// getTask 处理 GET /api/v1/tasks/:id：返回 ai_task 原始行（含状态与 lease 字段）。
func (s *Server) getTask(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var task domain.AITask
	if lookupFailed(c, s.store.DB.First(&task, id).Error, "could not load task") {
		return
	}
	ok(c, 200, task)
}

// taskDetail 处理 GET /api/v1/tasks/:id/detail：按 payload 反查关联的记录/文档/图片，
// 并聚合报告、复习任务与执行 trace，供前端详情页一次渲染。
func (s *Server) taskDetail(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var task domain.AITask
	if lookupFailed(c, s.store.DB.First(&task, id).Error, "could not load task detail") {
		return
	}
	var payload struct {
		StudyRecordID uint64 `json:"study_record_id"`
		DocumentID    uint64 `json:"document_id"`
		ImageID       uint64 `json:"image_id"`
	}
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
		fail(c, 500, "INTERNAL_ERROR", "task payload is invalid", nil)
		return
	}
	var record *domain.StudyRecord
	if payload.StudyRecordID != 0 {
		var value domain.StudyRecord
		err := s.store.DB.Preload("Modules").First(&value, payload.StudyRecordID).Error
		if err == nil {
			record = &value
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 500, "INTERNAL_ERROR", "could not load task study record", nil)
			return
		}
	}
	var document *domain.Document
	if payload.DocumentID != 0 {
		var value domain.Document
		err := s.store.DB.First(&value, payload.DocumentID).Error
		if err == nil {
			document = &value
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 500, "INTERNAL_ERROR", "could not load task document", nil)
			return
		}
	}
	var image *domain.Image
	if payload.ImageID != 0 {
		var value domain.Image
		err := s.store.DB.First(&value, payload.ImageID).Error
		if err == nil {
			image = &value
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 500, "INTERNAL_ERROR", "could not load task image", nil)
			return
		}
	}
	var report *domain.Report
	var review *domain.ReviewTask
	var reportValue domain.Report
	reportErr := s.store.DB.Where("task_id=?", task.ID).First(&reportValue).Error
	if reportErr == nil {
		report = &reportValue
		var reviewValue domain.ReviewTask
		reviewErr := s.store.DB.Where("report_id=?", reportValue.ID).First(&reviewValue).Error
		if reviewErr == nil {
			review = &reviewValue
		} else if !errors.Is(reviewErr, gorm.ErrRecordNotFound) {
			fail(c, 500, "INTERNAL_ERROR", "could not load task review", nil)
			return
		}
	} else if !errors.Is(reportErr, gorm.ErrRecordNotFound) {
		fail(c, 500, "INTERNAL_ERROR", "could not load task report", nil)
		return
	}
	traceData, err := s.loadTrace(task.ID)
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load task trace", nil)
		return
	}
	ok(c, 200, gin.H{
		"task": task, "study_record": record, "report": report, "review_task": review,
		"trace": traceData, "document_id": payload.DocumentID, "document": document,
		"image": func() any {
			if image == nil {
				return nil
			}
			return imageView(*image)
		}(),
	})
}

// trace 处理 GET /api/v1/tasks/:id/trace：仅返回该任务的 attempts 与 agent 执行链。
func (s *Server) trace(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var task domain.AITask
	if lookupFailed(c, s.store.DB.First(&task, id).Error, "could not load task") {
		return
	}
	traceData, err := s.loadTrace(id)
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load task trace", nil)
		return
	}
	ok(c, 200, traceData)
}

// loadTrace 加载任务的全部执行历史：attempts、agent_runs 及各自的 steps/tool_calls。
func (s *Server) loadTrace(id uint64) (gin.H, error) {
	var attempts []domain.TaskAttempt
	if err := s.store.DB.Where("task_id=?", id).Order("execution_generation,attempt_no").Find(&attempts).Error; err != nil {
		return nil, err
	}
	var runs []map[string]any
	if err := s.store.DB.Table("agent_runs").Where("task_id=?", id).Order("id").Find(&runs).Error; err != nil {
		return nil, err
	}
	runIDs := make([]uint64, 0, len(runs))
	for _, run := range runs {
		// GORM 把 BIGINT 扫入 map 时可能是 int64 或 uint64，两种解码都要收拢。
		switch value := run["id"].(type) {
		case uint64:
			runIDs = append(runIDs, value)
		case int64:
			runIDs = append(runIDs, uint64(value))
		}
	}
	var steps, tools []map[string]any
	if len(runIDs) > 0 {
		if err := s.store.DB.Table("agent_steps").Where("agent_run_id IN ?", runIDs).Order("id").Find(&steps).Error; err != nil {
			return nil, err
		}
		if err := s.store.DB.Table("tool_calls").Where("agent_run_id IN ?", runIDs).Order("id").Find(&tools).Error; err != nil {
			return nil, err
		}
	}
	return gin.H{"attempts": attempts, "agent_runs": runs, "agent_steps": steps, "tool_calls": tools}, nil
}

// retryTask 处理 POST /api/v1/tasks/:id/retry：仅允许 dead 任务，事务内 generation+1、
// attempt_no 清零并写入 outbox 事件重新入队；document_index 重试要求文档处于 failed，事务内一并重置回 uploaded。
func (s *Server) retryTask(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var task domain.AITask
	if lookupFailed(c, s.store.DB.First(&task, id).Error, "could not load task") {
		return
	}
	if task.Status != domain.TaskDead {
		fail(c, 409, "TASK_NOT_RETRYABLE", "only dead tasks may be retried", nil)
		return
	}
	var documentID uint64
	if task.Kind == "document_index" {
		var payload struct {
			DocumentID uint64 `json:"document_id"`
		}
		if json.Unmarshal([]byte(task.PayloadJSON), &payload) != nil || payload.DocumentID == 0 {
			fail(c, 500, "INTERNAL_ERROR", "document task payload is invalid", nil)
			return
		}
		var document domain.Document
		if lookupFailed(c, s.store.DB.First(&document, payload.DocumentID).Error, "could not load task document") {
			return
		}
		if document.Status != "failed" {
			fail(c, 409, "DOCUMENT_NOT_RETRYABLE", "only failed documents may be retried", gin.H{"status": document.Status})
			return
		}
		documentID = document.ID
	}
	now := time.Now().UTC()
	var updated bool
	err := s.store.DB.Transaction(func(tx *gorm.DB) error {
		// generation 递增使上一世代的迟到 Worker 失去写入资格；attempt_no 清零后重新计算退避。
		result := tx.Exec(`UPDATE ai_tasks SET status='pending',attempt_no=0,execution_generation=execution_generation+1,
			available_at=?,last_error='',updated_at=? WHERE id=? AND status='dead'`, now, now, id)
		if result.Error != nil {
			return result.Error
		}
		// 影响 0 行说明任务已被并发操作移出 dead 状态：按普通冲突返回 409，而非服务端错误。
		if result.RowsAffected != 1 {
			return nil
		}
		if documentID != 0 {
			result = tx.Model(&domain.Document{}).Where("id=? AND status='failed'", documentID).
				Updates(map[string]any{"status": "uploaded", "error_message": "", "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("document state changed before retry")
			}
		}
		payload, _ := json.Marshal(gin.H{"task_id": id})
		// 人工重试仍走 Outbox，不直接写 Redis，保持所有入队入口的一致可靠性语义。
		if err := tx.Create(&domain.OutboxEvent{AggregateID: id, EventType: "ai_task.manual_retry", PayloadJSON: string(payload), CreatedAt: now}).Error; err != nil {
			return err
		}
		updated = true
		return nil
	})
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "retry failed", nil)
		return
	}
	if !updated {
		fail(c, 409, "TASK_NOT_RETRYABLE", "only dead tasks may be retried", nil)
		return
	}
	ok(c, 202, gin.H{"task_id": id, "status": "pending"})
}

// getReport 处理 GET /api/v1/reports/:id：返回报告 JSON 记录。
func (s *Server) getReport(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var v domain.Report
	if lookupFailed(c, s.store.DB.First(&v, id).Error, "could not load report") {
		return
	}
	ok(c, 200, v)
}

// reportMarkdown 处理 GET /api/v1/reports/:id/report.md：以 text/markdown 直接输出报告原文。
func (s *Server) reportMarkdown(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var v domain.Report
	if lookupFailed(c, s.store.DB.First(&v, id).Error, "could not load report") {
		return
	}
	c.Data(200, "text/markdown; charset=utf-8", []byte(v.MarkdownContent))
}

// notFound 输出统一的 404 响应，供各查询路径复用。
func notFound(c *gin.Context) { fail(c, 404, "NOT_FOUND", "resource not found", nil) }
