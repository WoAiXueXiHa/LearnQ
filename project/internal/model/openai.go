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

// DependencyError 统一 AI 依赖错误：携带 HTTP 状态、稳定错误码与可重试标记。
// Worker 依据 Retryable 决定任务进入 retry_wait 还是直接 dead。
type DependencyError struct {
	Status    int
	Code      string
	Retryable bool
	Err       error
}

func (e *DependencyError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("AI dependency error (%s): %v", e.Code, e.Err)
	}
	return fmt.Sprintf("AI dependency error (%s)", e.Code)
}

// Unwrap 暴露底层错误，使调用方可用 errors.Is/As 穿透 DependencyError。
func (e *DependencyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func classifyStatus(status int) *DependencyError {
	// 只重试限流、超时和上游 5xx；鉴权/参数类 4xx 直接终止，防止无意义重放。
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

// OpenAICompatible 把 real 模式封装在与 Fake 相同的窄接口后。
// Eino/Compose 只看到项目接口，供应商协议不会泄漏到任务、Skill 或工作流包。
type OpenAICompatible struct {
	BaseURL        string
	APIKey         string
	ChatModel      string
	EmbeddingModel string
	Dimension      int
	Client         *http.Client
}

// Generate 构造 OpenAI 兼容 /chat/completions 请求：JSON Schema 拼入 system prompt
// 并请求 json_object 响应格式；空 choices 或空内容视为上游异常。
func (m OpenAICompatible) Generate(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	systemPrompt := strings.TrimSpace(req.Prompt)
	if len(req.ResponseSchema) > 0 {
		systemPrompt += "\n\nReturn only one JSON object that matches this JSON Schema exactly:\n" + string(req.ResponseSchema)
	}
	body := map[string]any{"model": m.ChatModel, "messages": []map[string]string{
		{"role": "system", "content": systemPrompt},
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
	// 防御上游返回空 choices（如内容策略截断）：宁可报错走重试，也不产出空报告。
	if len(response.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("chat response has no choices")
	}
	if strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return ChatResponse{}, fmt.Errorf("chat response content is empty")
	}
	return ChatResponse{Content: response.Choices[0].Message.Content, InputTokens: response.Usage.Prompt, OutputTokens: response.Usage.Completion, Model: m.ChatModel}, nil
}

// Embed 批量向量化；配置了 Dimension 时显式向供应商请求维度，
// 并校验返回数量、顺序与维度，防止向量漂移破坏 Qdrant collection 一致性。
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
		// 供应商可能乱序返回 embedding，按 index 复原输入顺序并拒绝重复、缺失或维度漂移。
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

// call 是 Chat/Embedding 共用的 HTTP 封装：超时与网络错误映射为可重试的
// DependencyError，非 2xx 交给 classifyStatus 分类，成功响应体上限 4 MiB。
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
		// 超时判定双路径兜底：部分 HTTP 客户端返回的 err 未包装 deadline 错误，
		// 此时 ctx.Err() 仍能区分"超时"与"网络故障"两类不同原因。
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &DependencyError{Code: "AI_TIMEOUT", Retryable: true}
		}
		return &DependencyError{Code: "AI_NETWORK_ERROR", Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		// 限量读取并丢弃错误体，使连接可被复用；4 KiB 上限避免被异常大错误页拖住。
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return classifyStatus(response.StatusCode)
	}
	const maxResponse = 4 << 20
	// 限制响应体，避免异常上游用超大 JSON 持续占用 Worker 内存。
	// 多读 1 字节用于判定超限：恰满 4 MiB 的合法响应不应被误判为超限。
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
