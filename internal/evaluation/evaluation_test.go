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
