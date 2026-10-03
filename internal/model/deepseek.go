package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DeepSeek supports schema-constrained output on its Responses endpoint. Schema
// validation and evidence checks in the worker/store remain mandatory.
func (m OpenAICompatible) generateDeepSeekStructured(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body := map[string]any{"model": m.ChatModel, "input": []map[string]string{{"role": "system", "content": req.Prompt}, {"role": "user", "content": string(req.Input)}}, "reasoning": map[string]string{"effort": "none"}, "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "practice_questions", "schema": json.RawMessage(req.ResponseSchema)}}}
	if req.MaxOutputTokens > 0 {
		body["max_output_tokens"] = req.MaxOutputTokens
	}
	var result struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := m.call(ctx, "/responses", body, &result); err != nil {
		return ChatResponse{}, err
	}
	response := ChatResponse{Model: m.ChatModel, InputTokens: result.Usage.Input, OutputTokens: result.Usage.Output}
	if result.Status != "completed" {
		return response, &DependencyError{Code: "AI_OUTPUT_INCOMPLETE"}
	}
	for _, item := range result.Output {
		if item.Type == "message" {
			for _, part := range item.Content {
				if part.Type == "output_text" {
					response.Content += part.Text
				}
			}
		}
	}
	if strings.TrimSpace(response.Content) == "" {
		return response, fmt.Errorf("structured response content is empty")
	}
	return response, nil
}
