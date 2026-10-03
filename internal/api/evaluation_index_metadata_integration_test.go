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
	"github.com/WoAiXueXiHa/LearnQ/internal/evaluation"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/skill"
)

type countedEvalEmbedding struct{ calls int }

func (m *countedEvalEmbedding) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	m.calls++
	return model.Fake{Dimension: 64}.Embed(ctx, texts)
}

func TestEvaluationStoresActualActiveIndexMetadataAndResolveOnlyCallsNoModel(t *testing.T) {
	f := setup(t)
	source := "# 结构证据\r\n核心机制\r\n"
	sum := sha256.Sum256([]byte(source))
	hash := hex.EncodeToString(sum[:])
	doc := domain.Document{Filename: "metadata.md", MediaType: "md", Content: source, ContentHash: hash, Status: "ready", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.store.DB.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	index := domain.DocumentIndex{DocumentID: doc.ID, TaskID: 9981, ExecutionGeneration: 1, AttemptNo: 1, Content: source, ContentHash: hash, IndexVersion: "fake-actual-model-markdown", ChunkVersion: "markdown-block-800-v1", Dimension: 64, Status: "active", CreatedAt: time.Now()}
	if err := f.store.DB.Create(&index).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.store.DB.Where("id=?", index.ID).Delete(&domain.DocumentIndex{}) })
	if err := f.store.DB.Model(&doc).Update("active_index_id", index.ID).Error; err != nil {
		t.Fatal(err)
	}
	chunk := domain.DocumentChunk{ID: strings.Repeat("d", 64), DocumentID: doc.ID, IndexID: index.ID, ChunkIndex: 0, Content: source, ContentHash: hash, StartByte: 0, EndByte: len(source), StartLine: 1, EndLine: 2, CreatedAt: time.Now()}
	if err := f.store.DB.Create(&chunk).Error; err != nil {
		t.Fatal(err)
	}
	embedding := &countedEvalEmbedding{}
	f.handler = api.New(f.store, skill.New(model.Fake{}), api.WithRAG(model.Fake{}, embedding, historicalCitationVectors{chunkID: chunk.ID})).Handler()
	dataset := fmt.Sprintf(`{"id":"metadata-one","question":"核心机制是什么","document_id":%d,"article_sha256":%q,"evidence_points":[{"point":"机制","lines":[[2,2]]}]}`, doc.ID, hash)
	resolve := request(t, f, http.MethodPost, "/api/v1/evaluations/rag?resolve_only=true", dataset, map[string]string{"Content-Type": "application/x-ndjson"})
	if resolve.Code != 200 || embedding.calls != 0 {
		t.Fatalf("resolve=%d calls=%d %s", resolve.Code, embedding.calls, resolve.Body.String())
	}
	run := request(t, f, http.MethodPost, "/api/v1/evaluations/rag", dataset, map[string]string{"Content-Type": "application/x-ndjson"})
	if run.Code != 202 || embedding.calls != 1 {
		t.Fatalf("evaluate=%d calls=%d %s", run.Code, embedding.calls, run.Body.String())
	}
	var row struct {
		ConfigJSON     string
		ReportMarkdown string
	}
	if err := f.store.DB.Table("rag_evaluations").Order("id desc").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Indexes []evaluation.IndexMetadata `json:"indexes"`
	}
	if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	want := evaluation.IndexMetadata{DocumentID: doc.ID, IndexID: index.ID, ArticleSHA256: hash, IndexVersion: index.IndexVersion, ChunkVersion: index.ChunkVersion, Dimension: 64}
	if len(cfg.Indexes) != 1 || cfg.Indexes[0] != want {
		t.Fatalf("actual persisted index metadata wrong: %#v want %#v", cfg.Indexes, want)
	}
	if !strings.Contains(row.ReportMarkdown, index.ChunkVersion) || !strings.Contains(row.ReportMarkdown, hash) {
		t.Fatal("report lost actual index contract")
	}
}
