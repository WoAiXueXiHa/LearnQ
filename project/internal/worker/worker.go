package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
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
		Process(context.Context, domain.AITask) error
		HandleFailure(context.Context, domain.AITask, bool, error) error
	}
	skills *skill.Registry
	trace  trace.Recorder
}

func New(s *store.Store, q *queue.Redis, m model.ChatModel, reportDir string, logger *slog.Logger) *Pool {
	return &Pool{store: s, queue: q, model: m, concurrency: 4, timeout: 45 * time.Second, lease: 60 * time.Second, reportDir: reportDir, log: logger}
}

func (p *Pool) WithIndexer(indexer interface {
	Process(context.Context, domain.AITask) error
	HandleFailure(context.Context, domain.AITask, bool, error) error
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
	if err != nil || !ok {
		return
	}
	task, acquired, err := p.store.Acquire(parent, id, token, time.Now().UTC().Add(p.lease))
	if err != nil || !acquired {
		_ = p.queue.Ack(parent, id, token)
		return
	}
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()
	if task.Kind == "document_index" {
		if p.indexer == nil {
			err = errors.New("document indexer is not configured")
		} else {
			err = p.indexer.Process(ctx, task)
		}
		if err == nil {
			err = p.store.CompleteWithoutReport(context.WithoutCancel(parent), task, token)
		}
		if err != nil {
			failureContext := context.WithoutCancel(parent)
			_, retry, failErr := p.store.Fail(failureContext, task, token, err)
			var documentErr error
			if p.indexer != nil {
				documentErr = p.indexer.HandleFailure(failureContext, task, !retry, err)
			}
			p.log.Error("document indexing failed", "task_id", id, "execution_generation", task.ExecutionGeneration, "error", err, "persist_error", failErr, "document_error", documentErr)
		}
		_ = p.queue.Ack(context.WithoutCancel(parent), id, token)
		return
	}
	var response model.ChatResponse
	if p.skills != nil {
		modules, input := p.workflowInput(task)
		started := time.Now()
		result, workflowErr := p.skills.RunEinoWorkflow(ctx, modules, input)
		err = workflowErr
		for _, route := range result.Routes {
			output, exists := result.Outputs[route]
			if !exists {
				continue
			}
			definition, _ := p.skills.Get(route)
			hash, _ := p.skills.PromptHash(route)
			_, _ = p.trace.Record(task.ID, definition, hash, string(input), output, time.Since(started), result.Tools[route], nil)
		}
		response = result.Outputs["daily-review"]
	} else {
		response, err = p.model.Generate(ctx, model.ChatRequest{Prompt: task.PayloadJSON, Skill: "daily-review", Input: []byte(task.PayloadJSON)})
	}
	var markdown string
	if err == nil {
		markdown, err = model.MarkdownFromJSON(response.Content)
	}
	if err != nil {
		_, _, failErr := p.store.Fail(context.WithoutCancel(parent), task, token, err)
		p.log.Error("task failed", "task_id", id, "execution_generation", task.ExecutionGeneration, "error", err, "persist_error", failErr)
		_ = p.queue.Ack(context.WithoutCancel(parent), id, token)
		return
	}
	saved, err := p.store.Complete(context.WithoutCancel(parent), task, token, markdown)
	if err == nil {
		if exportErr := report.Export(p.reportDir, saved.ID, markdown); exportErr != nil {
			p.store.DB.Model(&saved).Updates(map[string]any{"export_status": "failed", "export_error": exportErr.Error()})
		} else {
			p.store.DB.Model(&saved).Update("export_status", "succeeded")
		}
	}
	_ = p.queue.Ack(context.WithoutCancel(parent), id, token)
}

func (p *Pool) workflowInput(task domain.AITask) ([]string, json.RawMessage) {
	var payload struct {
		StudyRecordID uint64 `json:"study_record_id"`
	}
	_ = json.Unmarshal([]byte(task.PayloadJSON), &payload)
	var record domain.StudyRecord
	p.store.DB.Preload("Modules").First(&record, payload.StudyRecordID)
	modules := make([]string, 0, len(record.Modules))
	for _, module := range record.Modules {
		modules = append(modules, module.Category)
	}
	input, _ := json.Marshal(record)
	return modules, input
}

func randomToken() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(value[:])
}
