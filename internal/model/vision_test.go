package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFakeVisionReturnsStructuredDescription(t *testing.T) {
	response, err := (Fake{}).Describe(context.Background(), VisionRequest{
		Image: []byte("pixels"), MediaType: "image/png", Prompt: "解释架构图",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Model != "learnq-fake-vision-v1" || response.Description.Title == "" ||
		len(response.Description.KeyPoints) == 0 {
		t.Fatalf("response=%#v", response)
	}
}

func TestFakeVisionRejectsUnsupportedMediaPermanently(t *testing.T) {
	_, err := (Fake{}).Describe(context.Background(), VisionRequest{
		Image: []byte("pixels"), MediaType: "image/gif",
	})
	var permanent *PermanentError
	if !errors.As(err, &permanent) {
		t.Fatalf("error=%v", err)
	}
}

func TestOllamaVisionUsesNativeStructuredOutputAndContext(t *testing.T) {
	var captured map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		request["_path"] = r.URL.Path
		captured = request
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
			"model":"qwen3-vl:4b",
			"message":{"role":"assistant","content":"","thinking":"{\"title\":\"架构图\",\"summary\":\"异步任务链路\",\"extracted_text\":\"API -> Worker\",\"key_points\":[\"Outbox\"],\"learning_explanation\":\"先落库再异步执行\",\"uncertainties\":[]}"},
			"done":true,
			"done_reason":"stop",
			"prompt_eval_count":1200,
			"eval_count":80
		}`)),
			Request: r,
		}, nil
	})}

	response, err := (OllamaVision{
		BaseURL: "http://ollama.test/v1", Model: "qwen3-vl:4b",
		ContextLength: 8192, Client: client,
	}).Describe(context.Background(), VisionRequest{
		Image: []byte("pixels"), MediaType: "image/png", Prompt: "解释图片",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Description.Title != "架构图" || response.InputTokens != 1200 {
		t.Fatalf("response=%#v", response)
	}
	if captured["_path"] != "/api/chat" || captured["think"] != false {
		t.Fatalf("request=%#v", captured)
	}
	options, ok := captured["options"].(map[string]any)
	if !ok || options["num_ctx"] != float64(8192) {
		t.Fatalf("options=%#v", captured["options"])
	}
	if _, ok := captured["format"].(map[string]any); !ok {
		t.Fatalf("format=%#v", captured["format"])
	}
}

func TestOpenAIVisionReportsContextExhaustion(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
			"choices":[{"message":{"content":"","reasoning":"thinking"},"finish_reason":"length"}],
			"usage":{"prompt_tokens":4066,"completion_tokens":30}
		}`)),
			Request: r,
		}, nil
	})}

	_, err := (OpenAICompatibleVision{
		BaseURL: "http://vision.test/v1", APIKey: "key", Model: "vision", Client: client,
	}).Describe(context.Background(), VisionRequest{
		Image: []byte("pixels"), MediaType: "image/png",
	})
	var permanent *PermanentError
	if !errors.As(err, &permanent) || !strings.Contains(err.Error(), "context exhausted") {
		t.Fatalf("error=%v", err)
	}
}

func TestDecodeVisionDescriptionNormalizesOptionalLists(t *testing.T) {
	description, err := decodeVisionDescription(
		"```json\n" +
			`{"title":"标题","summary":"摘要","extracted_text":"","learning_explanation":"解释"}` +
			"\n```",
	)
	if err != nil {
		t.Fatal(err)
	}
	if description.KeyPoints == nil || description.Uncertainties == nil {
		t.Fatalf("description=%#v", description)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
