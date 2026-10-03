package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strconv"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
	"github.com/gin-gonic/gin"
)

func verifyArticleImage(version domain.DocumentIndex, row domain.ArticleImage) error {
	if row.IndexID != version.ID || row.DocumentID != version.DocumentID {
		return errors.New("image source identity mismatch")
	}
	if err := validateSourceHash(version.Content, version.ContentHash); err != nil {
		return err
	}
	parsed, err := rag.ParseMarkdown(version.Content)
	if err != nil {
		return err
	}
	for _, ref := range parsed.ImageRefs {
		if ref.StartByte == row.StartByte && ref.EndByte == row.EndByte && ref.StartLine == row.StartLine && ref.EndLine == row.EndLine && ref.URL == row.OriginalURL && ref.Kind == row.SyntaxKind {
			return nil
		}
	}
	return errors.New("image occurrence does not match immutable source")
}

func (s *Server) articleImageEvidence(c *gin.Context) {
	docID, e1 := strconv.ParseUint(c.Param("id"), 10, 64)
	indexID, e2 := strconv.ParseUint(c.Param("index_id"), 10, 64)
	refID, e3 := strconv.ParseUint(c.Param("image_ref_id"), 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || docID == 0 || indexID == 0 || refID == 0 {
		fail(c, 422, "VALIDATION_FAILED", "invalid image evidence identity", nil)
		return
	}
	ctx := c.Request.Context()
	var document domain.Document
	if lookupFailed(c, s.store.DB.WithContext(ctx).Where("id=? AND status<>'deleting'", docID).First(&document).Error, "could not load article") {
		return
	}
	var version domain.DocumentIndex
	if lookupFailed(c, s.store.DB.WithContext(ctx).Where("id=? AND document_id=? AND status IN ('active','retired')", indexID, docID).First(&version).Error, "could not load article version") {
		return
	}
	var row domain.ArticleImage
	if lookupFailed(c, s.store.DB.WithContext(ctx).Where("id=? AND index_id=? AND document_id=?", refID, indexID, docID).First(&row).Error, "could not load image occurrence") {
		return
	}
	if err := verifyArticleImage(version, row); err != nil {
		fail(c, 410, "EVIDENCE_INVALID", err.Error(), nil)
		return
	}
	if row.ImageID == nil {
		ok(c, 200, gin.H{"occurrence": row, "article_sha256": version.ContentHash, "snapshot_available": false})
		return
	}
	var snapshot domain.Image
	if lookupFailed(c, s.store.DB.WithContext(ctx).First(&snapshot, *row.ImageID).Error, "could not load image snapshot") {
		return
	}
	if s.imageStore == nil {
		fail(c, 503, "DEPENDENCY_UNAVAILABLE", "image storage unavailable", nil)
		return
	}
	body, err := s.imageStore.Read(snapshot.StoragePath)
	if err != nil {
		fail(c, 410, "EVIDENCE_INVALID", "image snapshot missing", nil)
		return
	}
	hash := sha256.Sum256(body)
	if hex.EncodeToString(hash[:]) != snapshot.ContentHash {
		fail(c, 410, "EVIDENCE_INVALID", "image snapshot hash mismatch", nil)
		return
	}
	var description *model.ImageDescription
	if snapshot.Status == "ready" {
		description = &model.ImageDescription{}
		if err := json.Unmarshal([]byte(snapshot.DescriptionJSON), description); err != nil {
			fail(c, 410, "EVIDENCE_INVALID", "invalid persisted description", nil)
			return
		}
	}
	effective, effectiveModel := snapshot.DescriptionJSON, snapshot.DescriptionModel
	var frozen domain.ImageEvidence
	err = s.store.DB.WithContext(ctx).Where("article_image_id=? AND status='ready'", row.ID).First(&frozen).Error
	if err == nil {
		if frozen.DocumentID != docID || frozen.IndexID != indexID || frozen.ImageID != snapshot.ID || frozen.ImageHash != snapshot.ContentHash || frozen.ValidateContent() != nil {
			fail(c, 410, "EVIDENCE_INVALID", "indexed image description hash mismatch", nil)
			return
		}
		effective, effectiveModel = frozen.Content, frozen.DescriptionModel
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 500, "INTERNAL_ERROR", "could not read indexed image description", nil)
		return
	}
	corrections := []store.ImageDescriptionCorrection{}
	if err := s.store.DB.WithContext(ctx).Where("document_id=? AND image_hash=?", docID, snapshot.ContentHash).Order("id").Find(&corrections).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load correction history", nil)
		return
	}
	ok(c, 200, gin.H{"document_name": document.Filename, "effective_description": effective, "effective_description_model": effectiveModel, "corrections": corrections, "occurrence": row, "article_sha256": version.ContentHash, "snapshot_available": true, "image_sha256": snapshot.ContentHash, "snapshot": snapshot, "description": description, "description_model": snapshot.DescriptionModel, "content_url": fmt.Sprintf("/api/v1/images/%d/content", snapshot.ID), "source_excerpt": version.Content[row.StartByte:row.EndByte]})
}
