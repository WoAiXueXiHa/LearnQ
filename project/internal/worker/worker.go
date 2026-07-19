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

	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/report"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/skill"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/trace"
)

type Pool struct {
	// concurrency 控制本进程并行度；timeout 限制业务执行，lease 略长于 timeout，
	// 给成功/失败结果留出持久化窗口。
	store       *store.Store
	queue       *queue.Redis
	model       model.ChatModel
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

func New(s *store.Store, q *queue.Redis, m model.ChatModel, reportDir string, logger *slog.Logger) *Pool {
	return &Pool{store: s, queue: q, model: m, concurrency: 4, timeout: 45 * time.Second, lease: 60 * time.Second, reportDir: reportDir, log: logger}
}

func (p *Pool) WithIndexer(indexer interface {
	Process(context.Context, domain.AITask) (uint64, error)
}) *Pool {
	p.indexer = indexer
	return p
}

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
			p.failAndAck(task, token, fmt.Errorf("worker panic: %v", r), task.Kind == "document_index")
		}
	}()
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()
	if task.Kind == "document_index" {
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
			p.failAndAck(task, token, err, true)
			return
		}
		p.ack(id, token)
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
			if _, traceErr := p.trace.Record(task.ID, definition, hash, string(input), response, time.Since(started), executions, runErr); traceErr != nil {
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
		p.failAndAck(task, token, err, false)
		return
	}
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
	// 持久化使用独立短上下文：业务 timeout 或进程取消后，仍有机会记录最终状态并释放租约。
	saved, err := p.store.Complete(persistCtx, task, token, markdown)
	persistCancel()
	if err != nil {
		p.failAndAck(task, token, err, false)
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

func (p *Pool) failAndAck(task domain.AITask, token string, cause error, document bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	terminal := !retryable(cause)
	var failErr error
	if document {
		_, _, failErr = p.store.FailDocument(ctx, task, token, cause, terminal)
	} else if terminal {
		_, _, failErr = p.store.FailTerminal(ctx, task, token, cause)
	} else {
		_, _, failErr = p.store.Fail(ctx, task, token, cause)
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

func (p *Pool) ack(id uint64, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.queue.Ack(ctx, id, token); err != nil {
		p.log.Error("queue ack failed", "task_id", id, "error", err)
	}
}

func retryable(cause error) bool {
	var dependency *model.DependencyError
	return !errors.As(cause, &dependency) || dependency.Retryable
}

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

func randomToken() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(value[:])
}
