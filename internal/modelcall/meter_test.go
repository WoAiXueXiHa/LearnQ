package modelcall

import (
	"context"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
)

type forbiddenProvider struct{ t *testing.T }

func (f forbiddenProvider) Generate(context.Context, model.ChatRequest) (model.ChatResponse, error) {
	f.t.Fatal("provider called with zero budget")
	return model.ChatResponse{}, nil
}
func (f forbiddenProvider) Embed(context.Context, []string) ([][]float32, error) {
	f.t.Fatal("provider called with zero budget")
	return nil, nil
}
func (f forbiddenProvider) Describe(context.Context, model.VisionRequest) (model.VisionResponse, error) {
	f.t.Fatal("provider called with zero budget")
	return model.VisionResponse{}, nil
}

func TestZeroBudgetBlocksAllProvidersBeforeDatabaseOrNetwork(t *testing.T) {
	for _, limits := range [][2]int64{{0, 0}, {100, 0}, {0, 100}, {-1, 10}, {10, -1}} {
		meter := &Meter{Mode: "real", DailyMicroCNY: limits[0], CallMicroCNY: limits[1]}
		provider := forbiddenProvider{t: t}
		if _, err := (Chat{Meter: meter, Inner: provider}).Generate(context.Background(), model.ChatRequest{}); err == nil {
			t.Fatal("text call allowed")
		}
		if _, err := (Embedding{Meter: meter, Inner: provider}).Embed(context.Background(), []string{"test"}); err == nil {
			t.Fatal("embedding call allowed")
		}
		if _, err := (Vision{Meter: meter, Inner: provider}).Describe(context.Background(), model.VisionRequest{}); err == nil {
			t.Fatal("vision call allowed")
		}
	}
}
