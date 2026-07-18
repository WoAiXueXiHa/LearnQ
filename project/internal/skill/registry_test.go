package skill

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
)

func TestRegistry(t *testing.T) {
	r := New(model.Fake{})
	if len(r.List()) != 6 {
		t.Fatalf("got %d skills", len(r.List()))
	}
	if _, ok := r.Get("weekly-plan"); !ok {
		t.Fatal("weekly-plan missing")
	}
	if r.List()[5].Name != "multi-agent" {
		t.Fatalf("multi-agent experiment missing: %#v", r.List())
	}
	if _, err := r.Run(context.Background(), "missing", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown skill accepted")
	}
}

func TestSkillOutputsDifferAndToolResultIsCaptured(t *testing.T) {
	registry := New(model.Fake{})
	registry.RegisterTool("weekly_stats", func(context.Context, json.RawMessage) (json.RawMessage, json.RawMessage, error) {
		return json.RawMessage(`{"minutes":90,"records":3,"fact_source":"mysql"}`), json.RawMessage("[]"), nil
	})
	input := json.RawMessage(`{"title":"LearnQ","summary":"异步任务"}`)
	daily, _, err := registry.RunDetailed(context.Background(), "daily-review", input)
	if err != nil {
		t.Fatal(err)
	}
	weekly, tools, err := registry.RunDetailed(context.Background(), "weekly-plan", input)
	if err != nil {
		t.Fatal(err)
	}
	if daily.Content == weekly.Content || !strings.Contains(weekly.Content, "一周学习计划") {
		t.Fatalf("daily=%s weekly=%s", daily.Content, weekly.Content)
	}
	if len(tools) != 1 || tools[0].Name != "weekly_stats" || !strings.Contains(string(tools[0].Response), `"minutes":90`) {
		t.Fatalf("tools=%#v", tools)
	}
}

func TestWorkflowConditionalAlgorithm(t *testing.T) {
	r := New(model.Fake{})
	got := r.RunWorkflow(context.Background(), []string{"backend"}, json.RawMessage(`{}`))
	for _, route := range got.Routes {
		if route == "algorithm-diagnosis" {
			t.Fatal("algorithm route unexpectedly used")
		}
	}
	got = r.RunWorkflow(context.Background(), []string{"algorithm"}, json.RawMessage(`{}`))
	if _, ok := got.Outputs["algorithm-diagnosis"]; !ok {
		t.Fatal("algorithm route missing")
	}
}

func TestOutputSchemaRejectsInvalidModelJSON(t *testing.T) {
	r := New(model.Fake{Failure: "invalid_json"})
	if _, err := r.Run(context.Background(), "daily-review", json.RawMessage(`{}`)); err == nil {
		t.Fatal("invalid JSON passed output schema")
	}
}

func TestEinoComposeConditionalParallelWorkflow(t *testing.T) {
	r := New(model.Fake{})
	without, err := r.RunEinoWorkflow(context.Background(), []string{"backend"}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := without.Outputs["algorithm-diagnosis"]; exists {
		t.Fatal("algorithm agent ran without algorithm module")
	}
	with, err := r.RunEinoWorkflow(context.Background(), []string{"backend", "algorithm"}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := with.Outputs["algorithm-diagnosis"]; !exists {
		t.Fatal("algorithm agent did not run")
	}
}
