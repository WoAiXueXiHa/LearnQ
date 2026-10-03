package api

import (
	"fmt"
	"strconv"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/articleimage"
	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/gin-gonic/gin"
)

// articleImages exposes pending occurrences as well as snapshots; no network or
// model call is triggered by inspecting a historical article version.
func (s *Server) articleImages(c *gin.Context) {
	documentID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || documentID == 0 {
		fail(c, 422, "VALIDATION_FAILED", "invalid document id", nil)
		return
	}
	indexID, err := strconv.ParseUint(c.Param("index_id"), 10, 64)
	if err != nil || indexID == 0 {
		fail(c, 422, "VALIDATION_FAILED", "invalid index id", nil)
		return
	}
	var version domain.DocumentIndex
	if err := s.store.DB.WithContext(c.Request.Context()).Where("id=? AND document_id=? AND status IN ('active','retired')", indexID, documentID).First(&version).Error; err != nil {
		fail(c, 404, "NOT_FOUND", "article version unavailable", nil)
		return
	}
	rows := []domain.ArticleImage{}
	if err := s.store.DB.WithContext(c.Request.Context()).Where("index_id=? AND document_id=?", indexID, documentID).Order("start_byte,id").Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load article images", nil)
		return
	}
	complete := true
	var indexRows []domain.ImageEvidence
	if err := s.store.DB.WithContext(c.Request.Context()).Where("index_id=?", indexID).Find(&indexRows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load image indexing states", nil)
		return
	}
	indexed := map[uint64]domain.ImageEvidence{}
	for _, row := range indexRows {
		indexed[row.ArticleImageID] = row
	}
	snapshots := map[uint64]domain.Image{}
	ids := []uint64{}
	for j := range rows {
		rows[j].EvidenceURL = fmt.Sprintf("/api/v1/documents/%d/indexes/%d/images/%d", documentID, indexID, rows[j].ID)
	}
	for _, row := range rows {
		if row.ImageID != nil {
			ids = append(ids, *row.ImageID)
		}
	}
	if len(ids) > 0 {
		var images []domain.Image
		if err := s.store.DB.WithContext(c.Request.Context()).Where("id IN ?", ids).Find(&images).Error; err != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not load image snapshots", nil)
			return
		}
		for _, image := range images {
			snapshots[image.ID] = image
		}
	}
	for _, row := range rows {
		ready := row.ImageID != nil && snapshots[*row.ImageID].Status == "ready" && indexed[row.ID].Status == "ready"
		if row.Status != "skipped" && !ready {
			complete = false
		}
	}
	ok(c, 200, gin.H{"index_id": indexID, "images": rows, "snapshots": snapshots, "image_indexes": indexed, "image_evidence_complete": complete})
}

func (s *Server) processArticleImage(c *gin.Context) {
	if s.imageStore == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "image storage unavailable", nil)
		return
	}
	docID, e1 := strconv.ParseUint(c.Param("id"), 10, 64)
	indexID, e2 := strconv.ParseUint(c.Param("index_id"), 10, 64)
	refID, e3 := strconv.ParseUint(c.Param("image_ref_id"), 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || docID == 0 || indexID == 0 || refID == 0 {
		fail(c, 422, "VALIDATION_FAILED", "invalid article image identity", nil)
		return
	}
	ctx := c.Request.Context()
	var row domain.ArticleImage
	if err := s.store.DB.WithContext(ctx).Where("id=? AND index_id=? AND document_id=?", refID, indexID, docID).First(&row).Error; err != nil {
		fail(c, 404, "NOT_FOUND", "image occurrence unavailable", nil)
		return
	}
	var version domain.DocumentIndex
	if err := s.store.DB.WithContext(ctx).Where("id=? AND document_id=? AND status IN ('active','retired')", indexID, docID).First(&version).Error; err != nil {
		fail(c, 409, "INVALID_STATE", "article version unavailable", nil)
		return
	}
	if row.ImageID != nil {
		if err := s.store.DB.WithContext(ctx).Model(&domain.ImageEvidence{}).Where("article_image_id=? AND status='failed'", row.ID).Updates(map[string]any{"status": "pending", "last_error": "", "updated_at": time.Now().UTC()}).Error; err != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not retry image evidence indexing", nil)
			return
		}
		ok(c, 200, row)
		return
	}
	if row.Status != "pending_snapshot" && row.Status != "download_failed" && row.Status != "limit_pending" {
		fail(c, 409, "INVALID_STATE", "image reference cannot be processed", nil)
		return
	}
	image, err := (&articleimage.Processor{Store: s.store, Images: s.imageStore, MaxImages: s.articleMaxImages, DescriptionModel: s.visionModel}).Process(ctx, row)
	if err != nil {
		fail(c, 422, "IMAGE_PROCESS_FAILED", "image processing failed; inspect the occurrence status", nil)
		return
	}
	ok(c, 202, gin.H{"image": image, "image_ref_id": refID})
}

// Explicit skip preserves the reference and snapshot for later inspection.
func (s *Server) skipArticleImage(c *gin.Context) {
	docID, e1 := strconv.ParseUint(c.Param("id"), 10, 64)
	indexID, e2 := strconv.ParseUint(c.Param("index_id"), 10, 64)
	refID, e3 := strconv.ParseUint(c.Param("image_ref_id"), 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || docID == 0 || indexID == 0 || refID == 0 {
		fail(c, 422, "VALIDATION_FAILED", "invalid article image identity", nil)
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || len(input.Reason) == 0 || len(input.Reason) > 1000 {
		fail(c, 422, "VALIDATION_FAILED", "a skip reason of 1 to 1000 bytes is required", nil)
		return
	}
	result := s.store.DB.WithContext(c.Request.Context()).Model(&domain.ArticleImage{}).
		Where("id=? AND document_id=? AND index_id=? AND EXISTS (SELECT 1 FROM document_indexes d WHERE d.id=? AND d.status IN ('active','retired'))", refID, docID, indexID, indexID).
		Updates(map[string]any{"status": "skipped", "last_error": input.Reason, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not skip article image", nil)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 404, "NOT_FOUND", "image occurrence unavailable", nil)
		return
	}
	ok(c, 200, gin.H{"image_ref_id": refID, "status": "skipped"})
}
