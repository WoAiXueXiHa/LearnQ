package store

import (
	"context"
	"errors"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"gorm.io/gorm"
)

type ImageDescriptionCorrection struct {
	ID              uint64    `json:"id"`
	DocumentID      uint64    `json:"document_id"`
	ImageHash       string    `json:"image_hash"`
	DescriptionJSON string    `json:"description_json"`
	Comment         string    `json:"comment"`
	CreatedAt       time.Time `json:"created_at"`
}

// EffectiveImageDescription freezes human edits at article-version creation.
// Original vision output is never overwritten or shared across articles.
func EffectiveImageDescription(ctx context.Context, db *gorm.DB, version domain.DocumentIndex, image domain.Image) (string, string, error) {
	var correction ImageDescriptionCorrection
	err := db.WithContext(ctx).Where("document_id=? AND image_hash=? AND created_at<=?", version.DocumentID, image.ContentHash, version.CreatedAt).Order("created_at DESC,id DESC").First(&correction).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return image.DescriptionJSON, image.DescriptionModel, nil
	}
	if err != nil {
		return "", "", err
	}
	return correction.DescriptionJSON, "user-correction", nil
}
