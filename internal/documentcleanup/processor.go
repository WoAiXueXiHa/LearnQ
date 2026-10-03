package documentcleanup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/imagestore"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
	"gorm.io/gorm"
)

type Processor struct {
	Store   *store.Store
	Images  *imagestore.Store
	Vectors interface {
		DeleteDocument(context.Context, uint64) error
	}
}

func (p *Processor) Process(ctx context.Context, job store.DocumentDeletion) (store.DocumentDeletion, error) {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	if job.Status == "complete" || (job.Status == "quiescing" && time.Now().UTC().Before(job.QuiesceUntil)) {
		return job, nil
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return job, err
	}
	token := hex.EncodeToString(entropy[:])
	now := time.Now().UTC()
	claim := p.Store.DB.WithContext(ctx).Model(&job).Where("status<>'complete' AND (claim_until IS NULL OR claim_until<?)", now).Updates(map[string]any{"claim_token": token, "claim_until": now.Add(time.Minute)})
	if claim.Error != nil {
		return job, claim.Error
	}
	if claim.RowsAffected != 1 {
		return job, nil
	}
	if err := p.Store.DB.WithContext(ctx).First(&job, job.ID).Error; err != nil {
		return job, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Store.DB.WithContext(cleanupCtx).Model(&job).Where("claim_token=?", token).Updates(map[string]any{"claim_token": "", "claim_until": nil})
	}()
	failure := func(code, message string) (store.DocumentDeletion, error) {
		job.LastError = message
		p.Store.DB.WithContext(ctx).Model(&job).Where("claim_token=?", token).Updates(map[string]any{"last_error": message, "updated_at": time.Now().UTC()})
		return job, fmt.Errorf("%s: %s", code, message)
	}
	if job.Status == "quiescing" {
		if time.Now().UTC().Before(job.QuiesceUntil) {
			return job, nil
		}
		advance := p.Store.DB.WithContext(ctx).Model(&job).Where("status='quiescing' AND claim_token=? AND claim_until>?", token, time.Now().UTC()).Update("status", "vectors_pending")
		if advance.Error != nil || advance.RowsAffected != 1 {
			return failure("DELETE_DATABASE_FAILED", "could not advance deletion state")
		}
		job.Status = "vectors_pending"
	}
	if job.Status == "vectors_pending" {
		if p.Vectors == nil {
			return failure("DELETE_VECTORS_FAILED", "vector storage unavailable")
		}
		if p.Vectors != nil {
			if err := p.Vectors.DeleteDocument(ctx, job.DocumentID); err != nil {
				return failure("DELETE_VECTORS_FAILED", "vector deletion failed; retry the same confirmed deletion")
			}
		}
		if err := p.Store.PurgeDocumentRows(ctx, job); err != nil {
			return failure("DELETE_DATABASE_FAILED", "database deletion failed; retry the same confirmed deletion")
		}
		job.Status = "images_pending"
	}
	if job.Status == "images_pending" {
		if p.Vectors == nil {
			return failure("DELETE_VECTORS_FAILED", "vector storage unavailable")
		}
		var imageIDs []uint64
		if err := json.Unmarshal([]byte(job.ImageIDsJSON), &imageIDs); err != nil {
			return failure("DELETE_IMAGES_FAILED", "invalid persisted deletion image scope")
		}
		for _, imageID := range imageIDs {
			var image domain.Image
			err := p.Store.DB.WithContext(ctx).First(&image, imageID).Error
			if err == gorm.ErrRecordNotFound {
				continue
			}
			if err != nil {
				return failure("DELETE_IMAGES_FAILED", "could not inspect retained image")
			}
			if image.StudyRecordID != nil || image.DerivedDocumentID != nil {
				continue
			}
			var references int64
			if err := p.Store.DB.WithContext(ctx).Model(&domain.ArticleImage{}).Where("image_id=?", imageID).Count(&references).Error; err != nil {
				return failure("DELETE_IMAGES_FAILED", "could not check image references")
			}
			if references > 0 {
				continue
			}
			if p.Images == nil {
				return failure("DELETE_IMAGES_FAILED", "image storage unavailable")
			}
			if _, err := p.Store.PrepareImageDelete(ctx, imageID); err != nil {
				return failure("DELETE_IMAGES_FAILED", "could not prepare unreferenced image deletion")
			}
			if err := p.Images.Delete(image.StoragePath); err != nil {
				return failure("DELETE_IMAGES_FAILED", "could not delete image file")
			}
			if err := p.Store.FinalizeImageDelete(ctx, imageID); err != nil {
				return failure("DELETE_IMAGES_FAILED", "could not delete image metadata")
			}
		}
		if p.Vectors != nil {
			if err := p.Vectors.DeleteDocument(ctx, job.DocumentID); err != nil {
				return failure("DELETE_VECTORS_FAILED", "final vector cleanup failed")
			}
		}
		complete := p.Store.DB.WithContext(ctx).Model(&job).Where("claim_token=? AND claim_until>?", token, time.Now().UTC()).Updates(map[string]any{"status": "complete", "last_error": "", "updated_at": time.Now().UTC()})
		if complete.Error != nil || complete.RowsAffected != 1 {
			return failure("DELETE_DATABASE_FAILED", "could not finalize deletion record")
		}
		job.Status = "complete"
		job.LastError = ""
	}
	return job, nil
}
func (p *Processor) Run(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		var jobs []store.DocumentDeletion
		if err := p.Store.DB.WithContext(ctx).Where("status<>'complete' AND quiesce_until<=? AND (claim_until IS NULL OR claim_until<?)", time.Now().UTC(), time.Now().UTC()).Order("updated_at,id").Limit(1).Find(&jobs).Error; err != nil {
			logger.Error("document cleanup scan failed", "error", err)
			continue
		}
		if len(jobs) > 0 {
			if _, err := p.Process(ctx, jobs[0]); err != nil {
				logger.Warn("document cleanup failed", "deletion_id", jobs[0].ID, "error", err)
			}
		}
	}
}
