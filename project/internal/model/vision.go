package model

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ImageDescription 是图片理解的固定输出契约，JSON 字段名与模型输出约定一致，
// 必填项校验规则见 validateDescription。
type ImageDescription struct {
	Title               string   `json:"title"`
	Summary             string   `json:"summary"`
	ExtractedText       string   `json:"extracted_text"`
	KeyPoints           []string `json:"key_points"`
	LearningExplanation string   `json:"learning_explanation"`
	Uncertainties       []string `json:"uncertainties"`
}

type VisionRequest struct {
	Image     []byte
	MediaType string
	Prompt    string
}

type VisionResponse struct {
	Description  ImageDescription
	InputTokens  int
	OutputTokens int
	Model        string
}

// VisionModel 抽象图片描述能力，提供 Fake / OpenAI 兼容 / Ollama 三种实现。
type VisionModel interface {
	Describe(context.Context, VisionRequest) (VisionResponse, error)
}

// PermanentError 标记"图片本身不可识别"的永久失败（空图、格式不支持、上下文耗尽），
// 与临时故障区分：Worker 收到后直接将任务置为 dead，不再进入 retry_wait 重试。
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string {
	if e == nil || e.Err == nil {
		return "permanent task error"
	}
	return e.Err.Error()
}

func (e *PermanentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Permanent 把错误包装为永久失败；nil 输入返回 nil，保证包装操作可安全串联。
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// Describe 在 fake 模式下不做真实识别，但保留与 real 相同的格式与媒体类型校验，
// 使上传、任务、Fencing 持久化全链路在无模型环境下可验证。
func (f Fake) Describe(ctx context.Context, req VisionRequest) (VisionResponse, error) {
	select {
	case <-ctx.Done():
		return VisionResponse{}, ctx.Err()
	default:
	}
	// 空图与不支持的类型是输入本身的问题，重试不会改变结果，标记永久失败。
	if len(req.Image) == 0 {
		return VisionResponse{}, Permanent(errors.New("image is empty"))
	}
	if req.MediaType != "image/jpeg" && req.MediaType != "image/png" {
		return VisionResponse{}, Permanent(errors.New("unsupported image media type"))
	}
	topic := strings.TrimSpace(req.Prompt)
	if topic == "" {
		topic = "这张学习图片"
	}
	return VisionResponse{
		Description: ImageDescription{
			Title:               "图片学习资料",
			Summary:             "Fake Vision 已验证图片理解异步链路。",
			ExtractedText:       "",
			KeyPoints:           []string{"图片已通过格式与大小校验", "描述结果通过 Lease/Fencing 持久化"},
			LearningExplanation: "请在 Real 模式下使用支持视觉输入的模型分析：" + topic,
			Uncertainties:       []string{"Fake 模式不识别真实像素内容"},
		},
		InputTokens: len(req.Image) / 1024, OutputTokens: 64, Model: "learnq-fake-vision-v1",
	}, nil
}

type OpenAICompatibleVision struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

type OllamaVision struct {
	BaseURL       string
	Model         string
	ContextLength int
	Client        *http.Client
}

// Describe 走 OpenAI 兼容多模态接口：图片以 base64 data URL 内联进 user 消息，
// 输出要求以文本形式附带 JSON Schema 并请求 json_object 响应格式。
func (m OpenAICompatibleVision) Describe(ctx context.Context, req VisionRequest) (VisionResponse, error) {
	if len(req.Image) == 0 {
		return VisionResponse{}, Permanent(errors.New("image is empty"))
	}
	if req.MediaType != "image/jpeg" && req.MediaType != "image/png" {
		return VisionResponse{}, Permanent(errors.New("unsupported image media type"))
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "请描述这张学习图片，提取可见文字、关键知识点，并用适合学习复盘的方式解释；不确定的内容必须明确列出。"
	}
	schema := `{"title":"string","summary":"string","extracted_text":"string","key_points":["string"],"learning_explanation":"string","uncertainties":["string"]}`
	dataURL := "data:" + req.MediaType + ";base64," + base64.StdEncoding.EncodeToString(req.Image)
	body := map[string]any{
		"model": m.Model,
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": prompt + "\n只返回一个 JSON 对象，结构必须严格匹配：" + schema},
				map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURL}},
			},
		}},
		"response_format": map[string]string{"type": "json_object"},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return VisionResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.BaseURL, "/")+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return VisionResponse{}, err
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
			return VisionResponse{}, &DependencyError{Code: "AI_TIMEOUT", Retryable: true, Err: err}
		}
		return VisionResponse{}, &DependencyError{Code: "AI_NETWORK_ERROR", Retryable: true, Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return VisionResponse{}, classifyStatus(response.StatusCode)
	}
	var output struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&output); err != nil {
		return VisionResponse{}, fmt.Errorf("decode vision response: %w", err)
	}
	if len(output.Choices) == 0 {
		return VisionResponse{}, errors.New("vision response has no choices")
	}
	choice := output.Choices[0]
	if strings.TrimSpace(choice.Message.Content) == "" {
		err := fmt.Errorf(
			"vision response content is empty (finish_reason=%s, prompt_tokens=%d, completion_tokens=%d)",
			choice.FinishReason, output.Usage.Prompt, output.Usage.Completion,
		)
		// 上下文耗尽（AI_VISION_CONTEXT_LENGTH 配置不足）重试必然复现，归为永久失败。
		if choice.FinishReason == "length" {
			return VisionResponse{}, Permanent(fmt.Errorf("vision context exhausted: %w", err))
		}
		return VisionResponse{}, err
	}
	description, err := decodeVisionDescription(choice.Message.Content)
	if err != nil {
		return VisionResponse{}, err
	}
	return VisionResponse{
		Description: description, InputTokens: output.Usage.Prompt,
		OutputTokens: output.Usage.Completion, Model: m.Model,
	}, nil
}

