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

	"github.com/WoAiXueXiHa/LeranQ/project/internal/bootstrap"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/indexer"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/migrate"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/rag"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
)

type memoryVectors struct {
	dimension int
	points    []rag.Point
}

func (m *memoryVectors) EnsureCollection(_ context.Context, dimension int) error {
	m.dimension = dimension
	return nil
}
func (m *memoryVectors) Upsert(_ context.Context, points []rag.Point) error {
	m.points = append(m.points, points...)
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
	if err := processor.Process(context.Background(), acquired); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteWithoutReport(context.Background(), acquired, "index-token"); err != nil {
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
	processor := &indexer.Indexer{Store: s}
	if err := db.Model(&document).Update("status", "indexing").Error; err != nil {
		t.Fatal(err)
	}
	if err := processor.HandleFailure(context.Background(), task, false, errors.New("temporary")); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&document, document.ID).Error; err != nil {
		t.Fatal(err)
	}
	if document.Status != "uploaded" {
		t.Fatalf("retry status = %q, want uploaded", document.Status)
	}
	if err := processor.HandleFailure(context.Background(), task, true, errors.New("terminal")); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&document, document.ID).Error; err != nil {
		t.Fatal(err)
	}
	if document.Status != "failed" {
		t.Fatalf("terminal status = %q, want failed", document.Status)
	}
}
