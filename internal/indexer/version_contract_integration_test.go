//go:build integration

package indexer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
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

func versionFixture(t *testing.T) (*store.Store, *indexer.Indexer, domain.Document, domain.AITask) {
	t.Helper()
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
		for _, table := range []string{"document_chunks", "document_indexes", "documents", "task_attempts", "outbox_events", "ai_tasks"} {
			if err := db.Exec("DELETE FROM " + table).Error; err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	s := store.New(db)
	content := "# 不可变证据\r\n重复中文\r\n重复中文\r\n"
	sum := sha256.Sum256([]byte(content))
	doc, task, err := s.CreateDocument(context.Background(), domain.Document{Filename: "versions.md", MediaType: "md", Content: content, ContentHash: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	p := &indexer.Indexer{Store: s, Embedding: model.Fake{Dimension: 64}, Vectors: &memoryVectors{}, Dimension: 64}
	task = acquireVersionTask(t, s, task, "first")
	id, err := p.Process(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDocumentIndex(context.Background(), task, "first", id, p.IndexVersion()); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&doc, doc.ID).Error; err != nil {
		t.Fatal(err)
	}
	return s, p, doc, task
}

func acquireVersionTask(t *testing.T, s *store.Store, task domain.AITask, token string) domain.AITask {
	t.Helper()
	if err := s.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error { return nil }, 100); err != nil {
		t.Fatal(err)
	}
	acquired, ok, err := s.Acquire(context.Background(), task.ID, token, time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("acquire ok=%v err=%v", ok, err)
	}
	return acquired
}

func TestVersionRebuildRetainsOldEvidenceAndSwitchesOnlyOnComplete(t *testing.T) {
	s, p, old, _ := versionFixture(t)
	if old.ActiveIndexID == 0 {
		t.Fatal("initial version was not activated")
	}
	var oldChunks []domain.DocumentChunk
	if err := s.DB.Where("document_id=?", old.ID).Find(&oldChunks).Error; err != nil {
		t.Fatal(err)
	}
	rebuilding, task, err := s.ReindexDocument(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := rag.EvidenceService{DB: s.DB, Embedding: model.Fake{Dimension: 64}, Vectors: versionHitRetriever{chunkID: oldChunks[0].ID}}
	evidence, err := service.Search(context.Background(), "旧中文", 1)
	if err != nil || len(evidence) != 1 || evidence[0].IndexID != old.ActiveIndexID {
		t.Fatalf("old active evidence unavailable: %#v %v", evidence, err)
	}
	if rebuilding.ActiveIndexID != old.ActiveIndexID {
		t.Fatalf("rebuild hid active evidence: %#v", rebuilding)
	}
	if _, _, err := s.ReindexDocument(context.Background(), old.ID); err == nil {
		t.Fatal("duplicate rebuild accepted")
	}
	task = acquireVersionTask(t, s, task, "second")
	if _, err := p.Process(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	var during domain.Document
	if err := s.DB.First(&during, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if during.ActiveIndexID != old.ActiveIndexID {
		t.Fatal("build switched active version before task completion")
	}
	if err := s.CompleteDocumentIndex(context.Background(), task, "second", old.ID, p.IndexVersion()); err != nil {
		t.Fatal(err)
	}
	var current domain.Document
	if err := s.DB.First(&current, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.ActiveIndexID == old.ActiveIndexID || current.Status != "ready" {
		t.Fatalf("not switched: %#v", current)
	}
	for _, oldChunk := range oldChunks {
		var retained domain.DocumentChunk
		if err := s.DB.Where("id=?", oldChunk.ID).First(&retained).Error; err != nil {
			t.Fatalf("old reference deleted: %v", err)
		}
		if retained != oldChunk {
			t.Fatalf("old immutable chunk rewritten: %#v vs %#v", retained, oldChunk)
		}
		if old.Content[retained.StartByte:retained.EndByte] != retained.Content {
			t.Fatal("original byte evidence changed")
		}
	}
}

func TestVersionLostLeaseCannotActivateAndFailureKeepsOldActive(t *testing.T) {
	s, p, old, _ := versionFixture(t)
	_, task, err := s.ReindexDocument(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	task = acquireVersionTask(t, s, task, "expired")
	if _, err := p.Process(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&domain.AITask{}).Where("id=?", task.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDocumentIndex(context.Background(), task, "expired", old.ID, p.IndexVersion()); err == nil {
		t.Fatal("expired lease activated version")
	}
	if _, _, err := s.FailExpiredDocument(context.Background(), task, "expired", errors.New("expired build")); err != nil {
		t.Fatal(err)
	}
	var current domain.Document
	if err := s.DB.First(&current, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.ActiveIndexID != old.ActiveIndexID {
		t.Fatalf("failure lost old version: %#v", current)
	}
}

type versionHitRetriever struct{ chunkID string }

func (v versionHitRetriever) Hybrid(context.Context, []float32, rag.SparseVector, int) ([]rag.Hit, error) {
	return []rag.Hit{{Payload: map[string]any{"chunk_id": v.chunkID}}}, nil
}

func TestVersionAutomaticRetryUsesSeparateAttemptIdentity(t *testing.T) {
	s, p, old, _ := versionFixture(t)
	_, task, err := s.ReindexDocument(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	task = acquireVersionTask(t, s, task, "retry-first")
	if _, err := p.Process(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if _, retry, err := s.FailDocument(context.Background(), task, "retry-first", errors.New("transient failure"), false); err != nil || !retry {
		t.Fatalf("retry=%v err=%v", retry, err)
	}
	if err := s.DB.Model(&domain.AITask{}).Where("id=?", task.ID).Update("available_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	next := acquireVersionTask(t, s, task, "retry-second")
	if next.AttemptNo <= task.AttemptNo {
		t.Fatal("attempt did not advance")
	}
	if _, err := p.Process(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDocumentIndex(context.Background(), next, "retry-second", old.ID, p.IndexVersion()); err != nil {
		t.Fatal(err)
	}
	var versions []domain.DocumentIndex
	if err := s.DB.Where("task_id=?", task.ID).Find(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].ID == versions[1].ID || versions[0].AttemptNo == versions[1].AttemptNo {
		t.Fatalf("retry identity not isolated: %#v", versions)
	}
	var current domain.Document
	if err := s.DB.First(&current, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.ActiveIndexID != versions[1].ID {
		t.Fatalf("wrong attempt activated: %#v", current)
	}
}

func TestLegacyEmptyVersionRebuildFailureRetainsEvidence(t *testing.T) {
	s, _, old, _ := versionFixture(t)
	if err := s.DB.Model(&domain.DocumentChunk{}).Where("document_id=?", old.ID).Update("index_id", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&domain.Document{}).Where("id=?", old.ID).Updates(map[string]any{"active_index_id": 0, "index_version": ""}).Error; err != nil {
		t.Fatal(err)
	}
	rebuilt, task, err := s.ReindexDocument(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.IndexVersion == "" {
		t.Fatal("unverified legacy active evidence was hidden")
	}
	task = acquireVersionTask(t, s, task, "legacy-fail")
	if _, retry, err := s.FailDocument(context.Background(), task, "legacy-fail", errors.New("terminal build failure"), true); err != nil || retry {
		t.Fatalf("terminal failure retry=%v err=%v", retry, err)
	}
	var current domain.Document
	if err := s.DB.First(&current, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != "ready" || current.ActiveIndexID != 0 || current.IndexVersion == "" || current.ErrorMessage == "" {
		t.Fatalf("legacy evidence not retained: %#v", current)
	}
}
