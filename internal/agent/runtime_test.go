package agent

import (
	"encoding/json"
	"testing"
)

func TestInferTask(t *testing.T) {
	cases := []struct {
		message string
		want    string
	}{
		{"复盘今天学习的 Go 并发", TaskLearningReview},
		{"准备项目讲解", TaskProjectExplanation},
		{"如何理解 RAG 引用", TaskKnowledgeQA},
	}
	for _, tc := range cases {
		if got := inferTask(tc.message); got != tc.want {
			t.Fatalf("inferTask(%q)=%q, want %q", tc.message, got, tc.want)
		}
	}
}

func TestAllowedToolBoundary(t *testing.T) {
	if !allowedTool(TaskKnowledgeQA, "rag_search") {
		t.Fatal("knowledge QA should allow rag_search")
	}
	if allowedTool(TaskKnowledgeQA, "review_task_create") {
		t.Fatal("knowledge QA must not allow state-changing review tool")
	}
	if !allowedTool(TaskLearningReview, "weekly_stats") {
		t.Fatal("learning review should allow weekly_stats")
	}
}

func TestCheckAnswerCitationsAndEvidenceBoundary(t *testing.T) {
	observations := []map[string]any{{
		"citations": json.RawMessage("[{\"source\":\"S1\",\"content\":\"可靠证据\"}]"),
	}}
	valid := checkAnswer(TaskKnowledgeQA, "回答内容 [S1]", observations)
	if !valid.CitationValid || !valid.Grounded {
		t.Fatalf("valid citation rejected: %#v", valid)
	}
	invalid := checkAnswer(TaskKnowledgeQA, "回答内容 [S2]", observations)
	if invalid.CitationValid {
		t.Fatalf("out-of-range citation accepted: %#v", invalid)
	}
	empty := checkAnswer(TaskKnowledgeQA, "无法回答", nil)
	if empty.Grounded || empty.CitationValid {
		t.Fatalf("empty evidence was treated as grounded: %#v", empty)
	}
}
