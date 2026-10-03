package store

import (
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
)

func TestFeedbackPriorityPreservesOriginalAndWithinTypeOrder(t *testing.T) {
	input := []domain.FeedbackItem{
		{Type: "expression", Explanation: "style"},
		{Type: "omission", Explanation: "first omission"},
		{Type: "conflict", Explanation: "suspected conflict"},
		{Type: "omission", Explanation: "second omission"},
	}
	ordered := orderedFeedback(input)
	want := []string{"suspected conflict", "first omission", "second omission", "style"}
	for i := range want {
		if ordered[i].Explanation != want[i] {
			t.Fatalf("item %d: got %q, want %q", i, ordered[i].Explanation, want[i])
		}
	}
	if input[0].Type != "expression" || input[1].Explanation != "first omission" {
		t.Fatal("sorting changed the original provider item order")
	}
}
