package skill

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed prompts/*.md schemas/*.json
var assets embed.FS

type Definition struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Description   string   `json:"description"`
	PromptVersion string   `json:"prompt_version"`
	SchemaVersion string   `json:"schema_version"`
	TargetAgent   string   `json:"target_agent"`
	AllowedTools  []string `json:"allowed_tools"`
}

type Registry struct {
	definitions  map[string]Definition
	model        model.ChatModel
	inputSchema  *jsonschema.Schema
	outputSchema *jsonschema.Schema
	tools        map[string]ToolFunc
}

type ToolExecution struct {
	Name      string          `json:"name"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
	Citations json.RawMessage `json:"citations"`
	Latency   time.Duration   `json:"-"`
	Error     string          `json:"error,omitempty"`
}

type ToolFunc func(context.Context, json.RawMessage) (json.RawMessage, json.RawMessage, error)

func New(chat model.ChatModel) *Registry {
	defs := []Definition{
		{"daily-review", "1.0.0", "生成每日学习复盘", "v1", "v1", "Review", []string{"weekly_stats"}},
		{"algorithm-diagnosis", "1.0.0", "诊断算法学习记录", "v1", "v1", "Algorithm", []string{"rag_query"}},
		{"interview-followup", "1.0.0", "生成面试追问", "v1", "v1", "Interview", []string{"rag_query"}},
		{"project-explanation", "1.0.0", "组织项目讲解", "v1", "v1", "Interview", []string{"rag_query"}},
		{"weekly-plan", "1.0.0", "解释 SQL 周统计并组织计划", "v1", "v1", "Planner", []string{"weekly_stats"}},
	}
	r := &Registry{definitions: make(map[string]Definition), model: chat, tools: make(map[string]ToolFunc)}
	for _, def := range defs {
		r.definitions[def.Name] = def
	}
	r.inputSchema = compileSchema("schemas/common-input.json")
	r.outputSchema = compileSchema("schemas/common-output.json")
	return r
}

func (r *Registry) RegisterTool(name string, tool ToolFunc) { r.tools[name] = tool }

func compileSchema(name string) *jsonschema.Schema {
	body, err := assets.ReadFile(name)
	if err != nil {
		panic(err)
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		panic(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(name, document); err != nil {
		panic(err)
	}
	schema, err := compiler.Compile(name)
	if err != nil {
		panic(err)
	}
	return schema
}

func (r *Registry) List() []Definition {
	order := []string{"daily-review", "algorithm-diagnosis", "interview-followup", "project-explanation", "weekly-plan"}
	out := make([]Definition, 0, len(order))
	for _, name := range order {
		out = append(out, r.definitions[name])
	}
	return out
}

func (r *Registry) Get(name string) (Definition, bool) {
	def, ok := r.definitions[name]
	return def, ok
}

func (r *Registry) PromptHash(name string) (string, error) {
	body, err := assets.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (r *Registry) Run(ctx context.Context, name string, input json.RawMessage) (model.ChatResponse, error) {
	response, _, err := r.RunDetailed(ctx, name, input)
	return response, err
}

func (r *Registry) RunDetailed(ctx context.Context, name string, input json.RawMessage) (model.ChatResponse, []ToolExecution, error) {
	if !json.Valid(input) {
		return model.ChatResponse{}, nil, errors.New("skill input must be valid JSON")
	}
	var inputValue any
	if err := json.Unmarshal(input, &inputValue); err != nil || r.inputSchema.Validate(inputValue) != nil {
		return model.ChatResponse{}, nil, errors.New("skill input does not match JSON Schema")
	}
	definition, ok := r.Get(name)
	if !ok {
		return model.ChatResponse{}, nil, fmt.Errorf("unknown skill %q", name)
	}
	prompt, err := assets.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return model.ChatResponse{}, nil, err
	}
	executions := make([]ToolExecution, 0, len(definition.AllowedTools))
	toolResults := map[string]any{}
	for _, toolName := range definition.AllowedTools {
		execution := ToolExecution{Name: toolName, Request: append(json.RawMessage(nil), input...), Citations: json.RawMessage("[]")}
		started := time.Now()
		tool, exists := r.tools[toolName]
		if !exists {
			execution.Error = "tool is not configured"
			execution.Response = json.RawMessage(`{"status":"unavailable"}`)
		} else {
			execution.Response, execution.Citations, err = tool(ctx, input)
			if err != nil {
				execution.Error = err.Error()
				execution.Response, _ = json.Marshal(map[string]any{"status": "failed", "error": err.Error()})
			}
		}
		execution.Latency = time.Since(started)
		var value any
		_ = json.Unmarshal(execution.Response, &value)
		toolResults[toolName] = value
		executions = append(executions, execution)
	}
	var enriched map[string]any
	_ = json.Unmarshal(input, &enriched)
	enriched["_tool_results"] = toolResults
	modelInput, _ := json.Marshal(enriched)
	response, err := r.model.Generate(ctx, model.ChatRequest{Skill: name, Prompt: string(prompt), Input: modelInput})
	if err != nil {
		return response, executions, err
	}
	var outputValue any
	if err := json.Unmarshal([]byte(response.Content), &outputValue); err != nil {
		return model.ChatResponse{}, executions, fmt.Errorf("model output is not valid JSON: %w", err)
	}
	if err := r.outputSchema.Validate(outputValue); err != nil {
		return model.ChatResponse{}, executions, fmt.Errorf("model output violates JSON Schema: %w", err)
	}
	return response, executions, nil
}
