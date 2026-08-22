package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/skill"
	"gorm.io/gorm"
)

const (
	ModeAuto               = "auto"
	TaskKnowledgeQA        = "knowledge_qa"
	TaskLearningReview     = "learning_review"
	TaskProjectExplanation = "project_explanation"
	maxPlanSteps           = 4
	maxToolCalls           = 4
)

var citationPattern = regexp.MustCompile(`\[S([0-9]+)\]`)

type PlanStep struct {
	ID      int            `json:"id"`
	Purpose string         `json:"purpose"`
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args"`
}

type Plan struct {
	TaskType string     `json:"task_type"`
	Intent   string     `json:"intent"`
	Steps    []PlanStep `json:"steps"`
}

type ToolCall struct {
	Name      string          `json:"name"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
	Citations json.RawMessage `json:"citations"`
	LatencyMS int64           `json:"latency_ms"`
	Status    string          `json:"status"`
	Error     string          `json:"error,omitempty"`
}

type SelfCheck struct {
	CitationValid    bool     `json:"citation_valid"`
	Grounded         bool     `json:"grounded"`
	ActionPolicyPass bool     `json:"action_policy_passed"`
	Warnings         []string `json:"warnings,omitempty"`
}

type RunRequest struct {
	Message      string         `json:"message"`
	Mode         string         `json:"mode"`
	AllowActions bool           `json:"allow_actions"`
	Context      map[string]any `json:"context"`
}

type Result struct {
	RunID     uint64     `json:"run_id"`
	TaskType  string     `json:"task_type"`
	Status    string     `json:"status"`
	Plan      Plan       `json:"plan"`
	ToolCalls []ToolCall `json:"tool_calls"`
	Evidence  []any      `json:"evidence"`
	Answer    string     `json:"answer"`
	SelfCheck SelfCheck  `json:"self_check"`
	Model     string     `json:"model"`
}

type Runtime struct {
	DB    *gorm.DB
	Model model.ChatModel
	Tools map[string]skill.ToolFunc
}

func New(db *gorm.DB, chat model.ChatModel) *Runtime {
	return &Runtime{DB: db, Model: chat, Tools: make(map[string]skill.ToolFunc)}
}

func (r *Runtime) RegisterTool(name string, tool skill.ToolFunc) { r.Tools[name] = tool }

func (r *Runtime) Run(ctx context.Context, request RunRequest) (Result, error) {
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" {
		return Result{}, errors.New("message is required")
	}
	if request.Mode == "" {
		request.Mode = ModeAuto
	}
	started := time.Now()
	plan, err := r.plan(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if len(plan.Steps) > maxPlanSteps {
		return Result{}, errors.New("agent plan exceeds step limit")
	}
	var calls []ToolCall
	var observations []map[string]any
	for _, step := range plan.Steps {
		if len(calls) >= maxToolCalls {
			return Result{}, errors.New("agent tool call limit exceeded")
		}
		if !allowedTool(plan.TaskType, step.Tool) {
			return Result{}, fmt.Errorf("tool %q is not allowed for task %q", step.Tool, plan.TaskType)
		}
		args := step.Args
		if args == nil {
			args = map[string]any{}
		}
		if step.Tool == "review_task_create" {
			if !request.AllowActions {
				call := ToolCall{Name: step.Tool, Request: mustJSON(args), Response: mustJSON(map[string]any{"status": "blocked", "reason": "action permission is disabled"}), Status: "blocked"}
				calls = append(calls, call)
				observations = append(observations, map[string]any{"tool": step.Tool, "result": map[string]any{"status": "blocked"}})
				continue
			}
			if request.Context != nil && args["report_id"] == nil {
				args["report_id"] = request.Context["report_id"]
			}
		}
		if err := validateToolArgs(step.Tool, args); err != nil {
			return Result{}, err
		}
		input := mustJSON(args)
		call := ToolCall{Name: step.Tool, Request: input, Status: "succeeded"}
		tool, ok := r.Tools[step.Tool]
		if !ok {
			call.Status = "failed"
			call.Error = "tool is not configured"
			call.Response = mustJSON(map[string]any{"status": "unavailable"})
		} else {
			toolStarted := time.Now()
			response, citations, toolErr := tool(ctx, input)
			call.LatencyMS = time.Since(toolStarted).Milliseconds()
			call.Response, call.Citations = response, citations
			if toolErr != nil {
				call.Status = "failed"
				call.Error = toolErr.Error()
				call.Response = mustJSON(map[string]any{"status": "failed", "error": toolErr.Error()})
			}
		}
		if len(call.Response) == 0 {
			call.Response = json.RawMessage("{}")
		}
		if len(call.Citations) == 0 {
			call.Citations = json.RawMessage("[]")
		}
		calls = append(calls, call)
		var observation any
		_ = json.Unmarshal(call.Response, &observation)
		observations = append(observations, map[string]any{"tool": step.Tool, "result": observation, "citations": call.Citations})
	}
	answer, responseModel, err := r.synthesize(ctx, request, plan, observations)
	if err != nil {
		return Result{}, err
	}
	check := checkAnswer(plan.TaskType, answer, observations)
	if plan.TaskType == TaskKnowledgeQA && !check.CitationValid {
		answer = "依据不足，当前知识库没有可核验内容。"
	}
	result := Result{TaskType: plan.TaskType, Status: "succeeded", Plan: plan, ToolCalls: calls, Answer: answer, SelfCheck: check, Model: responseModel}
	result.Evidence = collectEvidence(observations)
	runID, err := r.persist(ctx, request, result, started)
	if err != nil {
		return Result{}, err
	}
	result.RunID = runID
	return result, nil
}

func (r *Runtime) plan(ctx context.Context, request RunRequest) (Plan, error) {
	input := mustJSON(map[string]any{"message": request.Message, "mode": request.Mode, "allow_actions": request.AllowActions, "context": request.Context})
	response, err := r.Model.Generate(ctx, model.ChatRequest{
		Skill:          "agent-planner",
		Prompt:         `你是 LearnQ 任务规划器。将目标归类为 knowledge_qa、learning_review 或 project_explanation，并从允许工具中选择最少必要工具。只返回 JSON：{"task_type":"...","intent":"...","steps":[{"id":1,"purpose":"...","tool":"...","args":{}}]}。最多 4 步。允许工具：rag_search、study_history_search、weekly_stats、review_task_create。`,
		Input:          input,
		ResponseSchema: json.RawMessage(`{"type":"object","required":["task_type","intent","steps"],"additionalProperties":false}`),
	})
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	if err := json.Unmarshal([]byte(response.Content), &plan); err != nil {
		return Plan{}, fmt.Errorf("planner returned invalid JSON: %w", err)
	}
	if plan.TaskType == "" {
		plan.TaskType = inferTask(request.Message)
	}
	if !validTask(plan.TaskType) {
		return Plan{}, fmt.Errorf("planner returned unsupported task type %q", plan.TaskType)
	}
	for index := range plan.Steps {
		if plan.Steps[index].ID == 0 {
			plan.Steps[index].ID = index + 1
		}
	}
	return plan, nil
}

func (r *Runtime) synthesize(ctx context.Context, request RunRequest, plan Plan, observations []map[string]any) (string, string, error) {
	input := mustJSON(map[string]any{"message": request.Message, "task_type": plan.TaskType, "plan": plan, "observations": observations})
	response, err := r.Model.Generate(ctx, model.ChatRequest{
		Skill:          "agent-synthesizer",
		Prompt:         `你是 LearnQ 回答生成器。只能根据 observations 中的事实回答。知识问答的事实必须使用 [S1] 格式引用；没有证据就明确说依据不足。项目讲解不得编造 QPS、P99、用户量或生产规模。只返回 JSON：{"answer":"..."}。`,
		Input:          input,
		ResponseSchema: json.RawMessage(`{"type":"object","required":["answer"],"additionalProperties":false}`),
	})
	if err != nil {
		return "", "", err
	}
	var output struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(response.Content), &output); err != nil || strings.TrimSpace(output.Answer) == "" {
		return "", "", errors.New("synthesizer returned invalid answer")
	}
	return output.Answer, response.Model, nil
}

func (r *Runtime) persist(ctx context.Context, request RunRequest, result Result, started time.Time) (uint64, error) {
	planJSON, checkJSON := mustJSON(result.Plan), mustJSON(result.SelfCheck)
	outputJSON := mustJSON(map[string]any{"answer": result.Answer, "evidence": result.Evidence})
	sum := sha256.Sum256([]byte(request.Message))
	var id uint64
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := map[string]any{
			"task_id": nil, "workflow_id": hex.EncodeToString(sum[:])[:32], "skill_name": "agent-runtime",
			"skill_version": "1.0.0", "prompt_version": "planner+synthesizer-v1", "prompt_hash": hex.EncodeToString(sum[:]),
			"schema_version": "agent-v1", "model_name": result.Model, "input_summary": request.Message,
			"output_json": string(outputJSON), "input_tokens": 0, "output_tokens": 0,
			"latency_ms": time.Since(started).Milliseconds(), "error_reason": "", "created_at": time.Now().UTC(),
			"task_type": result.TaskType, "status": result.Status, "plan_json": string(planJSON),
			"self_check_json": string(checkJSON), "completed_at": time.Now().UTC(),
		}
		if err := tx.Table("agent_runs").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		steps := []map[string]any{
			{"agent_run_id": id, "agent_name": "Planner", "step_name": "plan", "output_json": string(planJSON), "latency_ms": 0, "error_reason": "", "created_at": time.Now().UTC()},
			{"agent_run_id": id, "agent_name": "Synthesizer", "step_name": "answer", "output_json": string(outputJSON), "latency_ms": 0, "error_reason": "", "created_at": time.Now().UTC()},
			{"agent_run_id": id, "agent_name": "Guardrail", "step_name": "self_check", "output_json": string(checkJSON), "latency_ms": 0, "error_reason": "", "created_at": time.Now().UTC()},
		}
		for _, step := range steps {
			if err := tx.Table("agent_steps").Create(step).Error; err != nil {
				return err
			}
		}
		for _, call := range result.ToolCalls {
			row := map[string]any{"agent_run_id": id, "tool_name": call.Name, "request_json": string(call.Request), "response_json": string(call.Response), "retrieval_citations_json": string(call.Citations), "latency_ms": call.LatencyMS, "error_reason": call.Error, "created_at": time.Now().UTC()}
			if err := tx.Table("tool_calls").Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

func allowedTool(taskType, tool string) bool {
	switch taskType {
	case TaskKnowledgeQA:
		return tool == "rag_search"
	case TaskLearningReview:
		return tool == "study_history_search" || tool == "weekly_stats" || tool == "review_task_create"
	case TaskProjectExplanation:
		return tool == "rag_search" || tool == "study_history_search"
	default:
		return false
	}
}

func validTask(value string) bool {
	return value == TaskKnowledgeQA || value == TaskLearningReview || value == TaskProjectExplanation
}

func validateToolArgs(tool string, args map[string]any) error {
	switch tool {
	case "rag_search":
		if err := rejectUnknownArgs(tool, args, map[string]bool{"question": true, "topic": true, "summary": true, "title": true, "top_k": true}); err != nil {
			return err
		}
		query := ""
		for _, key := range []string{"question", "topic", "summary", "title"} {
			if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
				query = strings.TrimSpace(value)
				break
			}
		}
		if query == "" {
			return errors.New("rag_search requires a non-empty question, topic, summary or title")
		}
		if topK, exists, err := intArg(args, "top_k"); err != nil {
			return err
		} else if exists && (topK < 1 || topK > 20) {
			return errors.New("rag_search.top_k must be between 1 and 20")
		}
	case "study_history_search":
		if err := rejectUnknownArgs(tool, args, map[string]bool{"query": true, "limit": true}); err != nil {
			return err
		}
		query, _ := args["query"].(string)
		if strings.TrimSpace(query) == "" {
			return errors.New("study_history_search.query is required")
		}
		if limit, exists, err := intArg(args, "limit"); err != nil {
			return err
		} else if exists && (limit < 1 || limit > 10) {
			return errors.New("study_history_search.limit must be between 1 and 10")
		}
	case "weekly_stats":
		if len(args) != 0 {
			return errors.New("weekly_stats does not accept arguments")
		}
	case "review_task_create":
		if err := rejectUnknownArgs(tool, args, map[string]bool{"report_id": true}); err != nil {
			return err
		}
		reportID, exists, err := intArg(args, "report_id")
		if err != nil {
			return err
		}
		if !exists || reportID < 1 {
			return errors.New("review_task_create.report_id must be a positive integer")
		}
	default:
		return fmt.Errorf("tool %q is not configured for argument validation", tool)
	}
	return nil
}

func rejectUnknownArgs(tool string, args map[string]any, allowed map[string]bool) error {
	for key := range args {
		if !allowed[key] {
			return fmt.Errorf("%s contains unsupported argument %q", tool, key)
		}
	}
	return nil
}

func intArg(args map[string]any, key string) (int, bool, error) {
	value, exists := args[key]
	if !exists || value == nil {
		return 0, false, nil
	}
	switch typed := value.(type) {
	case int:
		return typed, true, nil
	case int64:
		return int(typed), true, nil
	case uint64:
		maxInt := int(^uint(0) >> 1)
		if typed > uint64(maxInt) {
			return 0, true, fmt.Errorf("%s is too large", key)
		}
		return int(typed), true, nil
	case float64:
		integer := int(typed)
		if typed != float64(integer) {
			return 0, true, fmt.Errorf("%s must be an integer", key)
		}
		return integer, true, nil
	default:
		return 0, true, fmt.Errorf("%s must be an integer", key)
	}
}

func inferTask(message string) string {
	if strings.Contains(message, "复盘") || strings.Contains(message, "学习") || strings.Contains(message, "复习") {
		return TaskLearningReview
	}
	if strings.Contains(message, "项目") || strings.Contains(message, "面试") || strings.Contains(message, "讲解") {
		return TaskProjectExplanation
	}
	return TaskKnowledgeQA
}

func checkAnswer(taskType, answer string, observations []map[string]any) SelfCheck {
	check := SelfCheck{CitationValid: true, Grounded: true, ActionPolicyPass: true}
	for _, observation := range observations {
		result, _ := observation["result"].(map[string]any)
		if result["status"] == "blocked" {
			check.ActionPolicyPass = false
			check.Warnings = append(check.Warnings, "写操作权限未通过：本次没有执行状态变更")
			break
		}
	}
	if taskType == TaskKnowledgeQA || taskType == TaskProjectExplanation {
		candidates := len(collectEvidence(observations))
		refs := citationPattern.FindAllStringSubmatch(answer, -1)
		if candidates == 0 {
			check.CitationValid, check.Grounded = false, false
			check.Warnings = append(check.Warnings, "证据不足：本次执行没有可引用证据")
		}
		if len(refs) == 0 && candidates > 0 {
			check.CitationValid = false
			check.Warnings = append(check.Warnings, "引用格式无效：回答没有引用本次证据")
		}
		for _, ref := range refs {
			index, _ := strconv.Atoi(ref[1])
			if index < 1 || index > candidates {
				check.CitationValid = false
				check.Warnings = append(check.Warnings, "引用格式无效：回答引用了本次证据列表之外的来源")
				break
			}
		}
	}
	return check
}

func collectEvidence(observations []map[string]any) []any {
	var evidence []any
	for _, observation := range observations {
		raw, ok := observation["citations"].(json.RawMessage)
		if !ok || string(raw) == "[]" {
			continue
		}
		var values []any
		if json.Unmarshal(raw, &values) == nil {
			evidence = append(evidence, values...)
		}
	}
	return evidence
}

func mustJSON(value any) json.RawMessage { body, _ := json.Marshal(value); return body }
