package skill

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/WoAiXueXiHa/LeranQ/internal/model"
	"github.com/cloudwego/eino/compose"
)

// einoInput 是图的 START 输入：Modules 决定路由，Payload 为各 Agent 共享的原始输入。
type einoInput struct {
	Modules []string
	Payload json.RawMessage
}

// einoAgentOutput 是单个 Agent 节点的输出载荷，经图上下文传给 Planner 汇聚。
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
		// 显式复制循环变量：Lambda 闭包稍后执行，捕获副本避免路由错乱。
		route := route
		lambda := compose.InvokableLambda(func(ctx context.Context, in einoInput) (einoAgentOutput, error) {
			response, tools, err := r.RunDetailed(ctx, route, in.Payload)
			output := einoAgentOutput{Response: response, Tools: tools}
			if err != nil {
				output.Error = err.Error()
			}
			return output, nil
		})
		// WithOutputKey 把各 Agent 输出按路由名存入图上下文，Planner 节点按 key 取回。
		if err := graph.AddLambdaNode(route, lambda, compose.WithOutputKey(route)); err != nil {
			return WorkflowResult{}, err
		}
		if err := graph.AddEdge(compose.START, route); err != nil {
			return WorkflowResult{}, err
		}
	}
	planner := compose.InvokableLambda(func(ctx context.Context, values map[string]any) (WorkflowResult, error) {
		result := WorkflowResult{Routes: append([]string{}, routes...), Outputs: map[string]model.ChatResponse{}, Tools: map[string][]ToolExecution{}, Errors: map[string]string{}}
		agentOutputs := make(map[string]any, len(routes))
		for _, route := range routes {
			value, exists := values[route]
			if !exists {
				result.Errors[route] = "agent output missing"
				continue
			}
			// 图上下文的元素类型是 any，按节点实际输出类型断言并防御意外类型。
			agentOutput, ok := value.(einoAgentOutput)
			if !ok {
				return result, fmt.Errorf("agent %s returned %T", route, value)
			}
			if agentOutput.Error != "" {
				result.Errors[route] = agentOutput.Error
			} else {
				result.Outputs[route] = agentOutput.Response
				result.Tools[route] = agentOutput.Tools
				// Agent 输出优先按 JSON 结构传递；解析失败时回退为原始文本。
				var structured any
				if json.Unmarshal([]byte(agentOutput.Response.Content), &structured) != nil {
					structured = agentOutput.Response.Content
				}
				agentOutputs[route] = structured
			}
		}
		// 原始输入解析失败时以空对象兜底，保证 Planner 仍能获得合法输入。
		var request any
		if json.Unmarshal(input, &request) != nil {
			request = map[string]any{}
		}
		// Planner 输入由 Go 重组：原始 request + 各 Agent 的结构化输出与错误，
		// 汇聚的是结构化事实而非自由文本，避免模型改写上下文。
		plannerInput, _ := json.Marshal(map[string]any{
			"request": request, "agent_outputs": agentOutputs, "agent_errors": result.Errors,
		})
		plan, tools, err := r.RunDetailed(ctx, "weekly-plan", plannerInput)
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