// Describe 走 Ollama 原生 /api/chat：format 直接传 JSON Schema 强制结构化输出，
// think=false 与 temperature=0 抑制推理与随机性；未配置上下文长度时兜底 8192。
func (m OllamaVision) Describe(ctx context.Context, req VisionRequest) (VisionResponse, error) {
	if len(req.Image) == 0 {
		return VisionResponse{}, Permanent(errors.New("image is empty"))
	}
	if req.MediaType != "image/jpeg" && req.MediaType != "image/png" {
		return VisionResponse{}, Permanent(errors.New("unsupported image media type"))
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "请描述这张学习图片，提取可见文字、关键知识点，并用适合学习复盘的方式解释；不确定的内容必须明确列出。"
	}
	contextLength := m.ContextLength
	if contextLength <= 0 {
		contextLength = 8192
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"title", "summary", "extracted_text", "key_points",
			"learning_explanation", "uncertainties",
		},
		"properties": map[string]any{
			"title":                map[string]string{"type": "string"},
			"summary":              map[string]string{"type": "string"},
			"extracted_text":       map[string]string{"type": "string"},
			"key_points":           map[string]any{"type": "array", "items": map[string]string{"type": "string"}},
			"learning_explanation": map[string]string{"type": "string"},
			"uncertainties":        map[string]any{"type": "array", "items": map[string]string{"type": "string"}},
		},
	}
	body := map[string]any{
		"model":  m.Model,
		"stream": false,
		"think":  false,
		"format": schema,
		"options": map[string]any{
			"num_ctx":     contextLength,
			"temperature": 0,
		},
		"messages": []any{map[string]any{
			"role":    "user",
			"content": prompt + "\n请严格填写结构化结果，不要省略任何字段。",
			"images":  []string{base64.StdEncoding.EncodeToString(req.Image)},
		}},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return VisionResponse{}, err
	}
	// Ollama 原生 API 路径为 /api/chat：先剥掉 OpenAI 兼容前缀 /v1 再拼接。
	baseURL := strings.TrimRight(m.BaseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, baseURL+"/api/chat", bytes.NewReader(encoded),
	)
	if err != nil {
		return VisionResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := m.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return VisionResponse{}, &DependencyError{Code: "AI_TIMEOUT", Retryable: true, Err: err}
		}
		return VisionResponse{}, &DependencyError{Code: "AI_NETWORK_ERROR", Retryable: true, Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return VisionResponse{}, classifyStatus(response.StatusCode)
	}
	var output struct {
		Model   string `json:"model"`
		Message struct {
			Content  string `json:"content"`
			Thinking string `json:"thinking"`
		} `json:"message"`
		DoneReason      string `json:"done_reason"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&output); err != nil {
		return VisionResponse{}, fmt.Errorf("decode ollama vision response: %w", err)
	}
	content := strings.TrimSpace(output.Message.Content)
	// Qwen3-VL on Ollama may place schema-constrained JSON in `thinking` even
	// when think=false. Accept it only after the normal content is empty and
	// the response completed; decodeVisionDescription still enforces the schema.
	if content == "" && output.DoneReason == "stop" {
		content = strings.TrimSpace(output.Message.Thinking)
	}
	if content == "" {
		err := fmt.Errorf(
			"ollama vision content is empty (done_reason=%s, prompt_tokens=%d, completion_tokens=%d)",
			output.DoneReason, output.PromptEvalCount, output.EvalCount,
		)
		if output.DoneReason == "length" {
			return VisionResponse{}, Permanent(fmt.Errorf("vision context exhausted: %w", err))
		}
		return VisionResponse{}, err
	}
	description, err := decodeVisionDescription(content)
	if err != nil {
		return VisionResponse{}, err
	}
	if output.Model == "" {
		output.Model = m.Model
	}
	return VisionResponse{
		Description: description, InputTokens: output.PromptEvalCount,
		OutputTokens: output.EvalCount, Model: output.Model,
	}, nil
}

// decodeVisionDescription 容错解析模型输出：先剥掉 Markdown 代码围栏，
// 再截取首个 { 到最后一个 } 之间的内容（模型常夹带解释文本），最后按 Schema 校验。
func decodeVisionDescription(content string) (ImageDescription, error) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		if newline := strings.IndexByte(content, '\n'); newline >= 0 {
			content = content[newline+1:]
		}
		content = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(content), "```"))
	}
	start, end := strings.IndexByte(content, '{'), strings.LastIndexByte(content, '}')
	if start < 0 || end < start {
		return ImageDescription{}, errors.New("decode vision description: response has no JSON object")
	}
	var description ImageDescription
	if err := json.Unmarshal([]byte(content[start:end+1]), &description); err != nil {
		return ImageDescription{}, fmt.Errorf("decode vision description: %w", err)
	}
	if err := validateDescription(&description); err != nil {
		return ImageDescription{}, err
	}
	return description, nil
}

// validateDescription 校验标题/摘要/学习解释必填，并把 nil 切片归一为空数组，
// 保证下游序列化输出 [] 而不是 null。
func validateDescription(value *ImageDescription) error {
	if value == nil {
		return errors.New("vision description is missing")
	}
	if strings.TrimSpace(value.Title) == "" || strings.TrimSpace(value.Summary) == "" ||
		strings.TrimSpace(value.LearningExplanation) == "" {
		return errors.New("vision description is missing required fields")
	}
	if value.KeyPoints == nil {
		value.KeyPoints = []string{}
	}
	if value.Uncertainties == nil {
		value.Uncertainties = []string{}
	}
	return nil
}
