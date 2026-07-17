package trace

import (
	"encoding/json"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/skill"
	"gorm.io/gorm"
)

type Recorder struct{ DB *gorm.DB }

func (r Recorder) Record(taskID uint64, definition skill.Definition, promptHash, input string, response model.ChatResponse, latency time.Duration, executions []skill.ToolExecution, runErr error) (uint64, error) {
	reason := ""
	if runErr != nil {
		reason = runErr.Error()
	}
	run := map[string]any{
		"task_id": taskID, "skill_name": definition.Name, "skill_version": definition.Version,
		"prompt_version": definition.PromptVersion, "prompt_hash": promptHash, "schema_version": definition.SchemaVersion,
		"model_name": response.Model, "input_summary": truncate(input, 500), "output_json": response.Content,
		"input_tokens": response.InputTokens, "output_tokens": response.OutputTokens, "latency_ms": latency.Milliseconds(),
		"error_reason": reason, "created_at": time.Now().UTC(),
	}
	var id uint64
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("agent_runs").Create(run).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		step := map[string]any{"agent_run_id": id, "agent_name": definition.TargetAgent, "step_name": definition.Name,
			"output_json": response.Content, "latency_ms": latency.Milliseconds(), "error_reason": reason, "created_at": time.Now().UTC()}
		if err := tx.Table("agent_steps").Create(step).Error; err != nil {
			return err
		}
		for _, execution := range executions {
			request := execution.Request
			if len(request) == 0 {
				request, _ = json.Marshal(map[string]any{"input_summary": truncate(input, 200)})
			}
			responseBody := execution.Response
			if len(responseBody) == 0 {
				responseBody = json.RawMessage("{}")
			}
			citations := execution.Citations
			if len(citations) == 0 {
				citations = json.RawMessage("[]")
			}
			call := map[string]any{"agent_run_id": id, "tool_name": execution.Name, "request_json": string(request), "response_json": string(responseBody),
				"retrieval_citations_json": string(citations), "latency_ms": execution.Latency.Milliseconds(), "error_reason": execution.Error, "created_at": time.Now().UTC()}
			if err := tx.Table("tool_calls").Create(call).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
