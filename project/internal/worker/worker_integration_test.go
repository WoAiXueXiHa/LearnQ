//go:build integration

package worker_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/bootstrap"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/migrate"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/skill"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/worker"
	"github.com/go-redis/redis/v8"
)

func TestWorkerRunsEinoWorkflowWritesReportReviewAndTrace(t *testing.T) {
	cfg := config.Load()
	cfg.MySQLDSN = os.Getenv("LEARNQ_TEST_MYSQL_DSN")
	addr := os.Getenv("LEARNQ_TEST_REDIS_ADDR")
	if cfg.MySQLDSN == "" || addr == "" {
		t.Skip("integration dependencies are required")
	}
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(db); err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	rdb.FlushDB(context.Background())
	t.Cleanup(func() {
		rdb.FlushDB(context.Background())
		for _, table := range []string{"tool_calls", "agent_steps", "agent_runs", "review_events", "review_tasks", "reports", "task_attempts", "outbox_events", "ai_tasks", "study_modules", "study_records"} {
			db.Exec("DELETE FROM " + table)
		}
	})
	s := store.New(db)
	q := queue.New(rdb)
	_, task, err := s.CreateStudyRecord(context.Background(), store.CreateRecord{
		Title: "workflow", Summary: "trace", DurationMinutes: 30,
		Modules: []domain.StudyModule{{Category: "algorithm", Content: "binary search"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchOutbox(context.Background(), q.Enqueue, 10); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool := worker.New(s, q, model.Fake{}, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil))).WithSkills(skill.New(model.Fake{}))
	pool.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var current domain.AITask
		db.First(&current, task.ID)
		if current.Status == domain.TaskSucceeded {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	pool.Wait()
	var current domain.AITask
	db.First(&current, task.ID)
	var reports, reviews, runs, steps, tools int64
	db.Model(&domain.Report{}).Where("task_id=?", task.ID).Count(&reports)
	db.Model(&domain.ReviewTask{}).Count(&reviews)
	db.Table("agent_runs").Where("task_id=?", task.ID).Count(&runs)
	db.Table("agent_steps").Count(&steps)
	db.Table("tool_calls").Count(&tools)
	if current.Status != domain.TaskSucceeded || reports != 1 || reviews != 1 || runs != 4 || steps != 4 || tools == 0 {
		t.Fatalf("status=%s reports=%d reviews=%d runs=%d steps=%d tools=%d", current.Status, reports, reviews, runs, steps, tools)
	}
}
