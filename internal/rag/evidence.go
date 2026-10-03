package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"gorm.io/gorm"
)

// HybridRetriever is the only vector capability needed by the online evidence path.
// Keeping it narrow lets API and Skill tools share the same retrieval semantics in tests.
type HybridRetriever interface {
	Hybrid(context.Context, []float32, SparseVector, int) ([]Hit, error)
}

// Evidence is a ready-document chunk that can be shown to a model and returned as a citation.
// Source is assigned after MySQL revalidation, so stale Qdrant payloads never become citations.
type Evidence struct {
	ImageRefID  uint64           `json:"image_ref_id,omitempty"`
	EvidenceURL string           `json:"evidence_url,omitempty"`
	BlockSpans  []BlockSpan      `json:"block_spans"`
	HeadingPath []string         `json:"heading_path"`
	BlockType   string           `json:"block_type"`
	ImageRefs   []ImageReference `json:"image_refs"`
	Source      string           `json:"source"`
	DocumentID  uint64           `json:"document_id"`
	IndexID     uint64           `json:"index_id"`
	StartByte   int              `json:"start_byte"`
	EndByte     int              `json:"end_byte"`
	ChunkID     string           `json:"chunk_id"`
	Title       string           `json:"title"`
	StartLine   int              `json:"start_line"`
	EndLine     int              `json:"end_line"`
	Content     string           `json:"content"`
	Summary     string           `json:"summary"`
	Score       float64          `json:"score"`
}

// EvidenceService centralizes embedding, hybrid retrieval and ready-document revalidation.
// API RAG and Skill tools must use this service instead of maintaining separate copies.
type EvidenceService struct {
	DB        *gorm.DB
	Embedding model.EmbeddingModel
	Vectors   HybridRetriever
}

