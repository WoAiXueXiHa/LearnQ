package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
)

func TestReferenceItemsValidation(t *testing.T) {
	create := func() []domain.QuestionDraft {
		questions := []domain.QuestionDraft{}
		for i := 0; i < 5; i++ {
			questions = append(questions, domain.QuestionDraft{Prompt: fmt.Sprintf("题%d", i), KnowledgePoints: []string{"概念"}, ReferencePoints: []string{"概念", "边界"}, ChunkIDs: []string{"chunk"}, ReferenceItems: []domain.ReferenceItem{{Text: "概念", ChunkIDs: []string{"chunk"}}, {Text: "边界", ChunkIDs: []string{"chunk"}}}})
		}
		return questions
	}
	if err := validateQuestionDrafts(create()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*domain.QuestionDraft)
	}{
		{"foreign chunk", func(q *domain.QuestionDraft) { q.ReferenceItems[0].ChunkIDs = []string{"foreign"} }},
		{"foreign image", func(q *domain.QuestionDraft) { q.ReferenceItems[0].ImageRefIDs = []uint64{19} }},
		{"no support", func(q *domain.QuestionDraft) { q.ReferenceItems[0].ChunkIDs = nil }},
		{"too long", func(q *domain.QuestionDraft) { q.ReferenceItems[0].Text = strings.Repeat("中", 241) }},
		{"mismatch", func(q *domain.QuestionDraft) { q.ReferencePoints[0] = "different" }},
		{"one point", func(q *domain.QuestionDraft) { q.ReferenceItems = q.ReferenceItems[:1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := create()
			tc.change(&q[0])
			if validateQuestionDrafts(q) == nil {
				t.Fatal("invalid item accepted")
			}
		})
	}
	legacy := create()
	for i := range legacy {
		legacy[i].ReferenceItems = nil
	}
	if err := validateQuestionDrafts(legacy); err != nil {
		t.Fatalf("legacy rejected: %v", err)
	}
}
