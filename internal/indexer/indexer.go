package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
)

type VectorStore interface {
	// VectorStore 隔离 Qdrant 细节，使索引流程可用内存桩验证状态机和补偿逻辑。
	EnsureCollection(context.Context, int) error
	Upsert(context.Context, []rag.Point) error
	DeleteDocument(context.Context, uint64) error
}

type Indexer struct {
	Store        *store.Store
	Embedding    model.EmbeddingModel
	Vectors      VectorStore
	Dimension    int
	ChunkVersion string
	Version      string
}

func (i *Indexer) ConfiguredChunkVersion() string {
	if i.ChunkVersion != "" {
		return i.ChunkVersion
	}
	return rag.LegacyChunkVersion
}
func (i *Indexer) IndexVersion() string {
	if i.Version != "" {
		return i.Version
	}
	return fmt.Sprintf("configured_chunk=%s;dim=%d", i.ConfiguredChunkVersion(), i.Dimension)
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
	if err := i.Store.DB.WithContext(ctx).Where("document_id=? AND index_id=?", documentID, document.ActiveIndexID).
		Order("chunk_index,id").Find(&chunks).Error; err != nil {
		return err
	}
	if len(chunks) == 0 {
		return fmt.Errorf("ready document %d has no persisted chunks", documentID)
	}
	texts := make([]string, len(chunks))
	legacySource := document.ActiveIndexID == 0
	if !legacySource {
		var version domain.DocumentIndex
		if err := i.Store.DB.WithContext(ctx).First(&version, document.ActiveIndexID).Error; err != nil {
			return err
		}
		legacySource = version.ChunkVersion == rag.LegacyChunkVersion
	}
	for index := range chunks {
		texts[index] = chunks[index].EmbeddingContent
		if legacySource {
			texts[index] = chunks[index].Content
		}
	}
	dense, err := i.embedText(ctx, texts)
	if err != nil {
		return err
	}
	if err := i.Vectors.EnsureCollection(ctx, i.Dimension); err != nil {
		return err
	}
	if err := i.Vectors.DeleteDocument(ctx, documentID); err != nil {
		return err
	}
	points := make([]rag.Point, 0, len(chunks))
	for index, chunk := range chunks {
		if vector, ok := dense[index]; ok {
			points = append(points, rag.Point{ID: uuidFromHash(chunk.ID), Dense: vector, Sparse: rag.Sparse(texts[index]), Payload: map[string]any{
				"document_id": documentID, "chunk_id": chunk.ID, "index_id": chunk.IndexID, "title": chunk.Title,
				"start_line": chunk.StartLine, "end_line": chunk.EndLine, "summary": truncate(chunk.Content, 240),
			}})
		}
	}
	if len(points) == 0 {
		return nil
	}
	return i.Vectors.Upsert(ctx, points)
}

