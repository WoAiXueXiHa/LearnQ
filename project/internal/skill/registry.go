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
	// 版本与哈希会写入 trace，使一次模型输出可以还原到具体 Skill、Prompt 和 Schema。
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Description   string   `json:"description"`
	PromptVersion string   `json:"prompt_version"`
	SchemaVersion string   `json:"schema_version"`
	TargetAgent   string   `json:"target_agent"`
	AllowedTools  []string `json:"allowed_tools"`
}

type Registry struct {
	// AllowedTools 是每个 Skill 的显式能力白名单；模型只消费工具结果，不自行选择任意函数。
	definitions  map[string]Definition
	model        model.ChatModel
	inputSchema  *jsonschema.Schema
	outputSchema *jsonschema.Schema
	outputJSON   json.RawMessage
	tools        map[string]ToolFunc
}

// ToolExecution 记录一次工具调用的完整现场（请求、结果、引用、耗时、错误），
// 随任务写入 trace 供审计与问题排查。
type ToolExecution struct {
	Name      string          `json:"name"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
	Citations json.RawMessage `json:"citations"`
	Latency   time.Duration   `json:"-"`
	Error     string          `json:"error,omitempty"`
}

// ToolFunc 是工具实现签名：入参为输入 JSON，返回结果与引用片段、错误。
type ToolFunc func(context.Context, json.RawMessage) (json.RawMessage, json.RawMessage, error)

// New 构建注册表并编译输入/输出 Schema；outputJSON 原文随后随请求发给模型作为格式约束。
func New(chat model.ChatModel) *Registry {
	// Skill 元数据集中注册，Prompt/Schema 则编译进二进制，部署时不会依赖外部文件漂移。
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
	r.outputJSON, _ = assets.ReadFile("schemas/common-output.json")
	return r
}

// RegisterTool 注册工具实现；运行时缺失的工具不会中断 Skill，
// 而是以 unavailable 状态进入执行记录（见 RunDetailed）。
func (r *Registry) RegisterTool(name string, tool ToolFunc) { r.tools[name] = tool }

// compileSchema 编译内嵌 JSON Schema，失败即 panic：
// Schema 属于构建期契约，启动期暴露错误优于运行期悄悄放过非法输出。
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

// List 按固定顺序返回全部 Skill 定义，并附加 multi-agent 手动实验项（不进异步报告主链）。
func (r *Registry) List() []Definition {
	order := []string{"daily-review", "algorithm-diagnosis", "interview-followup", "project-explanation", "weekly-plan"}
	out := make([]Definition, 0, len(order)+1)
	for _, name := range order {
		out = append(out, r.definitions[name])
	}
	out = append(out, Definition{
		Name: "multi-agent", Version: "1.0.0",
		Description:   "Multi-Agent 手动实验（不参与异步报告主链）",
		PromptVersion: "workflow-v1", SchemaVersion: "v1", TargetAgent: "Experiment",
	})
	return out
}

// Get 按名称查询 Skill 定义，未注册时返回 false。
func (r *Registry) Get(name string) (Definition, bool) {
	def, ok := r.definitions[name]
	return def, ok
}

// PromptHash 返回 prompt 文件内容的 SHA-256，用于 trace 中还原生成时的提示词版本。
func (r *Registry) PromptHash(name string) (string, error) {
	body, err := assets.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// Run 是 RunDetailed 的简化入口：只返回生成结果，丢弃工具执行明细。
func (r *Registry) Run(ctx context.Context, name string, input json.RawMessage) (model.ChatResponse, error) {
	response, _, err := r.RunDetailed(ctx, name, input)
	return response, err
}

// RunDetailed 返回生成响应与工具执行记录；executions 供调用方写入 trace。
func (r *Registry) RunDetailed(ctx context.Context, name string, input json.RawMessage) (model.ChatResponse, []ToolExecution, error) {
	// 输入先过 JSON Schema，再执行白名单工具；模型输出再次过 Schema。
	// 这把不可信模型限制在“生成结构化内容”，路由、权限和状态写入仍由 Go 控制。
	if !json.Valid(input) {
		return model.ChatResponse{}, nil, errors.New("skill input must be valid JSON")
	}
	var inputValue any
	if err := json.Unmarshal(input, &inputValue); err != nil {
		return model.ChatResponse{}, nil, fmt.Errorf("skill input is not valid JSON: %w", err)
	}
	if err := r.inputSchema.Validate(inputValue); err != nil {
		return model.ChatResponse{}, nil, fmt.Errorf("skill input does not match JSON Schema: %w", err)
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
		// 工具失败作为结构化上下文交给模型，同时保留 execution 供审计；
		// 单个辅助工具不可用不必直接中断整个 Skill。
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
	// 工具结果整体注入模型输入：模型只消费结果，不参与工具选择。
	enriched["_tool_results"] = toolResults
	modelInput, _ := json.Marshal(enriched)
	// append(nil, ...) 是防御性复制，避免请求体与注册表共享同一底层数组。
	response, err := r.model.Generate(ctx, model.ChatRequest{
		Skill:          name,
		Prompt:         string(prompt) + "\n\n输出要求：只提炼与任务相关的事实；不得复制工具原始 JSON，不得输出 _tool_results 或其他内部字段名。",
		Input:          modelInput,
		ResponseSchema: append(json.RawMessage(nil), r.outputJSON...),
	})
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
