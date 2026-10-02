package evaluation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
)

type fakeRetriever struct{}

func (fakeRetriever) Dense(context.Context, []float32, int) ([]rag.Hit, error) {
	return hits("x", "a"), nil
}
func (fakeRetriever) Sparse(context.Context, rag.SparseVector, int) ([]rag.Hit, error) {
	return hits("a", "b"), nil
}
func (fakeRetriever) Hybrid(context.Context, []float32, rag.SparseVector, int) ([]rag.Hit, error) {
	return hits("a", "b"), nil
}
func hits(ids ...string) []rag.Hit {
	out := make([]rag.Hit, len(ids))
	for i, id := range ids {
		out[i].Payload = map[string]any{"chunk_id": id}
	}
	return out
}

func TestReadAndCompareThreeRetrievers(t *testing.T) {
	cases, err := ReadJSONL(strings.NewReader(`{"id":"e1","question":"q","relevant_chunks":[{"chunk_id":"a","relevance":3},{"chunk_id":"b","relevance":1}],"correct_answer":"a","tags":["rag"],"difficulty":"easy"}`))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), cases, model.Fake{Dimension: 8}, fakeRetriever{}, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Retrievers) != 3 || report.Retrievers["hybrid-rrf"].RecallAtK != 1 || report.Retrievers["dense-only"].RecallAtK != .5 {
		t.Fatalf("report=%#v", report)
	}
	if !strings.Contains(Markdown(report), "pipeline_test") {
		t.Fatal("mode missing")
	}
}

func TestReadJSONLRejectsBothAnnotationStyles(t *testing.T) {
	input := `{"id":"e1","question":"q","relevant_chunks":[{"chunk_id":"a","relevance":3}],"citation_text":"snippet"}`
	_, err := ReadJSONL(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "both relevant_chunks and citation_text") {
		t.Fatalf("mixed annotation error=%v", err)
	}
}

// TestRunKeepsPerCaseCandidates 验证逐例证据：命中与漏证据都要能在报告里看到
// 具体排名、来源行号和摘录，而不是只留一个均值。
func TestRunKeepsPerCaseCandidates(t *testing.T) {
	payloadRetriever := payloadFake{}
	cases := []Case{{ID: "e1", Question: "q", RelevantChunks: []Judgment{{ChunkID: "wanted", Relevance: 3}}}}
	report, err := Run(context.Background(), cases, model.Fake{Dimension: 8}, payloadRetriever, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 1 {
		t.Fatalf("cases=%#v", report.Cases)
	}
	dense := report.Cases[0].Candidates["dense-only"]
	if len(dense) != 2 || dense[0].Relevant || dense[1].ChunkID != "wanted" {
		t.Fatalf("dense candidates=%#v", dense)
	}
	if report.Cases[0].HitByRetriever["dense-only"] != true || report.Cases[0].HitByRetriever["sparse-only"] != true {
		t.Fatalf("hits=%#v", report.Cases[0].HitByRetriever)
	}
	markdown := Markdown(report)
	for _, want := range []string{"retrieval_benchmark", "真实 embedding", "命中标注块：dense-only ✅", "行区间", "42-58", "没被标注的候选", "## 逐例结果"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("markdown misses %q:\n%s", want, markdown)
		}
	}
}

type payloadFake struct{}

func (payloadFake) Dense(context.Context, []float32, int) ([]rag.Hit, error) {
	return []rag.Hit{
		{Payload: map[string]any{"chunk_id": "noise", "document_id": float64(7), "title": "无关段落", "start_line": float64(9), "end_line": float64(12), "summary": "没被标注的候选"}},
		{Payload: map[string]any{"chunk_id": "wanted", "document_id": uint64(3), "title": "Lease 与 Fencing", "start_line": int(42), "end_line": int64(58), "summary": "Worker 只有在 MySQL 成功持久化结果后才清理 Redis processing 状态。"}},
	}, nil
}
func (payloadFake) Sparse(context.Context, rag.SparseVector, int) ([]rag.Hit, error) {
	return hits("wanted"), nil
}
func (payloadFake) Hybrid(context.Context, []float32, rag.SparseVector, int) ([]rag.Hit, error) {
	return hits("wanted"), nil
}

func TestReadJSONLRejectsPlaceholderChunkID(t *testing.T) {
	_, err := ReadJSONL(strings.NewReader(`{"id":"e1","question":"q","relevant_chunks":[{"chunk_id":"replace-with-indexed-chunk-id","relevance":3}]}`))
	if err == nil || !strings.Contains(err.Error(), "placeholder chunk_id") {
		t.Fatalf("placeholder error=%v", err)
	}
}

func TestReadJSONLAllowsCitationTextOnlyCases(t *testing.T) {
	cases, err := ReadJSONL(strings.NewReader(`{"id":"e1","question":"q","citation_text":"lease token 与 generation","correct_answer":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || len(cases[0].RelevantChunks) != 0 || cases[0].CitationText == "" {
		t.Fatalf("cases=%#v", cases)
	}
}

func TestResolveCitationText(t *testing.T) {
	input := []Case{{ID: "e1", Question: "q", CitationText: "unique"}}
	resolved, err := ResolveCitationText(context.Background(), input, func(_ context.Context, text string) ([]string, error) {
		if text != "unique" {
			return nil, fmt.Errorf("unexpected text %q", text)
		}
		return []string{"chunk-1"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved[0].RelevantChunks; len(got) != 1 || got[0].ChunkID != "chunk-1" || got[0].Relevance != 3 {
		t.Fatalf("resolved=%#v", resolved)
	}
}

func TestResolveCitationTextRejectsMissingOrAmbiguousMatches(t *testing.T) {
	for _, test := range []struct {
		name string
		ids  []string
		want string
	}{
		{name: "missing", ids: nil, want: "matched no ready chunk"},
		{name: "ambiguous", ids: []string{"a", "b"}, want: "ambiguous"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveCitationText(context.Background(), []Case{{ID: "e1", Question: "q", CitationText: "text"}}, func(context.Context, string) ([]string, error) {
				return test.ids, nil
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestReadJSONLRejectsInvalidJudgments(t *testing.T) {
	tests := []string{
		`{"id":"negative","question":"q","relevant_chunks":[{"chunk_id":"a","relevance":-1}]}`,
		`{"id":"duplicate","question":"q","relevant_chunks":[{"chunk_id":"a","relevance":1},{"chunk_id":"a","relevance":2}]}`,
		`{"id":"no-positive","question":"q","relevant_chunks":[{"chunk_id":"a","relevance":0}]}`,
	}
	for _, input := range tests {
		if _, err := ReadJSONL(strings.NewReader(input)); err == nil {
			t.Fatalf("invalid judgments accepted: %s", input)
		}
	}
}
