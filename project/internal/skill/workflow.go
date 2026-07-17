package skill

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
)

type WorkflowResult struct {
	Routes  []string                      `json:"routes"`
	Outputs map[string]model.ChatResponse `json:"outputs"`
	Tools   map[string][]ToolExecution    `json:"tools,omitempty"`
	Errors  map[string]string             `json:"errors,omitempty"`
}

// RunWorkflow is deterministic orchestration: the model never selects routes or
// mutates task state. Independent agents run concurrently; Planner runs last.
func (r *Registry) RunWorkflow(ctx context.Context, modules []string, input json.RawMessage) WorkflowResult {
	routes := []string{"daily-review", "interview-followup"}
	for _, module := range modules {
		if module == "algorithm" {
			routes = append(routes, "algorithm-diagnosis")
			break
		}
	}
	result := WorkflowResult{Routes: routes, Outputs: map[string]model.ChatResponse{}, Tools: map[string][]ToolExecution{}, Errors: map[string]string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, route := range routes {
		route := route
		wg.Add(1)
		go func() {
			defer wg.Done()
			output, tools, err := r.RunDetailed(ctx, route, input)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				result.Errors[route] = err.Error()
			} else {
				result.Outputs[route] = output
				result.Tools[route] = tools
			}
		}()
	}
	wg.Wait()
	planner, tools, err := r.RunDetailed(ctx, "weekly-plan", input)
	result.Routes = append(result.Routes, "weekly-plan")
	if err != nil {
		result.Errors["weekly-plan"] = err.Error()
	} else {
		result.Outputs["weekly-plan"] = planner
		result.Tools["weekly-plan"] = tools
	}
	return result
}
