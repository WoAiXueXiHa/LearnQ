package imageindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Indexer struct {
	DB        *gorm.DB
	Embedding model.EmbeddingModel
	Vectors   rag.Qdrant
	Model     string
	Dimension int
}

func (i *Indexer) Process(ctx context.Context, refID uint64) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var ref domain.ArticleImage
	if err := i.DB.WithContext(ctx).Where("id=? AND image_id IS NOT NULL AND status<>'skipped'", refID).First(&ref).Error; err != nil {
		return err
	}
	var version domain.DocumentIndex
	if err := i.DB.WithContext(ctx).First(&version, ref.IndexID).Error; err != nil {
		return err
	}
	if version.Dimension != i.Dimension || !strings.Contains(version.IndexVersion, "embedding="+i.Model+";") {
		return fmt.Errorf("image embedding configuration differs from article index")
	}
	var snapshot domain.Image
	if err := i.DB.WithContext(ctx).Where("id=? AND status='ready'", *ref.ImageID).First(&snapshot).Error; err != nil {
		return err
	}
	content, descriptionModel, err := store.EffectiveImageDescription(ctx, i.DB, version, snapshot)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	identity := sha256.Sum256([]byte(fmt.Sprintf("image:%d:%s:%s", ref.ID, hash, i.Model)))
	id := hex.EncodeToString(identity[:])
	now := time.Now().UTC()
	row := domain.ImageEvidence{ID: id, ArticleImageID: ref.ID, DocumentID: ref.DocumentID, IndexID: ref.IndexID, ImageID: snapshot.ID, Content: content, ContentHash: hash, ImageHash: snapshot.ContentHash, DescriptionModel: descriptionModel, EmbeddingModel: i.Model, Dimension: i.Dimension, Status: "pending", CreatedAt: now, UpdatedAt: now}
	if err := i.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	if err := i.DB.WithContext(ctx).Where("article_image_id=?", ref.ID).First(&row).Error; err != nil {
		return err
	}
	if row.Status == "ready" {
		return nil
	}
	if row.ID != id {
		return fmt.Errorf("image description version changed; rebuild article index")
	}
	claimedAt := time.Now().UTC().Truncate(time.Microsecond)
	claim := i.DB.WithContext(ctx).Model(&row).Where("status='pending' OR (status='processing' AND updated_at<?)", claimedAt.Add(-time.Minute)).Updates(map[string]any{"status": "processing", "updated_at": claimedAt})
	if claim.Error != nil {
		return claim.Error
	}
	if claim.RowsAffected != 1 {
		return nil
	}
	fail := func(err error) error {
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer persistCancel()
		i.DB.WithContext(persistCtx).Model(&row).Where("status='processing' AND updated_at=?", claimedAt).Updates(map[string]any{"status": "failed", "last_error": err.Error(), "updated_at": time.Now().UTC()})
		return err
	}
	vectors, err := i.Embedding.Embed(ctx, []string{content})
	if err != nil {
		return fail(err)
	}
	if len(vectors) != 1 || len(vectors[0]) != i.Dimension {
		return fail(fmt.Errorf("image embedding dimension mismatch"))
	}
	if err := i.Vectors.EnsureCollection(ctx, i.Dimension); err != nil {
		return fail(err)
	}
	pointID := id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:32]
	if err := i.Vectors.Upsert(ctx, []rag.Point{{ID: pointID, Dense: vectors[0], Sparse: rag.Sparse(content), Payload: map[string]any{"chunk_id": id, "document_id": ref.DocumentID, "index_id": ref.IndexID, "article_image_id": ref.ID, "evidence_kind": "image"}}}); err != nil {
		return fail(err)
	}
	result := i.DB.WithContext(ctx).Model(&row).Where("status='processing' AND updated_at=?", claimedAt).Updates(map[string]any{"status": "ready", "last_error": "", "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("image evidence claim lost")
	}
	return nil
}

func (i *Indexer) Run(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		var refs []domain.ArticleImage
		err := i.DB.WithContext(ctx).Table("article_images a").Select("a.*").Joins("JOIN images img ON img.id=a.image_id JOIN documents d ON d.id=a.document_id LEFT JOIN image_evidence e ON e.article_image_id=a.id").Where("a.index_id=d.active_index_id AND d.status NOT IN ('deleting','archived') AND a.status<>'skipped' AND img.status='ready' AND (e.id IS NULL OR e.status='pending' OR (e.status='processing' AND e.updated_at<?))", time.Now().UTC().Add(-time.Minute)).Order("a.id").Limit(1).Find(&refs).Error
		if err != nil {
			logger.Error("image evidence scan failed", "error", err)
			continue
		}
		if len(refs) > 0 {
			if err := i.Process(ctx, refs[0].ID); err != nil {
				logger.Warn("image evidence indexing failed", "image_ref_id", refs[0].ID, "error", err)
			}
		}
	}
}
