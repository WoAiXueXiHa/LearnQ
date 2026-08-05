package trace

import (
	"context"
	"encoding/json"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/internal/skill"
	"gorm.io/gorm"
)

type Recorder struct{ DB *gorm.DB }

func (r Recorder) Record(ctx context.Context, taskID uint64, definition skill.Definition, promptHash, input string, response model.ChatResponse, latency time.Duration, executions []skill.ToolExecution, runErr error) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// trace 是旁路数据，5s 独立超时避免它拖累主任务的结果持久化。
	// run、step、tool_calls 在同一事务写入，避免详情页出现只有主记录、缺少工具证据的半条 trace。
	// 输入仅保存截断摘要；完整模型输出与 Prompt 哈希用于复现和版本对比。
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
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("agent_runs").Create(run).Error; err != nil {
			return err
		}
		// 通过 LAST_INSERT_ID 拿自增主键，避免对 Create 返回值做类型断言。
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		step := map[string]any{"agent_run_id": id, "agent_name": definition.TargetAgent, "step_name": definition.Name,
			"output_json": response.Content, "latency_ms": latency.Milliseconds(), "error_reason": reason, "created_at": time.Now().UTC()}
		if err := tx.Table("agent_steps").Create(step).Error; err != nil {
			return err
		}
		for _, execution := range executions {
			// 请求/响应/引用的空值统一补占位 JSON，保证 tool_calls 行字段可反序列化。
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

// truncate 按 rune 截断，避免切断 UTF-8 多字节字符。
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