func (i *Indexer) Process(ctx context.Context, task domain.AITask) (resultID uint64, processErr error) {
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
	if err := i.status(ctx, task, document.ID, "uploaded", "parsing"); err != nil {
		return 0, err
	}
	actualChunkVersion := i.ConfiguredChunkVersion()
	if actualChunkVersion != rag.LegacyChunkVersion && actualChunkVersion != rag.MarkdownChunkVersion {
		return 0, fmt.Errorf("unsupported chunk version %q", actualChunkVersion)
	}
	if document.MediaType != "md" && document.MediaType != ".md" && document.MediaType != "text/markdown" {
		actualChunkVersion = rag.LegacyChunkVersion
	}
	var chunks []rag.Chunk
	var articleImages []rag.ImageReference
	if actualChunkVersion == rag.MarkdownChunkVersion {
		var err error
		parsed, parseErr := rag.ParseMarkdown(document.Content)
		if parseErr != nil {
			return 0, parseErr
		}
		articleImages = parsed.ImageRefs
		chunks, err = rag.ChunkMarkdown(document.Content)
		if err != nil {
			return 0, err
		}
	} else {
		chunks = rag.ChunkText(document.Content, rag.DefaultChunkSize, rag.DefaultChunkOverlap)
	}
	if len(chunks) == 0 {
		// 空文档切不出块，直接以错误终止；否则会以零切片走完流程，把空文档错误标记为 ready。
		return 0, fmt.Errorf("document produced no chunks")
	}
	if err := i.status(ctx, task, document.ID, "parsing", "embedding"); err != nil {
		return 0, err
	}
	texts := make([]string, len(chunks))
	for index := range chunks {
		texts[index] = chunks[index].EmbeddingContent
		if actualChunkVersion == rag.LegacyChunkVersion {
			texts[index] = chunks[index].Content
		}
	}
	dense, err := i.embedText(ctx, texts)
	if err != nil {
		return 0, err
	}
	if err := i.status(ctx, task, document.ID, "embedding", "indexing"); err != nil {
		return 0, err
	}
	// EnsureCollection 确保 Qdrant collection 存在且维度一致，创建动作是幂等的。
	if err := i.Vectors.EnsureCollection(ctx, i.Dimension); err != nil {
		return 0, err
	}
	// Each execution attempt owns a separate immutable build; never delete active points.
	version := domain.DocumentIndex{DocumentID: document.ID, TaskID: task.ID, ExecutionGeneration: task.ExecutionGeneration, AttemptNo: task.AttemptNo,
		Content: document.Content, ContentHash: document.ContentHash, IndexVersion: i.IndexVersion(),
		ChunkVersion: actualChunkVersion, Dimension: i.Dimension, Status: "building", CreatedAt: time.Now().UTC()}
	defer func() {
		if processErr == nil || version.ID == 0 {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := i.abandon(cleanupCtx, document.ID, version.ID); err != nil {
			processErr = fmt.Errorf("%w; build cleanup failed: %v", processErr, err)
		}
	}()
	rows := make([]domain.DocumentChunk, len(chunks))
	points := make([]rag.Point, 0, len(chunks))
	now := time.Now().UTC()
	err = i.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current domain.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, document.ID).Error; err != nil {
			return err
		}
		if current.Status != "indexing" || current.IndexingTaskID != task.ID {
			return fmt.Errorf("document state changed before build persistence")
		}
		var owner int64
		if err := tx.Model(&domain.AITask{}).Where("id=? AND status='processing' AND execution_generation=? AND attempt_no=? AND lease_token=? AND lease_until>?", task.ID, task.ExecutionGeneration, task.AttemptNo, task.LeaseToken, time.Now().UTC()).Count(&owner).Error; err != nil {
			return err
		}
		if owner != 1 {
			return errors.New("task lease lost before build persistence")
		}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		// Persist every occurrence, including unresolved references, before any
		// remote processing. A text-ready index does not imply image completeness.
		for _, ref := range articleImages {
			row := domain.ArticleImage{DocumentID: document.ID, IndexID: version.ID,
				StartByte: ref.StartByte, EndByte: ref.EndByte, StartLine: ref.StartLine, EndLine: ref.EndLine,
				OriginalURL: ref.URL, AltText: ref.Alt, SyntaxKind: ref.Kind, Status: ref.Status,
				CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		for index, chunk := range chunks {
			// 块身份包含构建版本，重复文本与迟到重试不能覆盖其他版本。
			id := stableID(document.ID, chunk.Index, fmt.Sprintf("%d:%s", version.ID, chunk.Hash))
			headingJSON, _ := json.Marshal(chunk.HeadingPath)
			imagesJSON, _ := json.Marshal(chunk.ImageRefs)
			spansJSON, _ := json.Marshal(chunk.BlockSpans)
			rows[index] = domain.DocumentChunk{EmbeddingContent: texts[index], HeadingPathJSON: string(headingJSON), BlockSpansJSON: string(spansJSON), ImageRefsJSON: string(imagesJSON), BlockType: chunk.BlockType, ID: id, DocumentID: document.ID, IndexID: version.ID, StartByte: chunk.StartByte, EndByte: chunk.EndByte, ChunkIndex: chunk.Index, Title: chunk.Title, StartLine: chunk.StartLine, EndLine: chunk.EndLine, Content: chunk.Content, ContentHash: chunk.Hash, CreatedAt: now}
			if vector, ok := dense[index]; ok {
				points = append(points, rag.Point{ID: uuidFromHash(id), Dense: vector, Sparse: rag.Sparse(texts[index]), Payload: map[string]any{
					"document_id": document.ID, "chunk_id": id, "index_id": version.ID, "title": chunk.Title, "start_line": chunk.StartLine,
					"end_line": chunk.EndLine, "summary": truncate(chunk.Content, 240),
				}})
			}
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		return 0, err
	}
	if len(points) > 0 {
		if err := i.Vectors.Upsert(ctx, points); err != nil {
			return 0, err
		}
	}
	if err := i.requireStatus(ctx, task, document.ID, "indexing"); err != nil {
		return 0, err
	}
	result := i.Store.DB.WithContext(ctx).Model(&domain.DocumentIndex{}).Where("id=? AND status='building'", version.ID).Update("status", "built")
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, errors.New("index build disappeared before completion")
	}
	return document.ID, nil
}

// status 以条件 UPDATE 推进文档状态机：仅当当前状态等于 from 时才更新为 to。
// RowsAffected != 1 说明状态已被并发路径改动（删除请求、重复 Worker），
// 此时返回错误让上层放弃本次索引，不做强制覆盖。
func (i *Indexer) status(ctx context.Context, task domain.AITask, id uint64, from, to string) error {
	result := i.Store.DB.WithContext(ctx).Exec("UPDATE documents SET status=?,updated_at=? WHERE id=? AND status=? AND indexing_task_id=? AND EXISTS (SELECT 1 FROM ai_tasks t WHERE t.id=? AND t.status='processing' AND t.execution_generation=? AND t.attempt_no=? AND t.lease_token=? AND t.lease_until>?)", to, time.Now().UTC(), id, from, task.ID, task.ID, task.ExecutionGeneration, task.AttemptNo, task.LeaseToken, time.Now().UTC())
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
func (i *Indexer) requireStatus(ctx context.Context, task domain.AITask, id uint64, expected string) error {
	var current string
	result := i.Store.DB.WithContext(ctx).Raw("SELECT status FROM documents WHERE id=?", id).Scan(&current)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 || current != expected {
		return fmt.Errorf("document state changed before index completion (current=%s)", current)
	}
	var owner int64
	if err := i.Store.DB.WithContext(ctx).Model(&domain.AITask{}).Where("id=? AND status='processing' AND execution_generation=? AND attempt_no=? AND lease_token=? AND lease_until>?", task.ID, task.ExecutionGeneration, task.AttemptNo, task.LeaseToken, time.Now().UTC()).Count(&owner).Error; err != nil {
		return err
	}
	if owner != 1 {
		return errors.New("task lease lost before index completion")
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

// abandon compensates only this execution's build, preserving the active index.
func (i *Indexer) abandon(ctx context.Context, documentID, indexID uint64) error {
	var doc domain.Document
	err := i.Store.DB.WithContext(ctx).First(&doc, documentID).Error
	deleted := errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && doc.Status == "deleting")
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if scoped, ok := i.Vectors.(interface {
		DeleteIndex(context.Context, uint64, uint64) error
	}); ok {
		if err := scoped.DeleteIndex(ctx, documentID, indexID); err != nil {
			return err
		}
	} else if deleted {
		if err := i.Vectors.DeleteDocument(ctx, documentID); err != nil {
			return err
		}
	}
	if deleted {
		return i.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("index_id=?", indexID).Delete(&domain.DocumentChunk{}).Error; err != nil {
				return err
			}
			return tx.Delete(&domain.DocumentIndex{}, indexID).Error
		})
	}
	return i.Store.DB.WithContext(ctx).Model(&domain.DocumentIndex{}).Where("id=? AND status IN ('building','built')", indexID).Update("status", "failed").Error
}

// Metadata-only source chunks remain persisted for exact coverage, but providers
// never receive empty embedding inputs and raw markup is not used as a fallback.
func (i *Indexer) embedText(ctx context.Context, texts []string) (map[int][]float32, error) {
	inputs := make([]string, 0, len(texts))
	indices := make([]int, 0, len(texts))
	for j, value := range texts {
		if strings.TrimSpace(value) != "" {
			inputs = append(inputs, value)
			indices = append(indices, j)
		}
	}
	result := make(map[int][]float32, len(inputs))
	if len(inputs) == 0 {
		return result, nil
	}
	dense, err := i.Embedding.Embed(ctx, inputs)
	if err != nil {
		return nil, err
	}
	if len(dense) != len(inputs) {
		return nil, fmt.Errorf("embedding result count mismatch")
	}
	for j, vector := range dense {
		if len(vector) != i.Dimension {
			return nil, fmt.Errorf("EMBEDDING_DIMENSION_MISMATCH")
		}
		result[indices[j]] = vector
	}
	return result, nil
}
