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
	EnsureCollection(context.Context, int) error
	Upsert(context.Context, []rag.Point) error
}

type Indexer struct {
	Store     *store.Store
	Embedding model.EmbeddingModel
	Vectors   VectorStore
	Dimension int
}

// HandleFailure makes indexing retryable and exposes terminal failure on the
// document. Chunks and vector point IDs are deterministic, so rebuilding them
// after resetting the state is idempotent.
func (i *Indexer) HandleFailure(ctx context.Context, task domain.AITask, terminal bool, cause error) error {
	var payload struct {
		DocumentID uint64 `json:"document_id"`
	}
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil || payload.DocumentID == 0 {
		return fmt.Errorf("invalid document task payload")
	}
	status := "uploaded"
	if terminal {
		status = "failed"
	}
	errorMessage := ""
	if terminal {
		errorMessage = cause.Error()
	}
	return i.Store.DB.WithContext(ctx).Model(&domain.Document{}).
		Where("id = ? AND status <> 'ready'", payload.DocumentID).
		Updates(map[string]any{"status": status, "error_message": errorMessage, "updated_at": time.Now().UTC()}).Error
}

func (i *Indexer) Process(ctx context.Context, task domain.AITask) error {
	var payload struct {
		DocumentID uint64 `json:"document_id"`
	}
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil || payload.DocumentID == 0 {
		return fmt.Errorf("invalid document task payload")
	}
	var document domain.Document
	if err := i.Store.DB.WithContext(ctx).First(&document, payload.DocumentID).Error; err != nil {
		return err
	}
	if err := i.status(ctx, document.ID, "uploaded", "parsing"); err != nil {
		return err
	}
	chunks := rag.ChunkText(document.Content, 800, 120)
	if len(chunks) == 0 {
		return fmt.Errorf("document produced no chunks")
	}
	if err := i.status(ctx, document.ID, "parsing", "embedding"); err != nil {
		return err
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
	if err := i.status(ctx, document.ID, "embedding", "indexing"); err != nil {
		return err
	}
	if err := i.Vectors.EnsureCollection(ctx, i.Dimension); err != nil {
		return err
	}
	rows := make([]domain.DocumentChunk, len(chunks))
	points := make([]rag.Point, len(chunks))
	now := time.Now().UTC()
	for index, chunk := range chunks {
		id := stableID(document.ID, chunk.Hash)
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
		return err
	}
	if err := i.Vectors.Upsert(ctx, points); err != nil {
		return err
	}
	return i.status(ctx, document.ID, "indexing", "ready")
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

func stableID(documentID uint64, contentHash string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", documentID, contentHash)))
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
