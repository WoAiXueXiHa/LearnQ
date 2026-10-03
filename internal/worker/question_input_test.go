package worker

import (
	"encoding/json"
	"strings"
	"testing"

	"context"
	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
)

func TestQuestionInputBoundAndCoverage(t *testing.T) {
	chunks := []domain.DocumentChunk{{ID: "a", Content: "中文证据", HeadingPathJSON: `["概念"]`}, {ID: "b", Content: "边界", HeadingPathJSON: `["边界"]`}}
	images := []map[string]any{{"image_ref_id": uint64(17), "description": "合成图"}}
	input, err := buildQuestionInput(chunks, images)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(input, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["article"]; exists {
		t.Fatal("article duplicated")
	}
	if strings.Contains(string(input), "created_at") {
		t.Fatal("database metadata sent")
	}
	response, err := (model.Fake{}).Generate(context.Background(), model.ChatRequest{Skill: "practice-questions", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Questions []domain.QuestionDraft `json:"questions"`
	}
	if err := json.Unmarshal([]byte(response.Content), &output); err != nil {
		t.Fatal(err)
	}
	if err := validateQuestionCoverage(chunks, images, output.Questions); err != nil {
		t.Fatal(err)
	}
	for i := range output.Questions {
		output.Questions[i].ImageRefIDs = nil
	}
	if validateQuestionCoverage(chunks, images, output.Questions) == nil {
		t.Fatal("missing image accepted")
	}
	for i := range output.Questions {
		output.Questions[i].ChunkIDs = []string{"a"}
	}
	if validateQuestionCoverage(chunks, nil, output.Questions) == nil {
		t.Fatal("missing section accepted")
	}
	output.Questions[0].ChunkIDs = []string{"invented"}
	if validateQuestionCoverage(chunks, nil, output.Questions) == nil {
		t.Fatal("unknown ID accepted")
	}
	chunks[0].Content = strings.Repeat("中", maxQuestionInputBytes)
	if _, err := buildQuestionInput(chunks, nil); err == nil {
		t.Fatal("oversized input silently accepted")
	}
}

func TestHeadingOnlyParentCoverageKeepsSubstantiveIntroductions(t *testing.T) {
	chunks := []domain.DocumentChunk{{ID: "title", Content: "# Topic\n", HeadingPathJSON: `["Topic"]`}, {ID: "body", Content: "## Mechanism\nTwo facts.", HeadingPathJSON: `["Topic","Mechanism"]`}}
	questions := make([]domain.QuestionDraft, 5)
	for i := range questions {
		questions[i] = domain.QuestionDraft{ChunkIDs: []string{"body"}, ReferenceItems: []domain.ReferenceItem{{Text: "First", ChunkIDs: []string{"body"}}, {Text: "Second", ChunkIDs: []string{"body"}}}}
	}
	if err := validateQuestionCoverage(chunks, nil, questions); err != nil {
		t.Fatal(err)
	}
	chunks[0].Content = "# Topic\nAn important introductory fact."
	if validateQuestionCoverage(chunks, nil, questions) == nil {
		t.Fatal("substantive parent must still be covered")
	}
	chunks[0].Content = "# Topic\n"
	chunks[0].HeadingPathJSON = `["Unrelated"]`
	if validateQuestionCoverage(chunks, nil, questions) == nil {
		t.Fatal("unrelated heading must not be covered")
	}
}
