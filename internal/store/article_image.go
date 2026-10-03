package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AttachArticleSnapshot commits the snapshot, task and occurrence together.
// Locking the version serializes content-hash deduplication within an article.
func (s *Store) AttachArticleSnapshot(ctx context.Context, occurrenceID uint64, input domain.Image, descriptionModel string, maxImages int, validateCache func(domain.Image) error) (domain.Image, bool, error) {
	var result domain.Image
	created := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var occurrence domain.ArticleImage
		if err := tx.First(&occurrence, occurrenceID).Error; err != nil {
			return err
		}
		var document domain.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status<>'deleting'", occurrence.DocumentID).First(&document).Error; err != nil {
			return errors.New("article is unavailable for snapshot attachment")
		}
		var version domain.DocumentIndex
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status IN ('active','retired')", occurrence.IndexID).First(&version).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&occurrence, occurrenceID).Error; err != nil {
			return err
		}
		if occurrence.Status == "skipped" {
			return errors.New("image occurrence was skipped")
		}
		if occurrence.ImageID != nil {
			return tx.First(&result, *occurrence.ImageID).Error
		}
		if maxImages > 0 {
			var existing []struct{ ContentHash string }
			if err := tx.Table("article_images a").Select("img.content_hash").Joins("JOIN images img ON img.id=a.image_id").Where("a.index_id=? AND a.status<>'skipped'", version.ID).Clauses(clause.Locking{Strength: "UPDATE"}).Scan(&existing).Error; err != nil {
				return err
			}
			hashes := map[string]bool{}
			for _, row := range existing {
				hashes[row.ContentHash] = true
			}
			if !hashes[input.ContentHash] && len(hashes) >= maxImages {
				return errors.New("unique article image limit reached")
			}
		}
		if descriptionModel != "" {
			key := sha256.Sum256([]byte(input.ContentHash + "\x00" + descriptionModel + "\x00" + input.Prompt))
			cacheID := hex.EncodeToString(key[:])
			if err := tx.Exec("INSERT IGNORE INTO image_description_cache(id,image_id) VALUES (?,NULL)", cacheID).Error; err != nil {
				return err
			}
			var slot struct {
				ID      string
				ImageID *uint64
			}
			if err := tx.Table("image_description_cache").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", cacheID).First(&slot).Error; err != nil {
				return err
			}
			if slot.ImageID != nil {
				err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status IN ('uploaded','processing','ready') AND content_hash=? AND prompt=?", *slot.ImageID, input.ContentHash, input.Prompt).First(&result).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				validCache := err == nil && (validateCache == nil || validateCache(result) == nil)
				if validCache && (result.Status != "ready" || result.DescriptionModel == descriptionModel) {
					return tx.Model(&occurrence).Updates(map[string]any{"image_id": result.ID, "status": "snapshot_ready", "last_error": "", "updated_at": time.Now().UTC()}).Error
				}
			}
			var err error
			result, _, err = (&Store{DB: tx}).CreateImage(ctx, input)
			if err != nil {
				return err
			}
			created = true
			if err := tx.Table("image_description_cache").Where("id=?", cacheID).Update("image_id", result.ID).Error; err != nil {
				return err
			}
			return tx.Model(&occurrence).Updates(map[string]any{"image_id": result.ID, "status": "snapshot_ready", "last_error": "", "updated_at": time.Now().UTC()}).Error
		}
		// Reuse a snapshot from this version only, avoiding stale descriptions
		// across unrelated articles and retaining the original immutable image.
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&domain.Image{}).Select("images.*").Joins("JOIN article_images a ON a.image_id=images.id").Where("a.index_id=? AND images.content_hash=? AND images.status IN ('uploaded','processing','ready')", version.ID, input.ContentHash).First(&result).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var task domain.AITask
			result, task, err = (&Store{DB: tx}).CreateImage(ctx, input)
			_ = task
			created = err == nil
		}
		if err != nil {
			return err
		}
		return tx.Model(&occurrence).Updates(map[string]any{"image_id": result.ID, "status": "snapshot_ready", "last_error": "", "updated_at": time.Now().UTC()}).Error
	})
	return result, created, err
}
