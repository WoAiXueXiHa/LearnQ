package bootstrap

import (
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/config"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
)

func TestEmbeddingModelNameMatchesSelectedProvider(t *testing.T) {
	for _, mode := range []string{"fake", "real", ""} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Config{AIMode: mode, AIEmbeddingModel: "configured-real-model"}
			_, embedding := Models(cfg)
			_, fake := embedding.(*model.Fake)
			want := cfg.AIEmbeddingModel
			if fake {
				want = "learnq-fake-embedding-v1"
			}
			if got := EmbeddingModelName(cfg); got != want {
				t.Fatalf("selected provider %T: model name = %q, want %q", embedding, got, want)
			}
		})
	}
}
