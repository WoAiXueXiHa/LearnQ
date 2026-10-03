package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDeepSeekStructuredModeAndTruncation(t *testing.T) {
	for _, name := range []string{"deepseek-flash", "other-model"} {
		for _, finish := range []string{"stop", "length"} {
			t.Run(name+"/"+finish, func(t *testing.T) {
				client := &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					thinking, ok := body["thinking"]
					if name == "deepseek-flash" {
						if !ok || thinking.(map[string]any)["type"] != "disabled" {
							t.Fatal("thinking must be disabled")
						}
					} else if ok {
						t.Fatal("provider-specific parameter sent to unrelated model")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{}"},"finish_reason":"` + finish + `"}],"usage":{"prompt_tokens":20,"completion_tokens":30}}`)), Header: make(http.Header)}, nil
				})}
				response, err := (OpenAICompatible{BaseURL: "https://fixture.invalid", ChatModel: name, Client: client}).Generate(context.Background(), ChatRequest{MaxOutputTokens: 100})
				if finish == "length" {
					if err == nil || response.OutputTokens != 30 {
						t.Fatal("truncation must fail and preserve usage")
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestDeepSeekSchemaUsesResponsesAndPreservesIncompleteUsage(t *testing.T) {
	for _, status := range []string{"completed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			client := &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/responses" {
					t.Fatal(r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["reasoning"].(map[string]any)["effort"] != "none" {
					t.Fatal("thinking enabled")
				}
				format := body["text"].(map[string]any)["format"].(map[string]any)
				if format["type"] != "json_schema" || format["schema"] == nil {
					t.Fatal("schema missing")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"` + status + `","output":[{"type":"message","content":[{"type":"output_text","text":"{}"}]}],"usage":{"input_tokens":10,"output_tokens":20}}`)), Header: make(http.Header)}, nil
			})}
			response, err := (OpenAICompatible{BaseURL: "https://fixture.invalid", ChatModel: "deepseek-flash", Client: client}).Generate(context.Background(), ChatRequest{ResponseSchema: []byte(`{"type":"object"}`), MaxOutputTokens: 100})
			if (err != nil) != (status == "incomplete") || response.OutputTokens != 20 {
				t.Fatalf("%+v %v", response, err)
			}
		})
	}
}
