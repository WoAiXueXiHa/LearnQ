package evaluation

import (
	"context"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LeranQ/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/internal/rag"
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
	cases, err := ReadJSONL(strings.NewReader(`{"id":"e1","question":"q","relevant_chunks":[{"chunk_id":"a","relevance":3},{"chunk_id":"b","relevance":1}],"citation_text":"c","correct_answer":"a","tags":["rag"],"difficulty":"easy"}`))
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

func TestReadJSONLRejectsPlaceholderChunkID(t *testing.T) {
	_, err := ReadJSONL(strings.NewReader(`{"id":"e1","question":"q","relevant_chunks":[{"chunk_id":"replace-with-indexed-chunk-id","relevance":3}]}`))
	if err == nil || !strings.Contains(err.Error(), "placeholder chunk_id") {
		t.Fatalf("placeholder error=%v", err)
	}
}
