package rag

import (
	"context"
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
	Source     string  `json:"source"`
	DocumentID uint64  `json:"document_id"`
	ChunkID    string  `json:"chunk_id"`
	Title      string  `json:"title"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	Content    string  `json:"content"`
	Summary    string  `json:"summary"`
	Score      float64 `json:"score"`
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
	hits, err := s.Vectors.Hybrid(ctx, vectors[0], Sparse(question), topK)
	if err != nil {
		return nil, fmt.Errorf("hybrid retrieval: %w", err)
	}

	evidence := make([]Evidence, 0, len(hits))
	for _, hit := range hits {
		chunkID, ok := hit.Payload["chunk_id"].(string)
		if !ok || strings.TrimSpace(chunkID) == "" {
			continue
		}
		var chunk domain.DocumentChunk
		result := s.DB.WithContext(ctx).Table("document_chunks dc").Select("dc.*").
			Joins("JOIN documents d ON d.id=dc.document_id").
			Where("dc.id=? AND d.status='ready'", chunkID).Take(&chunk)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			continue
		}
		if result.Error != nil {
			return nil, fmt.Errorf("load RAG evidence %s: %w", chunkID, result.Error)
		}
		source := fmt.Sprintf("S%d", len(evidence)+1)
		evidence = append(evidence, Evidence{
			Source: source, DocumentID: chunk.DocumentID, ChunkID: chunk.ID,
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
