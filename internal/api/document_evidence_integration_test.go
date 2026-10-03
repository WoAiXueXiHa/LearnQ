//go:build integration

package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/api"
	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/WoAiXueXiHa/LearnQ/internal/skill"
)

func TestHistoricalEvidenceEndpointUsesSnapshotAndReportsInvalidity(t *testing.T) {
	f := setup(t)
	hash := func(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
	source := "# 标题\r\n旧中文证据\r\n"
	document := domain.Document{Filename: "historical.md", MediaType: "md", Content: "changed mutable content", ContentHash: hash("changed mutable content"), Status: "ready", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.store.DB.Create(&document).Error; err != nil {
		t.Fatal(err)
	}
	index := domain.DocumentIndex{DocumentID: document.ID, TaskID: 991, ExecutionGeneration: 1, AttemptNo: 1, Content: source, ContentHash: hash(source), IndexVersion: "old", ChunkVersion: "synthetic-test", Dimension: 64, Status: "retired", CreatedAt: time.Now()}
	if err := f.store.DB.Create(&index).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.store.DB.Where("id=?", index.ID).Delete(&domain.DocumentIndex{}) })
	text := "旧中文证据"
	start := strings.Index(source, text)
	chunk := domain.DocumentChunk{ID: hash("immutable chunk identity"), DocumentID: document.ID, IndexID: index.ID, ChunkIndex: 0, StartByte: start, EndByte: start + len(text), StartLine: 2, EndLine: 2, Content: text, ContentHash: hash(text), CreatedAt: time.Now()}
	if err := f.store.DB.Create(&chunk).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/documents/%d/indexes/%d/chunks/%s", document.ID, index.ID, chunk.ID)
	rec := request(t, f, http.MethodGet, path, "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"active":false`) || !strings.Contains(rec.Body.String(), hash(source)) {
		t.Fatalf("history=%d %s", rec.Code, rec.Body.String())
	}
	if err := f.store.DB.Model(&index).Update("status", "building").Error; err != nil {
		t.Fatal(err)
	}
	if rec := request(t, f, http.MethodGet, path, "", nil); rec.Code != 409 {
		t.Fatalf("building=%d %s", rec.Code, rec.Body.String())
	}
	f.store.DB.Model(&index).Update("status", "retired")
	f.store.DB.Model(&chunk).Update("start_byte", start+1)
	if rec := request(t, f, http.MethodGet, path, "", nil); rec.Code != 410 {
		t.Fatalf("invalid evidence=%d %s", rec.Code, rec.Body.String())
	}
	f.store.DB.Model(&chunk).Update("start_byte", start)
	f.store.DB.Model(&document).Updates(map[string]any{"active_index_id": index.ID, "status": "uploaded"})
	vectors := historicalCitationVectors{chunkID: chunk.ID}
	f.handler = api.New(f.store, skill.New(model.Fake{}), api.WithRAG(model.Fake{}, model.Fake{Dimension: 64}, vectors)).Handler()
	query := request(t, f, http.MethodPost, "/api/v1/rag/query", `{"question":"旧中文证据","top_k":1}`, map[string]string{"Content-Type": "application/json"})
	if query.Code != 200 {
		t.Fatalf("query=%d %s", query.Code, query.Body.String())
	}
	var result struct {
		Data struct {
			Citations []struct {
				EvidenceURL string `json:"evidence_url"`
				IndexID     uint64 `json:"index_id"`
				StartByte   int    `json:"start_byte"`
			} `json:"citations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(query.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data.Citations) != 1 || result.Data.Citations[0].EvidenceURL != path || result.Data.Citations[0].IndexID != index.ID || result.Data.Citations[0].StartByte != start {
		t.Fatalf("missing immutable citation: %s", query.Body.String())
	}
	f.store.DB.Model(&document).Updates(map[string]any{"active_index_id": index.ID + 1, "status": "ready"})
	if rec := request(t, f, http.MethodGet, result.Data.Citations[0].EvidenceURL, "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"active":false`) {
		t.Fatalf("saved citation after switch=%d %s", rec.Code, rec.Body.String())
	}
	f.store.DB.Model(&document).Update("status", "deleting")
	if rec := request(t, f, http.MethodGet, path, "", nil); rec.Code != 404 {
		t.Fatalf("deleted evidence=%d %s", rec.Code, rec.Body.String())
	}
}

type historicalCitationVectors struct{ chunkID string }

func (v historicalCitationVectors) Dense(context.Context, []float32, int) ([]rag.Hit, error) {
	return []rag.Hit{{Payload: map[string]any{"chunk_id": v.chunkID}}}, nil
}
func (v historicalCitationVectors) Sparse(context.Context, rag.SparseVector, int) ([]rag.Hit, error) {
	return v.Dense(context.Background(), nil, 1)
}
func (v historicalCitationVectors) Hybrid(context.Context, []float32, rag.SparseVector, int) ([]rag.Hit, error) {
	return v.Dense(context.Background(), nil, 1)
}
func (v historicalCitationVectors) DeleteDocument(context.Context, uint64) error { return nil }
