package skill

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
)

// WorkflowResult 汇总一次工作流的全部产出：路由顺序、各 Agent 输出、
// 工具执行记录与错误；整体随 API 响应返回，trace 则按成功路由单独落库。
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
		// 显式复制循环变量：goroutine 稍后执行时捕获的是本次迭代的局部副本，避免路由错乱。
		route := route
		wg.Add(1)
		go func() {
			defer wg.Done()
			output, tools, err := r.RunDetailed(ctx, route, input)
			mu.Lock()
			// 多个 Agent 共享 result，锁仅覆盖结果归并，不包住慢速模型调用。
			defer mu.Unlock()
			// 单个 Agent 失败仅记入 Errors，不中断其余并行 Agent。
			if err != nil {
				result.Errors[route] = err.Error()
			} else {
				result.Outputs[route] = output
				result.Tools[route] = tools
			}
		}()
	}
	// 汇聚节点在全部 Agent 收尾后串行运行，保证其看到完整输出。
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
