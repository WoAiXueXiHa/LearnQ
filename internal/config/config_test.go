package config

import (
	"strings"
	"testing"
	"time"
)

func validFakeConfig() Config {
	return Config{
		AIMode:        "fake",
		EmbeddingDim:  64,
		TaskTimeout:   45 * time.Second,
		LeaseDuration: 60 * time.Second,
	}
}

func TestValidateAIMode(t *testing.T) {
	if err := validFakeConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	unknown := validFakeConfig()
	unknown.AIMode = "unknown"
	if err := unknown.Validate(); err == nil {
		t.Fatal("unknown AI mode accepted")
	}
	real := validFakeConfig()
	real.AIMode = "real"
	if err := real.Validate(); err == nil || !strings.Contains(err.Error(), "AI_CHAT_API_KEY") {
		t.Fatalf("missing real configuration err=%v", err)
	}
	real.AIChatAPIKey = "chat-key"
	real.AIChatBaseURL = "https://api.deepseek.com"
	real.AIChatModel = "chat"
	real.AIEmbeddingAPIKey = "embedding-key"
	real.AIEmbeddingBaseURL = "https://api.openai.com/v1"
	real.AIEmbeddingModel = "embedding"
	real.AIVisionProvider = "ollama"
	real.AIVisionBaseURL = "http://127.0.0.1:11434/v1"
	real.AIVisionModel = "qwen3-vl:4b"
	real.AIVisionContext = 8192
	if err := real.Validate(); err != nil {
		t.Fatal(err)
	}
	apiOnly := real
	apiOnly.AIVisionBaseURL = ""
	apiOnly.AIVisionModel = ""
	apiOnly.AIVisionProvider = ""
	if err := apiOnly.ValidateAPI(); err != nil {
		t.Fatalf("API validation should not require vision: %v", err)
	}
	real.AIEmbeddingBaseURL = "not-a-url"
	if err := real.Validate(); err == nil || !strings.Contains(err.Error(), "AI_EMBEDDING_BASE_URL") {
		t.Fatalf("invalid URL accepted err=%v", err)
	}
	real.AIEmbeddingBaseURL = "https://api.openai.com/v1"
	real.AIVisionProvider = "openai"
	real.AIVisionAPIKey = ""
	real.AIVisionContext = 0
	if err := real.Validate(); err == nil || !strings.Contains(err.Error(), "AI_VISION_API_KEY") {
		t.Fatalf("missing openai vision key accepted err=%v", err)
	}
	invalidDimension := validFakeConfig()
	invalidDimension.EmbeddingDim = 0
	if err := invalidDimension.Validate(); err == nil {
		t.Fatal("invalid embedding dimension accepted")
	}
}

func TestValidateTaskAndLeaseDurations(t *testing.T) {
	tests := []struct {
		name          string
		taskTimeout   time.Duration
		leaseDuration time.Duration
	}{
		{name: "both zero"},
		{name: "task timeout zero", leaseDuration: time.Minute},
		{name: "lease duration zero", taskTimeout: 45 * time.Second},
		{name: "equal", taskTimeout: time.Minute, leaseDuration: time.Minute},
		{name: "lease shorter", taskTimeout: time.Minute, leaseDuration: 30 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validFakeConfig()
			cfg.TaskTimeout = tc.taskTimeout
			cfg.LeaseDuration = tc.leaseDuration
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid task and lease durations accepted")
			}
		})
	}
}

func TestLoadRAGCollectionIsolation(t *testing.T) {
	t.Setenv("RAG_COLLECTION", "")
	t.Setenv("AI_EMBEDDING_MODEL", "qwen3-embedding:0.6b")
	t.Setenv("AI_MODE", "fake")
	if got := Load().RAGCollection; got != "learnq_chunks_fake_v1" {
		t.Fatalf("fake collection=%q", got)
	}

	t.Setenv("AI_MODE", "real")
	if got := Load().RAGCollection; got != "learnq_chunks_real_qwen3_embedding_0_6b" {
		t.Fatalf("real collection=%q", got)
	}

	t.Setenv("RAG_COLLECTION", "learnq_custom")
	if got := Load().RAGCollection; got != "learnq_custom" {
		t.Fatalf("custom collection=%q", got)
	}
}

func TestLoadUsesQwenEmbeddingDimensionByDefault(t *testing.T) {
	t.Setenv("EMBEDDING_DIM", "")
	if got := Load().EmbeddingDim; got != 1024 {
		t.Fatalf("EmbeddingDim=%d, want 1024 for qwen3-embedding:0.6b", got)
	}
}
