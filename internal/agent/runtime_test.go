package agent

import (
	"encoding/json"
	"os"
	"strings"
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

func TestAgentEvalDatasetCoverageAndToolPolicy(t *testing.T) {
	body, err := os.ReadFile("../../data/eval/agent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]map[string]bool{
		TaskKnowledgeQA:        {"rag_search": true},
		TaskLearningReview:     {"study_history_search": true, "weekly_stats": true, "review_task_create": true},
		TaskProjectExplanation: {"rag_search": true, "study_history_search": true},
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatalf("invalid eval line: %v", err)
		}
		id, _ := item["id"].(string)
		message, _ := item["message"].(string)
		taskType, _ := item["task_type"].(string)
		if id == "" || message == "" || seen[id] {
			t.Fatalf("invalid or duplicate eval case: %#v", item)
		}
		if !validTask(taskType) {
			t.Fatalf("unsupported task type in eval case %s: %s", id, taskType)
		}
		seen[id], counts[taskType] = true, counts[taskType]+1
		tools, _ := item["tools"].([]any)
		for _, value := range tools {
			name, _ := value.(string)
			if !allowed[taskType][name] {
				t.Fatalf("eval case %s uses forbidden tool %s", id, name)
			}
		}
	}
	if len(seen) != 12 || counts[TaskKnowledgeQA] < 4 || counts[TaskLearningReview] < 4 || counts[TaskProjectExplanation] < 4 {
		t.Fatalf("eval coverage=%#v cases=%d", counts, len(seen))
	}
}
