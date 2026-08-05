package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/internal/imagestore"
	"github.com/WoAiXueXiHa/LeranQ/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/internal/report"
	"github.com/WoAiXueXiHa/LeranQ/internal/skill"
	"github.com/WoAiXueXiHa/LeranQ/internal/store"
	"github.com/WoAiXueXiHa/LeranQ/internal/trace"
)

type Pool struct {
	// concurrency 控制本进程并行度；timeout 限制业务执行，lease 略长于 timeout，
	// 给成功/失败结果留出持久化窗口。
	store       *store.Store
	queue       *queue.Redis
	model       model.ChatModel
	vision      model.VisionModel
	imageStore  *imagestore.Store
	concurrency int
	timeout     time.Duration
	lease       time.Duration
	reportDir   string
	log         *slog.Logger
	wg          sync.WaitGroup
	indexer     interface {
		Process(context.Context, domain.AITask) (uint64, error)
	}
	skills *skill.Registry
	trace  trace.Recorder
}

// New 以默认参数（4 并发、45s 超时、60s 租约）构造 Worker Pool。
// 可选依赖（索引器、视觉模型、Skill）通过 With 系列方法在调用 Run 前注入。
func New(s *store.Store, q *queue.Redis, m model.ChatModel, reportDir string, logger *slog.Logger) *Pool {
	return &Pool{store: s, queue: q, model: m, concurrency: 4, timeout: 45 * time.Second, lease: 60 * time.Second, reportDir: reportDir, log: logger}
}

// WithIndexer 注入文档索引器。以接口而非具体类型声明，使 worker 不依赖 Qdrant
// 细节，测试中可替换为内存实现。
func (p *Pool) WithIndexer(indexer interface {
	Process(context.Context, domain.AITask) (uint64, error)
}) *Pool {
	p.indexer = indexer
	return p
}

// WithVision 注入视觉模型与图片存储，启用 image_describe 任务的处理能力；
// 未注入时此类任务会以永久错误落为 dead。
func (p *Pool) WithVision(vision model.VisionModel, images *imagestore.Store) *Pool {
	p.vision = vision
	p.imageStore = images
	return p
}

// WithTiming 覆盖执行超时与租约时长。timeout 必须大于 0，lease 必须大于 timeout，
// 否则维持旧值——租约必须保证一次执行加结果持久化落在窗口内。
func (p *Pool) WithTiming(timeout, lease time.Duration) *Pool {
	if timeout > 0 {
		p.timeout = timeout
	}
	if lease > timeout {
		p.lease = lease
	}
	return p
}

// WithSkills 注入 Skill 注册表并复用同一 DB 连接创建 trace 记录器，
// 此后报告执行会附带完整 trace（prompt、model、tool 调用）。
func (p *Pool) WithSkills(registry *skill.Registry) *Pool {
	p.skills = registry
	p.trace = trace.Recorder{DB: p.store.DB}
	return p
}

func (p *Pool) Run(ctx context.Context) {
	// 固定大小协程池提供天然背压，不为每个队列项无限创建 goroutine。
	for i := 0; i < p.concurrency; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.loop(ctx)
		}()
	}
}

func (p *Pool) Wait() { p.wg.Wait() }

// loop 是单个 Worker 的主循环：每 250ms 尝试领取一个任务并执行。
// 固定间隔轮询而非阻塞等待，换取实现简单与故障自愈，吞吐由协程数（concurrency）控制。
func (p *Pool) loop(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.claimAndRun(ctx)
		}
	}
}

