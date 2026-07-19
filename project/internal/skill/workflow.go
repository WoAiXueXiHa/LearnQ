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

// RunWorkflow 采用确定性编排：模型不选择路由，也不修改任务状态。
// 独立 Agent 并行执行以降低总延迟，Planner 在它们完成后作为唯一汇聚节点运行。
func (r *Registry) RunWorkflow(ctx context.Context, modules []string, input json.RawMessage) WorkflowResult {
	routes := []string{"daily-review", "interview-followup"}
	for _, module := range modules {
		// Algorithm 节点按输入模块显式启用，避免无关 Agent 消耗调用额度或引入噪声。
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
			// 多个 Agent 共享 result，锁仅覆盖结果归并，不包住慢速模型调用。
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
