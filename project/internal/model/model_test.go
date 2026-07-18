package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFakeFailureInjection(t *testing.T) {
	if _, err := (Fake{Failure: "temporary"}).Generate(context.Background(), ChatRequest{}); err == nil {
		t.Fatal("temporary error not injected")
	}
	response, err := (Fake{Failure: "invalid_json"}).Generate(context.Background(), ChatRequest{})
	if err != nil || response.Content != "{" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestFakeHonorsContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := (Fake{Delay: time.Second}).Generate(ctx, ChatRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestFakeDailyReviewUsesReadableFactsWithoutInternalToolJSON(t *testing.T) {
	response, err := (Fake{}).Generate(context.Background(), ChatRequest{
		Skill: "daily-review",
		Input: []byte(`{"title":"Go 并发","summary":"学习 channel","duration_minutes":30,"modules":[{"category":"backend","content":"理解关闭语义"}],"_tool_results":{"weekly_stats":{"records":1}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Go 并发", "学习 channel", "30 分钟", "理解关闭语义", "面试追问"} {
		if !strings.Contains(response.Content, expected) {
			t.Fatalf("missing %q in %s", expected, response.Content)
		}
	}
	if strings.Contains(response.Content, "_tool_results") {
		t.Fatalf("internal tool JSON leaked: %s", response.Content)
	}
}

func TestFakeRAGAnswerUsesProvidedCitation(t *testing.T) {
	response, err := (Fake{}).Generate(context.Background(), ChatRequest{
		Skill: "rag-answer",
		Input: []byte(`{"question":"如何恢复任务？","evidence":[{"source":"S1","content":"Reaper 从 MySQL 扫描过期 lease。"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Reaper", "[S1]"} {
		if !strings.Contains(response.Content, expected) {
			t.Fatalf("missing %q in %s", expected, response.Content)
		}
	}
}

func TestMarkdownRequiresStructuredFields(t *testing.T) {
	if _, err := MarkdownFromJSON(`{"title":"x"}`); err == nil {
		t.Fatal("missing required fields accepted")
	}
	if _, err := MarkdownFromJSON(`{"title":"x","summary":"y","sections":[{"heading":"h","content":"c"}]}`); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAICompatibleParsesUsageAndHonorsClientTimeout(t *testing.T) {
	client := &http.Client{Transport: modelRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/chat/completions" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		return &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"choices":[{"message":{"content":"{\"title\":\"x\",\"summary\":\"y\",\"sections\":[]}"}}],"usage":{"prompt_tokens":12,"completion_tokens":7}}`,
			)),
		}, nil
	}), Timeout: time.Second}
	model := OpenAICompatible{BaseURL: "http://ai.test", APIKey: "test", ChatModel: "chat", Client: client}
	response, err := model.Generate(context.Background(), ChatRequest{Prompt: "p", Input: []byte(`{}`)})
	if err != nil || response.InputTokens != 12 || response.OutputTokens != 7 {
		t.Fatalf("response=%#v err=%v", response, err)
	}

	timeoutClient := &http.Client{Transport: modelRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}), Timeout: 5 * time.Millisecond}
	model.Client = timeoutClient
	_, err = model.Generate(context.Background(), ChatRequest{})
	var timeoutErr *DependencyError
	if !errors.As(err, &timeoutErr) || timeoutErr.Code != "AI_TIMEOUT" || !timeoutErr.Retryable {
		t.Fatalf("HTTP client timeout error = %#v", err)
	}
}

func TestOpenAICompatibleEmbeddingValidatesRequestAndCompleteResponse(t *testing.T) {
	client := &http.Client{Transport: modelRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/embeddings" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer embedding-key" {
			t.Fatalf("authorization=%q", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, expected := range []string{`"model":"text-embedding-3-small"`, `"dimensions":2`} {
			if !strings.Contains(string(body), expected) {
				t.Fatalf("missing %s in %s", expected, body)
			}
		}
		return &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"index":1,"embedding":[0.3,0.4]},{"index":0,"embedding":[0.1,0.2]}]}`)),
		}, nil
	})}
	model := OpenAICompatible{
		BaseURL: "https://api.openai.com/v1", APIKey: "embedding-key",
		EmbeddingModel: "text-embedding-3-small", Dimension: 2, Client: client,
	}
	vectors, err := model.Embed(context.Background(), []string{"a", "b"})
	if err != nil || len(vectors) != 2 || vectors[0][0] != 0.1 || vectors[1][0] != 0.3 {
		t.Fatalf("vectors=%v err=%v", vectors, err)
	}
}

func TestOpenAICompatibleRejectsIncompleteEmbeddingAndSanitizesErrors(t *testing.T) {
	incomplete := &http.Client{Transport: modelRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)),
		}, nil
	})}
	model := OpenAICompatible{BaseURL: "http://embedding.test", APIKey: "secret", EmbeddingModel: "embedding", Dimension: 2, Client: incomplete}
	if _, err := model.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("incomplete embedding response accepted")
	}

	unauthorized := &http.Client{Transport: modelRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 401,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"secret provider detail"}`)),
		}, nil
	})}
	model.Client = unauthorized
	_, err := model.Embed(context.Background(), []string{"a"})
	var dependencyErr *DependencyError
	if !errors.As(err, &dependencyErr) || dependencyErr.Code != "AI_AUTH_FAILED" || dependencyErr.Retryable {
		t.Fatalf("err=%#v", err)
	}
	if strings.Contains(err.Error(), "secret provider detail") {
		t.Fatalf("provider body leaked: %v", err)
	}
}

type modelRoundTripFunc func(*http.Request) (*http.Response, error)

func (f modelRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
