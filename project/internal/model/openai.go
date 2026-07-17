package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

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
		Usage struct{ Prompt, Completion int } `json:"usage"`
	}
	if err := m.call(ctx, "/chat/completions", body, &response); err != nil {
		return ChatResponse{}, err
	}
	if len(response.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("chat response has no choices")
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
	out := make([][]float32, len(texts))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(out) || (m.Dimension > 0 && len(item.Embedding) != m.Dimension) {
			return nil, fmt.Errorf("EMBEDDING_DIMENSION_MISMATCH")
		}
		out[item.Index] = item.Embedding
	}
	return out, nil
}

func (m OpenAICompatible) call(ctx context.Context, path string, input, output any) error {
	body, _ := json.Marshal(input)
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
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("AI dependency returned %d: %s", response.StatusCode, message)
	}
	return json.NewDecoder(response.Body).Decode(output)
}