// claimAndRun 完成一次「领取 → 确权 → 执行 → 提交」闭环：Redis Claim 抢占先机、
// MySQL Acquire 条件更新最终确权，成功路径 Complete + ack，失败路径 failAndAck。
// 中途退出必须守住租约语义：要么提交结果，要么由 Reaper 在租约超时后接管。
func (p *Pool) claimAndRun(parent context.Context) {
	// 每次领取生成独立 token。Redis 负责低成本抢占，MySQL Acquire 负责最终确权，
	// 二者结合可容忍重复投递，同时保证只有一个执行者能提交结果。
	token := randomToken()
	id, ok, err := p.queue.Claim(parent, p.lease, token)
	if err != nil {
		p.log.Error("queue claim failed", "error", err)
		return
	}
	if !ok {
		return
	}
	task, acquired, err := p.store.Acquire(parent, id, token, time.Now().UTC().Add(p.lease))
	if err != nil || !acquired {
		_ = p.queue.Ack(parent, id, token)
		return
	}
	defer func() {
		// panic 被转换为普通失败，既保住 Worker 进程，也让任务进入统一重试/死信流程。
		if r := recover(); r != nil {
			p.log.Error("worker panic recovered", "task_id", id, "panic", r, "stack", string(debug.Stack()))
			p.failAndAck(task, token, fmt.Errorf("worker panic: %v", r))
		}
	}()
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()
	switch task.Kind {
	case "document_index":
		// 索引任务与报告任务共用可靠性外壳，但业务完成动作不同：
		// 索引成功必须同时把 Document 推进到 ready。
		var documentID uint64
		if p.indexer == nil {
			err = errors.New("document indexer is not configured")
		} else {
			documentID, err = p.indexer.Process(ctx, task)
		}
		if err == nil {
			persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = p.store.CompleteDocumentIndex(persistCtx, task, token, documentID)
			persistCancel()
		}
		if err != nil {
			p.failAndAck(task, token, err)
			return
		}
		p.ack(id, token)
		return
	case "image_describe":
		p.processImage(ctx, task, token)
		return
	case "study_report":
		// Continue with the report workflow below.
	default:
		p.failAndAck(task, token, model.Permanent(fmt.Errorf("unsupported task kind %q", task.Kind)))
		return
	}
	var response model.ChatResponse
	if p.skills != nil {
		// Skill 模式记录 prompt/schema/model/tool 调用，保证报告结果可追溯；
		// 未配置 Registry 时保留窄模型调用，便于最小化测试。
		input, inputErr := p.workflowInput(ctx, task)
		if inputErr != nil {
			err = inputErr
		} else {
			definition, _ := p.skills.Get("daily-review")
			hash, _ := p.skills.PromptHash("daily-review")
			started := time.Now()
			var executions []skill.ToolExecution
			var runErr error
			response, executions, runErr = p.skills.RunDetailed(ctx, "daily-review", input)
			err = runErr
			if _, traceErr := p.trace.Record(context.Background(), task.ID, definition, hash, string(input), response, time.Since(started), executions, runErr); traceErr != nil {
				p.log.Error("trace persist failed", "task_id", task.ID, "error", traceErr)
			}
		}
	} else {
		response, err = p.model.Generate(ctx, model.ChatRequest{Prompt: task.PayloadJSON, Skill: "daily-review", Input: []byte(task.PayloadJSON)})
	}
	var markdown string
	if err == nil {
		markdown, err = model.MarkdownFromJSON(response.Content)
	}
	if err != nil {
		p.failAndAck(task, token, err)
		return
	}
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
	// 持久化使用独立短上下文：业务 timeout 或进程取消后，仍有机会记录最终状态并释放租约。
	saved, err := p.store.Complete(persistCtx, task, token, markdown)
	persistCancel()
	if err != nil {
		p.failAndAck(task, token, err)
		return
	}
	if exportErr := report.Export(p.reportDir, saved.ID, markdown); exportErr != nil {
		// Markdown 文件是可替换导出物，数据库 Report 才是真相源；导出失败不回滚已完成任务。
		p.log.Error("report export failed", "report_id", saved.ID, "error", exportErr)
		p.store.DB.Model(&saved).Updates(map[string]any{"export_status": "failed", "export_error": exportErr.Error()})
	} else {
		p.store.DB.Model(&saved).Update("export_status", "succeeded")
	}
	p.ack(id, token)
}

