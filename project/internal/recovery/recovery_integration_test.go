//go:build integration

package recovery_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/bootstrap"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/migrate"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/recovery"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
	"github.com/go-redis/redis/v8"
)

func TestReaperAndReconcilerRecoverDerivedState(t *testing.T) {
	cfg := config.Load()
	cfg.MySQLDSN = os.Getenv("LEARNQ_TEST_MYSQL_DSN")
	if cfg.MySQLDSN == "" {
		t.Skip("LEARNQ_TEST_MYSQL_DSN is required")
	}
	addr := os.Getenv("LEARNQ_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("LEARNQ_TEST_REDIS_ADDR is required")
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
	q := queue.New(rdb)
	s := store.New(db)
	t.Cleanup(func() {
		rdb.FlushDB(context.Background())
		for _, table := range []string{"task_attempts", "outbox_events", "ai_tasks", "study_modules", "study_records"} {
			db.Exec("DELETE FROM " + table)
		}
	})
	_, task, err := s.CreateStudyRecord(context.Background(), store.CreateRecord{
		Title: "recover", DurationMinutes: 1,
		Modules: []domain.StudyModule{{Category: "backend", Content: "lease"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchOutbox(context.Background(), q.Enqueue, 10); err != nil {
		t.Fatal(err)
	}
	id, ok, err := q.Claim(context.Background(), time.Millisecond, "expired-token")
	if err != nil || !ok || id != task.ID {
		t.Fatalf("claim id=%d ok=%v err=%v", id, ok, err)
	}
	acquired, ok, err := s.Acquire(context.Background(), id, "expired-token", time.Now().Add(-time.Second))
	if err != nil || !ok {
		t.Fatalf("acquire=%v err=%v", ok, err)
	}
	_ = acquired
	time.Sleep(2 * time.Millisecond)
	// MySQL remains authoritative even if Redis loses all processing state.
	rdb.Del(context.Background(), queue.ProcessingKey, queue.LeaseTokenKey)
	if err := recovery.Reap(context.Background(), s, q); err != nil {
		t.Fatal(err)
	}
	var current domain.AITask
	db.First(&current, task.ID)
	if current.Status != domain.TaskRetryWait {
		t.Fatalf("status=%s", current.Status)
	}
	if err := s.DispatchOutbox(context.Background(), q.Enqueue, 10); err != nil {
		t.Fatal(err)
	}
	rdb.Del(context.Background(), queue.ReadyKey)
	if err := recovery.Reconcile(context.Background(), s, q); err != nil {
		t.Fatal(err)
	}
	if score := rdb.ZScore(context.Background(), queue.ReadyKey, strconv.FormatUint(task.ID, 10)).Val(); score == 0 {
		t.Fatal("queued MySQL task was not restored to Redis")
	}
	db.Model(&domain.AITask{}).Where("id=?", task.ID).Update("status", domain.TaskSucceeded)
	rdb.ZAdd(context.Background(), queue.ProcessingKey, &redis.Z{Score: float64(time.Now().UnixMilli()), Member: strconv.FormatUint(task.ID, 10)})
	rdb.HSet(context.Background(), queue.LeaseTokenKey, strconv.FormatUint(task.ID, 10), "ghost-token")
	if err := recovery.Reconcile(context.Background(), s, q); err != nil {
		t.Fatal(err)
	}
	if rdb.ZScore(context.Background(), queue.ProcessingKey, strconv.FormatUint(task.ID, 10)).Err() != redis.Nil {
		t.Fatal("terminal processing ghost was not removed")
	}
}

func TestReaperResetsExpiredDocumentForRetry(t *testing.T) {
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
	q := queue.New(rdb)
	s := store.New(db)
	t.Cleanup(func() {
		rdb.FlushDB(context.Background())
		for _, table := range []string{"document_chunks", "documents", "task_attempts", "outbox_events", "ai_tasks"} {
			db.Exec("DELETE FROM " + table)
		}
	})
	document, task, err := s.CreateDocument(context.Background(), domain.Document{
		Filename: "recover.md", MediaType: "md", ContentHash: "recover", Content: "# recover",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchOutbox(context.Background(), q.Enqueue, 10); err != nil {
		t.Fatal(err)
	}
	id, ok, err := q.Claim(context.Background(), time.Millisecond, "expired-document-token")
	if err != nil || !ok || id != task.ID {
		t.Fatalf("claim id=%d ok=%v err=%v", id, ok, err)
	}
	if _, ok, err := s.Acquire(context.Background(), id, "expired-document-token", time.Now().Add(-time.Second)); err != nil || !ok {
		t.Fatalf("acquire=%v err=%v", ok, err)
	}
	if err := db.Model(&domain.Document{}).Where("id=?", document.ID).Update("status", "embedding").Error; err != nil {
		t.Fatal(err)
	}
	rdb.Del(context.Background(), queue.ProcessingKey, queue.LeaseTokenKey)
	if err := recovery.Reap(context.Background(), s, q); err != nil {
		t.Fatal(err)
	}
	var currentTask domain.AITask
	var currentDocument domain.Document
	if err := db.First(&currentTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&currentDocument, document.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentTask.Status != domain.TaskRetryWait || currentDocument.Status != "uploaded" || currentDocument.ErrorMessage != "" {
		t.Fatalf("task=%s document=%s error=%q", currentTask.Status, currentDocument.Status, currentDocument.ErrorMessage)
	}
}
