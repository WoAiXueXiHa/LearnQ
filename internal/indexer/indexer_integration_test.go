//go:build integration

package indexer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/bootstrap"
	"github.com/WoAiXueXiHa/LearnQ/internal/config"
	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/indexer"
	"github.com/WoAiXueXiHa/LearnQ/internal/migrate"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
)

type memoryVectors struct {
	dimension int
	points    []rag.Point
	onUpsert  func()
}

func (m *memoryVectors) EnsureCollection(_ context.Context, dimension int) error {
	m.dimension = dimension
	return nil
}
func (m *memoryVectors) Upsert(_ context.Context, points []rag.Point) error {
	m.points = append(m.points, points...)
	if m.onUpsert != nil {
		m.onUpsert()
	}
	return nil
}
func (m *memoryVectors) DeleteDocument(_ context.Context, documentID uint64) error {
	kept := m.points[:0]
	for _, point := range m.points {
		if point.Payload["document_id"] != documentID {
			kept = append(kept, point)
		}
	}
	m.points = kept
	return nil
}

func TestDocumentTaskIndexesAsynchronously(t *testing.T) {
	cfg := config.Load()
	cfg.MySQLDSN = os.Getenv("LEARNQ_TEST_MYSQL_DSN")
	if cfg.MySQLDSN == "" {
		t.Skip("LEARNQ_TEST_MYSQL_DSN is required")
	}
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"document_chunks", "documents", "task_attempts", "outbox_events", "ai_tasks"} {
			db.Exec("DELETE FROM " + table)
		}
	})
	s := store.New(db)
	content := "# 标题\n" + strings.Repeat("学习 Go Redis MySQL。", 100)
	sum := sha256.Sum256([]byte(content))
	document, task, err := s.CreateDocument(context.Background(), domain.Document{Filename: "notes.md", MediaType: "md", ContentHash: hex.EncodeToString(sum[:]), Content: content})
	if err != nil {
		t.Fatal(err)
	}
	if document.Status != "uploaded" || task.Kind != "document_index" || task.Status != domain.TaskPending {
		t.Fatalf("document=%#v task=%#v", document, task)
	}
	if err := s.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error { return nil }, 10); err != nil {
		t.Fatal(err)
	}
	acquired, ok, err := s.Acquire(context.Background(), task.ID, "index-token", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("acquire=%v err=%v", ok, err)
	}
	vectors := &memoryVectors{}
	processor := &indexer.Indexer{Store: s, Embedding: model.Fake{Dimension: 64}, Vectors: vectors, Dimension: 64}
	documentID, err := processor.Process(context.Background(), acquired)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDocumentIndex(context.Background(), acquired, "index-token", documentID, "test-v1"); err != nil {
		t.Fatal(err)
	}
	var ready domain.Document
	db.First(&ready, document.ID)
	var count int64
	db.Model(&domain.DocumentChunk{}).Where("document_id=?", document.ID).Count(&count)
	if ready.Status != "ready" || count < 2 || len(vectors.points) != int(count) || vectors.dimension != 64 {
		t.Fatalf("status=%s chunks=%d points=%d dimension=%d", ready.Status, count, len(vectors.points), vectors.dimension)
	}
	for _, point := range vectors.points {
		if point.Payload["document_id"] != document.ID || len(point.Dense) != 64 || len(point.Sparse.Indices) == 0 {
			t.Fatalf("invalid point %#v", point)
		}
	}
	shadowVectors := &memoryVectors{}
	shadow := &indexer.Indexer{Store: s, Embedding: model.Fake{Dimension: 64}, Vectors: shadowVectors, Dimension: 64}
	if err := shadow.ShadowDocument(context.Background(), document.ID); err != nil {
		t.Fatal(err)
	}
	var afterShadow domain.Document
	if err := db.First(&afterShadow, document.ID).Error; err != nil {
		t.Fatal(err)
	}
	if afterShadow.Status != "ready" || afterShadow.IndexingTaskID != task.ID || len(shadowVectors.points) != int(count) {
		t.Fatalf("shadow changed source state: document=%#v points=%d", afterShadow, len(shadowVectors.points))
	}
}