func (s *EvidenceService) Search(ctx context.Context, question string, topK int) ([]Evidence, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, errors.New("question is required")
	}
	if s == nil || s.DB == nil || s.Embedding == nil || s.Vectors == nil {
		return nil, errors.New("RAG dependencies are not configured")
	}
	if topK <= 0 || topK > 20 {
		topK = 5
	}
	vectors, err := s.Embedding.Embed(ctx, []string{question})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("embed query returned %d vectors, want 1", len(vectors))
	}
	var hits []Hit
	if filtered, ok := s.Vectors.(interface {
		HybridActive(context.Context, []float32, SparseVector, int, []string) ([]Hit, error)
	}); ok {
		var ids []string
		if err := s.DB.WithContext(ctx).Table("document_chunks dc").Joins("JOIN documents d ON d.id=dc.document_id").Where("dc.index_id=d.active_index_id AND d.status NOT IN ('deleting','archived') AND (d.active_index_id > 0 OR d.index_version <> '' OR d.status='ready')").Pluck("dc.id", &ids).Error; err != nil {
			return nil, err
		}
		var imageIDs []string
		if err := s.DB.WithContext(ctx).Table("image_evidence e").Joins("JOIN documents d ON d.id=e.document_id JOIN article_images a ON a.id=e.article_image_id").Where("e.index_id=d.active_index_id AND d.status NOT IN ('deleting','archived') AND e.status='ready' AND a.status<>'skipped'").Pluck("e.id", &imageIDs).Error; err != nil {
			return nil, err
		}
		ids = append(ids, imageIDs...)
		if len(ids) == 0 {
			return []Evidence{}, nil
		}
		hits, err = filtered.HybridActive(ctx, vectors[0], Sparse(question), topK, ids)
	} else {
		hits, err = s.Vectors.Hybrid(ctx, vectors[0], Sparse(question), topK)
	}
	if err != nil {
		return nil, fmt.Errorf("hybrid retrieval: %w", err)
	}

	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		if id, ok := hit.Payload["chunk_id"].(string); ok && strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	evidence := make([]Evidence, 0, len(hits))
	if len(ids) == 0 {
		return evidence, nil
	}
	var chunks []domain.DocumentChunk
	if err := s.DB.WithContext(ctx).Table("document_chunks dc").Select("dc.*").Joins("JOIN documents d ON d.id=dc.document_id").Where("dc.id IN ? AND dc.index_id=d.active_index_id AND d.status NOT IN ('deleting','archived') AND (d.active_index_id>0 OR d.index_version<>'' OR d.status='ready')", ids).Find(&chunks).Error; err != nil {
		return nil, fmt.Errorf("load RAG evidence: %w", err)
	}
	byID := make(map[string]domain.DocumentChunk, len(chunks))
	for _, chunk := range chunks {
		byID[chunk.ID] = chunk
	}
	var imageRows []struct {
		domain.ImageEvidence
		StartByte int
		EndByte   int
		StartLine int
		EndLine   int
	}
	if err := s.DB.WithContext(ctx).Table("image_evidence e").Select("e.*,a.start_byte,a.end_byte,a.start_line,a.end_line").Joins("JOIN documents d ON d.id=e.document_id JOIN article_images a ON a.id=e.article_image_id JOIN images img ON img.id=e.image_id").Where("e.id IN ? AND e.index_id=d.active_index_id AND d.status NOT IN ('deleting','archived') AND e.status='ready' AND a.status<>'skipped' AND img.status='ready' AND img.content_hash=e.image_hash", ids).Scan(&imageRows).Error; err != nil {
		return nil, err
	}
	imageByID := map[string]Evidence{}
	for _, row := range imageRows {
		if err := row.ImageEvidence.ValidateContent(); err != nil {
			return nil, err
		}
		imageByID[row.ID] = Evidence{DocumentID: row.DocumentID, IndexID: row.IndexID, ChunkID: row.ID, ImageRefID: row.ArticleImageID, BlockType: "image_description", StartByte: row.StartByte, EndByte: row.EndByte, StartLine: row.StartLine, EndLine: row.EndLine, Title: "图片模型观察（非权威事实）", Content: row.Content, Summary: truncateEvidence(row.Content, 240), EvidenceURL: fmt.Sprintf("/api/v1/documents/%d/indexes/%d/images/%d", row.DocumentID, row.IndexID, row.ArticleImageID)}
	}
	seen := make(map[string]bool)
	for _, hit := range hits {
		id, ok := hit.Payload["chunk_id"].(string)
		if !ok || seen[id] {
			continue
		}
		chunk, ok := byID[id]
		if !ok {
			if image, exists := imageByID[id]; exists {
				image.Source = fmt.Sprintf("S%d", len(evidence)+1)
				image.Score = hit.Score
				evidence = append(evidence, image)
				seen[id] = true
			}
			continue
		}
		seen[id] = true
		var headingPath []string
		var imageRefs []ImageReference
		var blockSpans []BlockSpan
		if chunk.HeadingPathJSON != "" {
			if err := json.Unmarshal([]byte(chunk.HeadingPathJSON), &headingPath); err != nil {
				return nil, fmt.Errorf("decode evidence heading path: %w", err)
			}
		}
		if chunk.ImageRefsJSON != "" {
			if err := json.Unmarshal([]byte(chunk.ImageRefsJSON), &imageRefs); err != nil {
				return nil, fmt.Errorf("decode evidence image references: %w", err)
			}
		}
		if chunk.BlockSpansJSON != "" {
			if err := json.Unmarshal([]byte(chunk.BlockSpansJSON), &blockSpans); err != nil {
				return nil, fmt.Errorf("decode evidence block spans: %w", err)
			}
		}
		source := fmt.Sprintf("S%d", len(evidence)+1)
		evidence = append(evidence, Evidence{
			BlockSpans: blockSpans, HeadingPath: headingPath, BlockType: chunk.BlockType, ImageRefs: imageRefs, Source: source, DocumentID: chunk.DocumentID, ChunkID: chunk.ID, IndexID: chunk.IndexID, StartByte: chunk.StartByte, EndByte: chunk.EndByte,
			Title: chunk.Title, StartLine: chunk.StartLine, EndLine: chunk.EndLine,
			Content: chunk.Content, Summary: truncateEvidence(chunk.Content, 240), Score: hit.Score,
		})
	}
	return evidence, nil
}

func truncateEvidence(value string, limit int) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
