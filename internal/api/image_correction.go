package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) correctImageDescription(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64000)
	var input struct {
		ImageRefID  uint64                 `json:"image_ref_id"`
		Description model.ImageDescription `json:"description"`
		Comment     string                 `json:"comment"`
	}
	if c.ShouldBindJSON(&input) != nil || input.ImageRefID == 0 || strings.TrimSpace(input.Comment) == "" || len(input.Comment) > 8000 || strings.TrimSpace(input.Description.Summary) == "" {
		fail(c, 422, "VALIDATION_FAILED", "provide a description and correction rationale", nil)
		return
	}
	body, err := json.Marshal(input.Description)
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", "invalid description", nil)
		return
	}
	var row store.ImageDescriptionCorrection
	err = s.store.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var document domain.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status IN ('ready','archived')", id).First(&document).Error; err != nil {
			return err
		}
		var ref domain.ArticleImage
		if err := tx.Where("id=? AND document_id=? AND image_id IS NOT NULL", input.ImageRefID, id).First(&ref).Error; err != nil {
			return err
		}
		var image domain.Image
		if err := tx.Where("id=? AND status='ready'", *ref.ImageID).First(&image).Error; err != nil {
			return err
		}
		row = store.ImageDescriptionCorrection{DocumentID: id, ImageHash: image.ContentHash, DescriptionJSON: string(body), Comment: input.Comment, CreatedAt: time.Now().UTC()}
		return tx.Create(&row).Error
	})
	if err != nil {
		fail(c, 409, "IMAGE_CORRECTION_REJECTED", "ready image and retained article required", nil)
		return
	}
	ok(c, 201, gin.H{"correction": row, "requires_article_reindex": true, "historical_evidence_unchanged": true})
}
