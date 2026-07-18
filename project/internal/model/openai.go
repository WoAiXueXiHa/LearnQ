package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type DependencyError struct {
	Status    int
	Code      string
	Retryable bool
}

func (e *DependencyError) Error() string {
	return fmt.Sprintf("AI dependency error (%s)", e.Code)
}

func classifyStatus(status int) *DependencyError {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &DependencyError{Status: status, Code: "AI_AUTH_FAILED"}
	case status == http.StatusTooManyRequests:
		return &DependencyError{Status: status, Code: "AI_RATE_LIMITED", Retryable: true}
	case status == http.StatusRequestTimeout:
		return &DependencyError{Status: status, Code: "AI_TIMEOUT", Retryable: true}
	case status >= 500:
		return &DependencyError{Status: status, Code: "AI_UPSTREAM_UNAVAILABLE", Retryable: true}
	default:
		return &DependencyError{Status: status, Code: "AI_REQUEST_REJECTED"}
	}
}

// OpenAICompatible keeps Real mode behind the same narrow interfaces as Fake.
// Eino/Compose orchestration can wrap this adapter without leaking SDK types
// into LearnQ's task, skill, or workflow packages.
type OpenAICompatible struct {
	BaseURL        string
	APIKey         string
	ChatModel      string
	EmbeddingModel string
	Dimension      int
	Client         *http.Client
}

func (m OpenAICompatible) Generate(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body := map[string]any{"model": m.ChatModel, "messages": []map[string]string{
		{"role": "system", "content": req.Prompt},
		{"role": "user", "content": string(req.Input)},
	}, "response_format": map[string]string{"type": "json_object"}}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := m.call(ctx, "/chat/completions", body, &response); err != nil {
		return ChatResponse{}, err
	}
	if len(response.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("chat response has no choices")
	}
	if strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return ChatResponse{}, fmt.Errorf("chat response content is empty")
	}
	return ChatResponse{Content: response.Choices[0].Message.Content, InputTokens: response.Usage.Prompt, OutputTokens: response.Usage.Completion, Model: m.ChatModel}, nil
}

func (m OpenAICompatible) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body := map[string]any{"model": m.EmbeddingModel, "input": texts}
	if m.Dimension > 0 {
		body["dimensions"] = m.Dimension
	}
	var response struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := m.call(ctx, "/embeddings", body, &response); err != nil {
		return nil, err
	}
	if len(response.Data) != len(texts) {
		return nil, fmt.Errorf("embedding response count mismatch: got %d want %d", len(response.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	seen := make([]bool, len(texts))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(out) || seen[item.Index] ||
			len(item.Embedding) == 0 || (m.Dimension > 0 && len(item.Embedding) != m.Dimension) {
			return nil, fmt.Errorf("EMBEDDING_DIMENSION_MISMATCH")
		}
		seen[item.Index] = true
		out[item.Index] = item.Embedding
	}
	for index, present := range seen {
		if !present {
			return nil, fmt.Errorf("embedding response missing index %d", index)
		}
	}
	return out, nil
}

func (m OpenAICompatible) call(ctx context.Context, path string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode AI request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+m.APIKey)
	client := m.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &DependencyError{Code: "AI_TIMEOUT", Retryable: true}
		}
		return &DependencyError{Code: "AI_NETWORK_ERROR", Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return classifyStatus(response.StatusCode)
	}
	const maxResponse = 4 << 20
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("read AI response: %w", err)
	}
	if len(responseBody) > maxResponse {
		return fmt.Errorf("AI response exceeds 4 MiB")
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("decode AI response: %w", err)
	}
	return nil
}
