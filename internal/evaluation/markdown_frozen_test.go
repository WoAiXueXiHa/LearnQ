package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
)

// Optional private-source regression. The synthetic cases remain public and independent.
func TestFrozenRedisMarkdownSpansAndThreeRouteMockReport(t *testing.T) {
	path := os.Getenv("REDIS_ARTICLE_PATH")
	if path == "" {
		t.Skip("REDIS_ARTICLE_PATH is required for private frozen source regression")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join("../..", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../data/eval/redis_persistence_evidence.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ArticleSHA256 string `json:"article_sha256"`
		Questions     []struct {
			ID       string          `json:"id"`
			Question string          `json:"question"`
			Evidence []EvidencePoint `json:"evidence"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	if hash != fixture.ArticleSHA256 {
		t.Fatalf("frozen input hash mismatch got %s", hash)
	}
	chunks, err := rag.ChunkMarkdown(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]EvidenceChunk, len(chunks))
	hits := make([]rag.Hit, len(chunks))
	for i, c := range chunks {
		start := c.StartByte
		id := fmt.Sprintf("structure-%d", i)
		rows[i] = EvidenceChunk{ID: id, Content: c.Content, StartLine: c.StartLine, EndLine: c.EndLine, StartByte: &start}
		hits[i] = rag.Hit{Payload: map[string]any{"chunk_id": id, "document_id": uint64(7), "start_line": c.StartLine, "end_line": c.EndLine, "summary": c.Content}}
	}
	cases := make([]Case, len(fixture.Questions))
	for i, q := range fixture.Questions {
		cases[i] = Case{ID: q.ID, Question: q.Question, DocumentID: 7, ArticleSHA256: hash, EvidencePoints: q.Evidence}
	}
	resolved, err := ResolveEvidencePoints(context.Background(), cases, func(context.Context, uint64) (string, []EvidenceChunk, error) { return string(raw), rows, nil })
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), resolved, model.Fake{Dimension: 64}, frozenAllCandidates{hits: hits}, len(hits), false)
	if err != nil {
		t.Fatal(err)
	}
	report.Indexes = []IndexMetadata{{DocumentID: 7, IndexID: 1, ArticleSHA256: hash, ChunkVersion: rag.MarkdownChunkVersion, Dimension: 64}}
	rendered := Markdown(report)
	for _, id := range []string{"redis-2", "redis-4"} {
		if !strings.Contains(rendered, id) {
			t.Fatalf("diagnostic %s missing", id)
		}
	}
	if err := os.MkdirAll("../../data/reports/p1-structure", 0700); err != nil {
		t.Fatal(err)
	}
	reportPath := "../../data/reports/p1-structure/frozen-mock-report.md"
	disclaimer := "# 结构切块定位诊断（Mock）\n\n返回全文所有结构块作为人工控制候选，三路相同；仅检验真实结构raw offsets映射与逐点报告。不是检索TopK质量或模型能力实验。真实模型调用0次。\n\n"
	if err := os.WriteFile(reportPath, []byte(disclaimer+rendered), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("frozen_sha256=%s structure_chunks=%d questions=%d mock_report=%s", hash, len(chunks), len(cases), reportPath)
}

type frozenAllCandidates struct{ hits []rag.Hit }

func (r frozenAllCandidates) Dense(context.Context, []float32, int) ([]rag.Hit, error) {
	return r.hits, nil
}
func (r frozenAllCandidates) Sparse(context.Context, rag.SparseVector, int) ([]rag.Hit, error) {
	return r.hits, nil
}
func (r frozenAllCandidates) Hybrid(context.Context, []float32, rag.SparseVector, int) ([]rag.Hit, error) {
	return r.hits, nil
}
