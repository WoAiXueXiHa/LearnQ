package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/cloudwego/eino/compose"
)

// einoInput is the graph START input. The route topology is selected by Go;
// Payload is carried through agent outputs so a compiled graph is reusable.
type einoInput struct {
	Modules []string
	Payload json.RawMessage
}

type einoAgentOutput struct {
	Response model.ChatResponse
	Tools    []ToolExecution
	Error    string
	Payload  json.RawMessage
	Latency  time.Duration
}

// RunEinoWorkflow is an experimental deterministic DAG. Normal reports and RAG
// never call it; the two possible topologies are compiled once per Registry.
func (r *Registry) RunEinoWorkflow(ctx context.Context, modules []string, input json.RawMessage) (WorkflowResult, error) {
	routes := []string{"daily-review", "interview-followup"}
	withAlgorithm := false
	for _, module := range modules {
		if module == "algorithm" {
			withAlgorithm = true
			routes = append(routes, "algorithm-diagnosis")
			break
		}
	}
	key := "base"
	if withAlgorithm {
		key = "algorithm"
	}

	r.einoMu.Lock()
	runnable := r.einoRunnables[key]
	compileErr := r.einoErrors[key]
	if runnable == nil && compileErr == nil {
		runnable, compileErr = r.buildEinoWorkflow(routes)
		if compileErr != nil {
			r.einoErrors[key] = compileErr
		} else {
			r.einoRunnables[key] = runnable
		}
	}
	r.einoMu.Unlock()
	if compileErr != nil {
		return WorkflowResult{}, compileErr
	}
	return runnable.Invoke(ctx, einoInput{Modules: modules, Payload: append(json.RawMessage(nil), input...)})
}

func (r *Registry) buildEinoWorkflow(routes []string) (compose.Runnable[einoInput, WorkflowResult], error) {
	graph := compose.NewGraph[einoInput, WorkflowResult]()
	for _, route := range routes {
		route := route
		lambda := compose.InvokableLambda(func(ctx context.Context, in einoInput) (einoAgentOutput, error) {
			started := time.Now()
			response, tools, err := r.RunDetailed(ctx, route, in.Payload)
			output := einoAgentOutput{
				Response: response, Tools: tools, Payload: append(json.RawMessage(nil), in.Payload...),
				Latency: time.Since(started),
			}
			if err != nil {
				output.Error = err.Error()
			}
			return output, nil
		})
		if err := graph.AddLambdaNode(route, lambda, compose.WithOutputKey(route)); err != nil {
			return nil, err
		}
		if err := graph.AddEdge(compose.START, route); err != nil {
			return nil, err
		}
	}

	planner := compose.InvokableLambda(func(ctx context.Context, values map[string]any) (WorkflowResult, error) {
		result := WorkflowResult{
			Routes:  append(append([]string{}, routes...), "weekly-plan"),
			Outputs: map[string]model.ChatResponse{}, Tools: map[string][]ToolExecution{},
			Errors: map[string]string{}, Latencies: map[string]time.Duration{},
		}
		var input json.RawMessage
		agentOutputs := make(map[string]any, len(routes))
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
			if len(input) == 0 {
				input = append(json.RawMessage(nil), agentOutput.Payload...)
			}
			result.Latencies[route] = agentOutput.Latency
			result.Tools[route] = agentOutput.Tools
			if agentOutput.Error != "" {
				result.Errors[route] = agentOutput.Error
				continue
			}
			result.Outputs[route] = agentOutput.Response
			var structured any
			if json.Unmarshal([]byte(agentOutput.Response.Content), &structured) != nil {
				structured = agentOutput.Response.Content
			}
			agentOutputs[route] = structured
		}
		var request any
		if json.Unmarshal(input, &request) != nil {
			request = map[string]any{}
		}
		plannerInput, _ := json.Marshal(map[string]any{
			"request": request, "agent_outputs": agentOutputs, "agent_errors": result.Errors,
		})
		started := time.Now()
		plan, tools, err := r.RunDetailed(ctx, "weekly-plan", plannerInput)
		result.Latencies["weekly-plan"] = time.Since(started)
		if err != nil {
			result.Errors["weekly-plan"] = err.Error()
		} else {
			result.Outputs["weekly-plan"] = plan
			result.Tools["weekly-plan"] = tools
		}
		return result, nil
	})
	if err := graph.AddLambdaNode("planner", planner); err != nil {
		return nil, err
	}
	for _, route := range routes {
		if err := graph.AddEdge(route, "planner"); err != nil {
			return nil, err
		}
	}
	if err := graph.AddEdge("planner", compose.END); err != nil {
		return nil, err
	}
	return graph.Compile(context.Background(), compose.WithGraphName("learnq-multi-agent"))
}
