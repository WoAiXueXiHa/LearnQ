package config

import (
	"strings"
	"testing"
)

func TestValidateAIMode(t *testing.T) {
	if err := (Config{AIMode: "fake", EmbeddingDim: 64}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Config{AIMode: "unknown", EmbeddingDim: 64}).Validate(); err == nil {
		t.Fatal("unknown AI mode accepted")
	}
	real := Config{AIMode: "real", EmbeddingDim: 64}
	if err := real.Validate(); err == nil || !strings.Contains(err.Error(), "AI_CHAT_API_KEY") {
		t.Fatalf("missing real configuration err=%v", err)
	}
	real.AIChatAPIKey = "chat-key"
	real.AIChatBaseURL = "https://api.deepseek.com"
	real.AIChatModel = "chat"
	real.AIEmbeddingAPIKey = "embedding-key"
	real.AIEmbeddingBaseURL = "https://api.openai.com/v1"
	real.AIEmbeddingModel = "embedding"
	if err := real.Validate(); err != nil {
		t.Fatal(err)
	}
	real.AIEmbeddingBaseURL = "not-a-url"
	if err := real.Validate(); err == nil || !strings.Contains(err.Error(), "AI_EMBEDDING_BASE_URL") {
		t.Fatalf("invalid URL accepted err=%v", err)
	}
	if err := (Config{AIMode: "fake", EmbeddingDim: 0}).Validate(); err == nil {
		t.Fatal("invalid embedding dimension accepted")
	}
}
