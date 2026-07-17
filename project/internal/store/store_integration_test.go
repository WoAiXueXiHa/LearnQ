//go:build integration

package store_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/bootstrap"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/migrate"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
)

func database(t *testing.T) *store.Store {
	t.Helper()
	cfg := config.Load()
	if dsn := os.Getenv("LEARNQ_TEST_MYSQL_DSN"); dsn != "" {
		cfg.MySQLDSN = dsn
	}
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"review_events", "review_tasks", "reports", "task_attempts", "outbox_events", "ai_tasks", "study_modules", "study_records", "idempotency_keys"} {
			db.Exec("DELETE FROM " + table)
		}
	})
	return store.New(db)
}

func TestCreateStudyRecordCommitsAllFourAggregates(t *testing.T) {
	s := database(t)
	record, task, err := s.CreateStudyRecord(context.Background(), store.CreateRecord{
		Title: "事务", Summary: "同事务", DurationMinutes: 30,
		Modules: []domain.StudyModule{{Category: "backend", Content: "outbox"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.ID == 0 || task.ID == 0 || task.Status != domain.TaskPending {
		t.Fatalf("record=%#v task=%#v", record, task)
	}
	var modules, events int64
	s.DB.Model(&domain.StudyModule{}).Where("study_record_id=?", record.ID).Count(&modules)
	s.DB.Model(&domain.OutboxEvent{}).Where("aggregate_id=?", task.ID).Count(&events)
	if modules != 1 || events != 1 {
		t.Fatalf("modules=%d events=%d", modules, events)
	}
}

func TestCreateStudyRecordRollsBackOnModuleFailure(t *testing.T) {
	s := database(t)
	_, _, err := s.CreateStudyRecord(context.Background(), store.CreateRecord{
		Title: "rollback", DurationMinutes: 1,
		Modules: []domain.StudyModule{{Category: string(make([]byte, 80)), Content: "invalid"}},
	})
	if err == nil {
		t.Fatal("expected strict varchar failure")
	}
	var records, tasks, events int64
	s.DB.Model(&domain.StudyRecord{}).Count(&records)
	s.DB.Model(&domain.AITask{}).Count(&tasks)
	s.DB.Model(&domain.OutboxEvent{}).Count(&events)
	if records+tasks+events != 0 {
		t.Fatalf("partial transaction: records=%d tasks=%d events=%d", records, tasks, events)
	}
}

func TestOutboxReplayAndConcurrentAcquireHaveOneWinner(t *testing.T) {
	s := database(t)
	_, task, err := s.CreateStudyRecord(context.Background(), store.CreateRecord{
		Title: "replay", DurationMinutes: 1,
		Modules: []domain.StudyModule{{Category: "backend", Content: "lease"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var enqueued int
	injected := errors.New("crash after derived write")
	err = s.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error {
		enqueued++
		return injected
	}, 10)
	if !errors.Is(err, injected) {
		t.Fatalf("got %v", err)
	}
	var pending domain.OutboxEvent
	s.DB.Where("aggregate_id=?", task.ID).First(&pending)
	if pending.PublishedAt != nil {
		t.Fatal("outbox marked published after simulated crash")
	}
	if err := s.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error {
		enqueued++
		return nil
	}, 10); err != nil {
		t.Fatal(err)
	}
	if enqueued != 2 {
		t.Fatalf("idempotent enqueue must be replayed, got %d", enqueued)
	}
	var winners int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, ok, acquireErr := s.Acquire(context.Background(), task.ID, "token-"+string(rune('a'+index)), time.Now().Add(time.Minute))
			if acquireErr != nil {
				t.Errorf("acquire: %v", acquireErr)
			}
			if ok {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
}

func TestLeaseTokenFencesStaleCompletion(t *testing.T) {
	s := database(t)
	_, task, _ := s.CreateStudyRecord(context.Background(), store.CreateRecord{
		Title: "fence", DurationMinutes: 1,
		Modules: []domain.StudyModule{{Category: "backend", Content: "token"}},
	})
	_ = s.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error { return nil }, 10)
	acquired, ok, err := s.Acquire(context.Background(), task.ID, "valid-token", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("acquire=%v,%v", ok, err)
	}
	if _, err := s.Complete(context.Background(), acquired, "stale-token", "# stale"); err == nil {
		t.Fatal("stale lease committed")
	}
	report, err := s.Complete(context.Background(), acquired, "valid-token", "# valid")
	if err != nil || report.MarkdownContent != "# valid" {
		t.Fatalf("valid completion: %#v %v", report, err)
	}
}

func TestThreeAttemptsUseRetryWaitThenDead(t *testing.T) {
	s := database(t)
	_, task, err := s.CreateStudyRecord(context.Background(), store.CreateRecord{
		Title: "retry", DurationMinutes: 1,
		Modules: []domain.StudyModule{{Category: "backend", Content: "backoff"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := s.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error { return nil }, 10); err != nil {
			t.Fatal(err)
		}
		acquired, ok, err := s.Acquire(context.Background(), task.ID, "retry-token-"+string(rune('0'+attempt)), time.Now().Add(time.Minute))
		if err != nil || !ok {
			t.Fatalf("attempt %d acquire=%v err=%v", attempt, ok, err)
		}
		available, retry, err := s.Fail(context.Background(), acquired, acquired.LeaseToken, errors.New("temporary"))
		if err != nil {
			t.Fatal(err)
		}
		var current domain.AITask
		s.DB.First(&current, task.ID)
		if attempt < 3 {
			if !retry || current.Status != domain.TaskRetryWait {
				t.Fatalf("attempt %d status=%s retry=%v", attempt, current.Status, retry)
			}
			want := time.Duration(1<<attempt) * time.Second
			if remaining := time.Until(available); remaining < want-time.Second || remaining > want+time.Second {
				t.Fatalf("attempt %d delay=%v want=%v", attempt, remaining, want)
			}
		} else if retry || current.Status != domain.TaskDead {
			t.Fatalf("final status=%s retry=%v", current.Status, retry)
		}
	}
}
