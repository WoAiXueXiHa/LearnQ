package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PracticeModelResult struct {
	TaskID              uint64 `gorm:"primaryKey"`
	ExecutionGeneration int    `gorm:"primaryKey"`
	InputHash           string
	ResponseJSON        string
	ResponseHash        string
	CreatedAt           time.Time
}

func (s *Store) CachedPracticeResult(ctx context.Context, task domain.AITask, inputHash string) (model.ChatResponse, bool, error) {
	var row PracticeModelResult
	var response model.ChatResponse
	err := s.DB.WithContext(ctx).Where("task_id=? AND execution_generation=?", task.ID, task.ExecutionGeneration).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return response, false, nil
	}
	if err != nil {
		return response, false, err
	}
	response, err = row.decode(inputHash)
	if err != nil {
		return response, false, err
	}
	return response, true, nil
}

func (row PracticeModelResult) decode(inputHash string) (model.ChatResponse, error) {
	var response model.ChatResponse
	sum := sha256.Sum256([]byte(row.ResponseJSON))
	if row.InputHash != inputHash || row.ResponseHash != hex.EncodeToString(sum[:]) || json.Unmarshal([]byte(row.ResponseJSON), &response) != nil {
		return response, invalidResult("cached model input or response hash mismatch")
	}
	return response, nil
}

func (s *Store) SavePracticeResult(ctx context.Context, task domain.AITask, token, inputHash string, response model.ChatResponse) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(body) > 512*1024 {
		return invalidResult("model response exceeds cache limit")
	}
	sum := sha256.Sum256(body)
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current domain.AITask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='processing' AND execution_generation=? AND lease_token=? AND lease_until>?", task.ID, task.ExecutionGeneration, token, time.Now().UTC()).First(&current).Error; err != nil {
			return err
		}
		row := PracticeModelResult{TaskID: task.ID, ExecutionGeneration: task.ExecutionGeneration, InputHash: inputHash, ResponseJSON: string(body), ResponseHash: hex.EncodeToString(sum[:]), CreatedAt: time.Now().UTC()}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	})
}
