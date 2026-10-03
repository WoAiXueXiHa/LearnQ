package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
)

func (p *Pool) WithModelCacheIdentity(identity string) { p.modelCacheIdentity = identity }

func (p *Pool) generatePractice(ctx context.Context, task domain.AITask, token string, request model.ChatRequest) (model.ChatResponse, error) {
	if p.modelCacheIdentity == "" {
		return p.model.Generate(ctx, request)
	}
	body, err := json.Marshal([]any{"practice-result-v1", p.modelCacheIdentity, task.Kind, request})
	if err != nil {
		return model.ChatResponse{}, err
	}
	sum := sha256.Sum256(body)
	key := hex.EncodeToString(sum[:])
	if response, found, err := p.store.CachedPracticeResult(ctx, task, key); err != nil {
		return response, classifyPracticeResultError(err)
	} else if found {
		return response, nil
	}
	response, err := p.model.Generate(ctx, request)
	if err != nil {
		return response, err
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.store.SavePracticeResult(persistCtx, task, token, key, response); err != nil {
		return response, classifyPracticeResultError(err)
	}
	return response, nil
}