// processImage 处理 image_describe 任务：读取图片行与文件内容 → 视觉模型描述 →
// 持久化描述 JSON。文件读取失败属永久错误（重试也读不到同一文件）；模型调用失败
// 按 retryable 分类决定进入 retry_wait 还是 dead。
func (p *Pool) processImage(ctx context.Context, task domain.AITask, token string) {
	if p.vision == nil || p.imageStore == nil {
		p.failAndAck(task, token, model.Permanent(errors.New("image description dependencies are not configured")))
		return
	}
	imageRow, err := p.store.BeginImageDescription(ctx, task)
	if err != nil {
		p.failAndAck(task, token, model.Permanent(err))
		return
	}
	body, err := p.imageStore.Read(imageRow.StoragePath)
	if err != nil {
		p.failAndAck(task, token, model.Permanent(fmt.Errorf("read image: %w", err)))
		return
	}
	response, err := p.vision.Describe(ctx, model.VisionRequest{
		Image: body, MediaType: imageRow.MediaType, Prompt: imageRow.Prompt,
	})
	if err != nil {
		p.failAndAck(task, token, err)
		return
	}
	description, err := json.Marshal(response.Description)
	if err != nil {
		p.failAndAck(task, token, model.Permanent(fmt.Errorf("encode image description: %w", err)))
		return
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = p.store.CompleteImageDescription(persistCtx, task, token, string(description), response.Model)
	cancel()
	if err != nil {
		p.failAndAck(task, token, err)
		return
	}
	p.ack(task.ID, token)
}

// failAndAck 把任务落为失败状态并 ACK 队列项。文档/图片任务走专属 Fail 方法，
// 以同步关联实体状态；普通任务按 terminal 选择 FailTerminal（直接 dead）或 Fail
// （再按剩余尝试次数决定 retry_wait 或 dead）。
// 持久化失败时不 ACK，让租约过期后的 Reaper 重新接管，避免任务凭空消失。
func (p *Pool) failAndAck(task domain.AITask, token string, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	terminal := !retryable(cause)
	var failErr error
	switch task.Kind {
	case "document_index":
		_, _, failErr = p.store.FailDocument(ctx, task, token, cause, terminal)
	case "image_describe":
		_, _, failErr = p.store.FailImage(ctx, task, token, cause, terminal)
	default:
		if terminal {
			_, _, failErr = p.store.FailTerminal(ctx, task, token, cause)
		} else {
			_, _, failErr = p.store.Fail(ctx, task, token, cause)
		}
	}
	if failErr != nil {
		// 状态未持久化时不能 ACK，否则会丢失仍需恢复的任务；交给租约超时后的 Reaper 处理。
		p.log.Error("task failure could not be persisted", "task_id", task.ID,
			"execution_generation", task.ExecutionGeneration, "error", cause, "persist_error", failErr)
		return
	}
	p.log.Error("task failed", "task_id", task.ID, "execution_generation", task.ExecutionGeneration, "error", cause)
	// 先提交 MySQL 的失败/重试状态，再清理 Redis processing 项。
	p.ack(task.ID, token)
}

// ack 从 Redis processing ZSet 中移除任务。MySQL 事务已先完成，ack 只是清理
// 队列残留；失败仅记日志，过期条目最终由 Reaper/Reconciler 兜底。
func (p *Pool) ack(id uint64, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.queue.Ack(ctx, id, token); err != nil {
		p.log.Error("queue ack failed", "task_id", id, "error", err)
	}
}

// retryable 按错误类型决定任务是否可重试：PermanentError 永不重试；
// DependencyError 仅在标记 Retryable 时重试；其余普通错误默认可重试。
// errors.As 沿包装链查找，内层包装的类别同样能被识别。
func retryable(cause error) bool {
	var permanent *model.PermanentError
	if errors.As(cause, &permanent) {
		return false
	}
	var dependency *model.DependencyError
	return !errors.As(cause, &dependency) || dependency.Retryable
}

// workflowInput 从任务 payload 取 study_record_id，预加载 Modules 后把完整记录
// 序列化为 Skill 输入。查询失败返回带上下文的错误，由调用方转入失败流程。
func (p *Pool) workflowInput(ctx context.Context, task domain.AITask) (json.RawMessage, error) {
	var payload struct {
		StudyRecordID uint64 `json:"study_record_id"`
	}
	_ = json.Unmarshal([]byte(task.PayloadJSON), &payload)
	var record domain.StudyRecord
	if err := p.store.DB.WithContext(ctx).Preload("Modules").First(&record, payload.StudyRecordID).Error; err != nil {
		return nil, fmt.Errorf("study record %d: %w", payload.StudyRecordID, err)
	}
	input, _ := json.Marshal(record)
	return input, nil
}

// randomToken 生成 128 位随机 token 作为租约所有权凭证（fencing token）。
// crypto/rand 不可用时退化为纳秒时间戳，保证熵源故障不致阻断领取流程。
func randomToken() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(value[:])
}
