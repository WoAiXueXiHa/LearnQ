package skill

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/cloudwego/eino/compose"
)

type einoInput struct {
	Modules []string
	Payload json.RawMessage
}

type einoAgentOutput struct {
	Response model.ChatResponse
	Tools    []ToolExecution
	Error    string
}

// RunEinoWorkflow 使用 Eino Compose 构图，但路由仍由 Go 确定。
// 只有输入包含算法模块时才添加 Algorithm；各独立节点从 START 并行出发，
// Planner 是唯一汇聚节点，因此模型无法自行改写拓扑或越权调用其他 Skill。
func (r *Registry) RunEinoWorkflow(ctx context.Context, modules []string, input json.RawMessage) (WorkflowResult, error) {
	routes := []string{"daily-review", "interview-followup"}
	for _, module := range modules {
		if module == "algorithm" {
			routes = append(routes, "algorithm-diagnosis")
			break
		}
	}
	graph := compose.NewGraph[einoInput, WorkflowResult]()
	for _, route := range routes {
		route := route
		lambda := compose.InvokableLambda(func(ctx context.Context, in einoInput) (einoAgentOutput, error) {
			response, tools, err := r.RunDetailed(ctx, route, in.Payload)
			output := einoAgentOutput{Response: response, Tools: tools}
			if err != nil {
				output.Error = err.Error()
			}
			return output, nil
		})
		if err := graph.AddLambdaNode(route, lambda, compose.WithOutputKey(route)); err != nil {
			return WorkflowResult{}, err
		}
		if err := graph.AddEdge(compose.START, route); err != nil {
			return WorkflowResult{}, err
		}
	}
	planner := compose.InvokableLambda(func(ctx context.Context, values map[string]any) (WorkflowResult, error) {
		result := WorkflowResult{Routes: append([]string{}, routes...), Outputs: map[string]model.ChatResponse{}, Tools: map[string][]ToolExecution{}, Errors: map[string]string{}}
		for _, route := range routes {
			value, exists := values[route]
			if !exists {
				result.Errors[route] = "agent output missing"
				continue
			}
			agentOutput, ok := value.(einoAgentOutput)
			if !ok {
				return result, fmt.Errorf("agent %s returned %T", route, value)
			}
			if agentOutput.Error != "" {
				result.Errors[route] = agentOutput.Error
			} else {
				result.Outputs[route] = agentOutput.Response
				result.Tools[route] = agentOutput.Tools
			}
		}
		plan, tools, err := r.RunDetailed(ctx, "weekly-plan", input)
		result.Routes = append(result.Routes, "weekly-plan")
		if err != nil {
			result.Errors["weekly-plan"] = err.Error()
		} else {
			result.Outputs["weekly-plan"] = plan
			result.Tools["weekly-plan"] = tools
		}
		return result, nil
	})
	if err := graph.AddLambdaNode("planner", planner); err != nil {
		return WorkflowResult{}, err
	}
	for _, route := range routes {
		if err := graph.AddEdge(route, "planner"); err != nil {
			return WorkflowResult{}, err
		}
	}
	if err := graph.AddEdge("planner", compose.END); err != nil {
		return WorkflowResult{}, err
	}
	runnable, err := graph.Compile(ctx, compose.WithGraphName("learnq-multi-agent"))
	if err != nil {
		return WorkflowResult{}, err
	}
	return runnable.Invoke(ctx, einoInput{Modules: modules, Payload: input})
}
