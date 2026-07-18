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
		if r := recover(); r != nil {
			p.log.Error("worker panic recovered", "task_id", id, "panic", r, "stack", string(debug.Stack()))
			p.failAndAck(task, token, fmt.Errorf("worker panic: %v", r), task.Kind == "document_index")
		}
	}()
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()
	if task.Kind == "document_index" {
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
	saved, err := p.store.Complete(persistCtx, task, token, markdown)
	persistCancel()
	if err != nil {
		p.failAndAck(task, token, err, false)
		return
	}
	if exportErr := report.Export(p.reportDir, saved.ID, markdown); exportErr != nil {
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
		p.log.Error("task failure could not be persisted", "task_id", task.ID,
			"execution_generation", task.ExecutionGeneration, "error", cause, "persist_error", failErr)
		return
	}
	p.log.Error("task failed", "task_id", task.ID, "execution_generation", task.ExecutionGeneration, "error", cause)
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
