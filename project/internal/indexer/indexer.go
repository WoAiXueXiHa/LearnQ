package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/rag"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
	"gorm.io/gorm"
)

type VectorStore interface {
	// VectorStore 隔离 Qdrant 细节，使索引流程可用内存桩验证状态机和补偿逻辑。
	EnsureCollection(context.Context, int) error
	Upsert(context.Context, []rag.Point) error
	DeleteDocument(context.Context, uint64) error
}

type Indexer struct {
	Store     *store.Store
	Embedding model.EmbeddingModel
	Vectors   VectorStore
	Dimension int
}

func (i *Indexer) Process(ctx context.Context, task domain.AITask) (uint64, error) {
	// 文档状态按 uploaded -> parsing -> embedding -> indexing -> ready 单向推进。
	// 每一步都用条件更新做并发保护，删除请求或重复 Worker 无法悄悄覆盖当前状态。
	var payload struct {
		DocumentID uint64 `json:"document_id"`
	}
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil || payload.DocumentID == 0 {
		return 0, fmt.Errorf("invalid document task payload")
	}
	var document domain.Document
	if err := i.Store.DB.WithContext(ctx).First(&document, payload.DocumentID).Error; err != nil {
		return 0, err
	}
	if err := i.status(ctx, document.ID, "uploaded", "parsing"); err != nil {
		return 0, err
	}
	chunks := rag.ChunkText(document.Content, 800, 120)
	if len(chunks) == 0 {
		return 0, fmt.Errorf("document produced no chunks")
	}
	if err := i.status(ctx, document.ID, "parsing", "embedding"); err != nil {
		return 0, err
	}
	texts := make([]string, len(chunks))
	for index := range chunks {
		texts[index] = chunks[index].Content
	}
	dense, err := i.Embedding.Embed(ctx, texts)
	if err != nil {
		return 0, err
	}
	if len(dense) != len(chunks) {
		return 0, fmt.Errorf("embedding result count mismatch")
	}
	for _, vector := range dense {
		if len(vector) != i.Dimension {
			return 0, fmt.Errorf("EMBEDDING_DIMENSION_MISMATCH")
		}
	}
	if err := i.status(ctx, document.ID, "embedding", "indexing"); err != nil {
		return 0, err
	}
	if err := i.Vectors.EnsureCollection(ctx, i.Dimension); err != nil {
		return 0, err
	}
	rows := make([]domain.DocumentChunk, len(chunks))
	points := make([]rag.Point, len(chunks))
	now := time.Now().UTC()
	for index, chunk := range chunks {
		// 稳定 ID 让重试具有幂等性；同一内容块会覆盖原记录和向量点。
		id := stableID(document.ID, chunk.Index, chunk.Hash)
		rows[index] = domain.DocumentChunk{ID: id, DocumentID: document.ID, ChunkIndex: chunk.Index, Title: chunk.Title, StartLine: chunk.StartLine, EndLine: chunk.EndLine, Content: chunk.Content, ContentHash: chunk.Hash, CreatedAt: now}
		points[index] = rag.Point{ID: uuidFromHash(id), Dense: dense[index], Sparse: rag.Sparse(chunk.Content), Payload: map[string]any{
			"document_id": document.ID, "chunk_id": id, "title": chunk.Title, "start_line": chunk.StartLine,
			"end_line": chunk.EndLine, "summary": truncate(chunk.Content, 240),
		}}
	}
	if err := i.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index := range rows {
			if err := tx.Where("id=?", rows[index].ID).Assign(rows[index]).FirstOrCreate(&rows[index]).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if err := i.Vectors.Upsert(ctx, points); err != nil {
		return 0, err
	}
	if err := i.requireStatus(ctx, document.ID, "indexing"); err != nil {
		// MySQL 与 Qdrant 无法共享事务。若向量写入期间文档被删除/改态，
		// 立即反向删除两侧切片，避免“文档不可见但向量仍可召回”的幽灵证据。
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanupErr := i.Vectors.DeleteDocument(cleanupCtx, document.ID)
		_ = i.Store.DB.WithContext(cleanupCtx).
			Where("document_id=?", document.ID).Delete(&domain.DocumentChunk{}).Error
		if cleanupErr != nil {
			return 0, fmt.Errorf("%w; vector cleanup failed: %v", err, cleanupErr)
		}
		return 0, err
	}
	return document.ID, nil
}

func (i *Indexer) status(ctx context.Context, id uint64, from, to string) error {
	result := i.Store.DB.WithContext(ctx).Exec("UPDATE documents SET status=?,updated_at=? WHERE id=? AND status=?", to, time.Now().UTC(), id, from)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		var current string
		i.Store.DB.WithContext(ctx).Raw("SELECT status FROM documents WHERE id=?", id).Scan(&current)
		return fmt.Errorf("document state changed while moving %s -> %s (current=%s)", from, to, current)
	}
	return nil
}

func (i *Indexer) requireStatus(ctx context.Context, id uint64, expected string) error {
	var current string
	result := i.Store.DB.WithContext(ctx).Raw("SELECT status FROM documents WHERE id=?", id).Scan(&current)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 || current != expected {
		return fmt.Errorf("document state changed before index completion (current=%s)", current)
	}
	return nil
}

func stableID(documentID uint64, chunkIndex int, contentHash string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", documentID, chunkIndex, contentHash)))
	return hex.EncodeToString(sum[:])
}

func uuidFromHash(hash string) string {
	return hash[0:8] + "-" + hash[8:12] + "-" + hash[12:16] + "-" + hash[16:20] + "-" + hash[20:32]
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
