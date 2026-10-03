package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type ImageEvidence struct {
	ID               string    `json:"id"`
	ArticleImageID   uint64    `json:"article_image_id"`
	DocumentID       uint64    `json:"document_id"`
	IndexID          uint64    `json:"index_id"`
	ImageID          uint64    `json:"image_id"`
	Content          string    `json:"content"`
	ContentHash      string    `json:"content_hash"`
	ImageHash        string    `json:"image_hash"`
	DescriptionModel string    `json:"description_model"`
	EmbeddingModel   string    `json:"embedding_model"`
	Dimension        int       `json:"dimension"`
	Status           string    `json:"status"`
	LastError        string    `json:"last_error,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (ImageEvidence) TableName() string { return "image_evidence" }

// ValidateContent verifies the immutable indexed description before consumers
// use it as evidence. Human corrections remain JSON data, never instructions.
func (e ImageEvidence) ValidateContent() error {
	sum := sha256.Sum256([]byte(e.Content))
	if e.Status != "ready" || hex.EncodeToString(sum[:]) != e.ContentHash || !json.Valid([]byte(e.Content)) {
		return errors.New("image evidence is unavailable or content hash mismatched")
	}
	return nil
}