func TestIndexerFailureResetsOrTerminatesDocument(t *testing.T) {
	cfg := config.Load()
	cfg.MySQLDSN = os.Getenv("LEARNQ_TEST_MYSQL_DSN")
	if cfg.MySQLDSN == "" {
		t.Skip("LEARNQ_TEST_MYSQL_DSN is required")
	}
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"document_chunks", "documents", "task_attempts", "outbox_events", "ai_tasks"} {
			db.Exec("DELETE FROM " + table)
		}
	})
	s := store.New(db)
	document, task, err := s.CreateDocument(context.Background(), domain.Document{
		Filename: "retry.md", MediaType: "md", ContentHash: "retry", Content: "# Retry\ncontent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error { return nil }, 10); err != nil {
		t.Fatal(err)
	}
	acquired, ok, err := s.Acquire(context.Background(), task.ID, "retry-token", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("acquire=%v err=%v", ok, err)
	}
	if err := db.Model(&document).Update("status", "indexing").Error; err != nil {
		t.Fatal(err)
	}
	if _, retry, err := s.FailDocument(context.Background(), acquired, "retry-token", errors.New("temporary"), false); err != nil || !retry {
		t.Fatal(err)
	}
	if err := db.First(&document, document.ID).Error; err != nil {
		t.Fatal(err)
	}
	if document.Status != "uploaded" {
		t.Fatalf("retry status = %q, want uploaded", document.Status)
	}
	if err := s.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error { return nil }, 10); err != nil {
		t.Fatal(err)
	}
	acquired, ok, err = s.Acquire(context.Background(), task.ID, "terminal-token", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("acquire terminal=%v err=%v", ok, err)
	}
	if err := db.Model(&document).Update("status", "indexing").Error; err != nil {
		t.Fatal(err)
	}
	if _, retry, err := s.FailDocument(context.Background(), acquired, "terminal-token", errors.New("terminal"), true); err != nil || retry {
		t.Fatal(err)
	}
	if err := db.First(&document, document.ID).Error; err != nil {
		t.Fatal(err)
	}
	if document.Status != "failed" {
		t.Fatalf("terminal status = %q, want failed", document.Status)
	}
	if err := db.Model(&document).Update("status", "deleting").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&document, document.ID).Error; err != nil {
		t.Fatal(err)
	}
	if document.Status != "deleting" {
		t.Fatalf("deleting document was resurrected as %q", document.Status)
	}
}

func TestDeletingDocumentCannotBeResurrectedAfterVectorUpsert(t *testing.T) {
	cfg := config.Load()
	cfg.MySQLDSN = os.Getenv("LEARNQ_TEST_MYSQL_DSN")
	if cfg.MySQLDSN == "" {
		t.Skip("LEARNQ_TEST_MYSQL_DSN is required")
	}
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"document_chunks", "documents", "task_attempts", "outbox_events", "ai_tasks"} {
			db.Exec("DELETE FROM " + table)
		}
	})
	s := store.New(db)
	document, task, err := s.CreateDocument(context.Background(), domain.Document{
		Filename: "delete-race.md", MediaType: "md", ContentHash: "delete-race",
		Content: "# Delete race\n" + strings.Repeat("worker upsert race ", 100),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error { return nil }, 10); err != nil {
		t.Fatal(err)
	}
	acquired, ok, err := s.Acquire(context.Background(), task.ID, "delete-race-token", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("acquire=%v err=%v", ok, err)
	}
	vectors := &memoryVectors{onUpsert: func() {
		if err := db.Model(&domain.Document{}).Where("id=?", document.ID).Update("status", "deleting").Error; err != nil {
			t.Errorf("mark deleting: %v", err)
		}
	}}
	processor := &indexer.Indexer{Store: s, Embedding: model.Fake{Dimension: 64}, Vectors: vectors, Dimension: 64}
	if _, err := processor.Process(context.Background(), acquired); err == nil {
		t.Fatal("delete race unexpectedly completed")
	}
	var current domain.Document
	if err := db.First(&current, document.ID).Error; err != nil {
		t.Fatal(err)
	}
	var chunks int64
	db.Model(&domain.DocumentChunk{}).Where("document_id=?", document.ID).Count(&chunks)
	if current.Status != "deleting" || chunks != 0 || len(vectors.points) != 0 {
		t.Fatalf("status=%s chunks=%d points=%d", current.Status, chunks, len(vectors.points))
	}
}
