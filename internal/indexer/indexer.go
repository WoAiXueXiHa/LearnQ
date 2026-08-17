package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
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
	Version   string
}

func (i *Indexer) IndexVersion() string {
	if i.Version != "" {
		return i.Version
	}
	return fmt.Sprintf("chunk-v1-dim-%d", i.Dimension)
}

// ShadowDocument rebuilds vectors for a ready document into an independent
// collection without changing MySQL document or task state. It intentionally
// reuses persisted chunks, so the old collection remains compatible until an
// alias is switched after every document succeeds.
func (i *Indexer) ShadowDocument(ctx context.Context, documentID uint64) error {
	var document domain.Document
	if err := i.Store.DB.WithContext(ctx).First(&document, documentID).Error; err != nil {
		return err
	}
	if document.Status != "ready" {
		return fmt.Errorf("shadow rebuild requires ready document %d", documentID)
	}
	var chunks []domain.DocumentChunk
	if err := i.Store.DB.WithContext(ctx).Where("document_id=?", documentID).
		Order("chunk_index,id").Find(&chunks).Error; err != nil {
		return err
	}
	if len(chunks) == 0 {
		return fmt.Errorf("ready document %d has no persisted chunks", documentID)
	}
	texts := make([]string, len(chunks))
	for index := range chunks {
		texts[index] = chunks[index].Content
	}
	dense, err := i.Embedding.Embed(ctx, texts)
	if err != nil {
		return err
	}
	if len(dense) != len(chunks) {
		return fmt.Errorf("embedding result count mismatch")
	}
	for _, vector := range dense {
		if len(vector) != i.Dimension {
			return fmt.Errorf("EMBEDDING_DIMENSION_MISMATCH")
		}
	}
	if err := i.Vectors.EnsureCollection(ctx, i.Dimension); err != nil {
		return err
	}
	if err := i.Vectors.DeleteDocument(ctx, documentID); err != nil {
		return err
	}
	points := make([]rag.Point, len(chunks))
	for index, chunk := range chunks {
		points[index] = rag.Point{ID: uuidFromHash(chunk.ID), Dense: dense[index], Sparse: rag.Sparse(chunk.Content), Payload: map[string]any{
			"document_id": documentID, "chunk_id": chunk.ID, "title": chunk.Title,
			"start_line": chunk.StartLine, "end_line": chunk.EndLine, "summary": truncate(chunk.Content, 240),
		}}
	}
	return i.Vectors.Upsert(ctx, points)
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
		// 空文档切不出块，直接以错误终止；否则会以零切片走完流程，把空文档错误标记为 ready。
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
		// 向量维度必须与 Qdrant collection 一致，不一致说明 Embedding 配置漂移，
		// 继续 Upsert 会污染整个检索空间。
		if len(vector) != i.Dimension {
			return 0, fmt.Errorf("EMBEDDING_DIMENSION_MISMATCH")
		}
	}
	if err := i.status(ctx, document.ID, "embedding", "indexing"); err != nil {
		return 0, err
	}
	// EnsureCollection 确保 Qdrant collection 存在且维度一致，创建动作是幂等的。
	if err := i.Vectors.EnsureCollection(ctx, i.Dimension); err != nil {
		return 0, err
	}
	// 重建前删除整篇文档的旧点。文档此时不是 ready，在线检索不会暴露清理窗口。
	if err := i.Vectors.DeleteDocument(ctx, document.ID); err != nil {
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
		// 先清理旧切片，允许切块算法升级后 chunk 数量和稳定 ID 发生变化。
		if err := tx.Where("document_id=?", document.ID).Delete(&domain.DocumentChunk{}).Error; err != nil {
			return err
		}
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

// status 以条件 UPDATE 推进文档状态机：仅当当前状态等于 from 时才更新为 to。
// RowsAffected != 1 说明状态已被并发路径改动（删除请求、重复 Worker），
// 此时返回错误让上层放弃本次索引，不做强制覆盖。
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

// requireStatus 在向量写入后复查文档仍处于 indexing。若期间被删除或改态，
// 索引结果不应保留，调用方据此触发 MySQL 与 Qdrant 的双向清理。
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

// stableID 由 document_id、chunk 序号与内容哈希派生：同一内容重试或重新索引
// 得到相同 ID，使 MySQL 行与 Qdrant 向量点可以幂等覆盖，避免重复索引累积脏数据。
func stableID(documentID uint64, chunkIndex int, contentHash string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", documentID, chunkIndex, contentHash)))
	return hex.EncodeToString(sum[:])
}

// uuidFromHash 在 64 位十六进制哈希的固定位置插入连字符，拼成 Qdrant 要求的
// UUID 形式，同时保持与 chunk ID 的确定性一一对应。
func uuidFromHash(hash string) string {
	return hash[0:8] + "-" + hash[8:12] + "-" + hash[12:16] + "-" + hash[16:20] + "-" + hash[20:32]
}

// truncate 按 rune（字符）而非字节截断，避免切断 UTF-8 多字节字符。
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
