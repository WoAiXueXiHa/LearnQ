package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/rag"
	"gorm.io/gorm"
)

type Retriever interface {
	Hybrid(context.Context, []float32, rag.SparseVector, int) ([]rag.Hit, error)
}

type Service struct {
	DB        *gorm.DB
	Embedding model.EmbeddingModel
	Vectors   Retriever
}

func (s Service) WeeklyStats(ctx context.Context, _ json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	// 统计事实由 SQL 计算，模型只负责解释，避免让 LLM 自己从自然语言估算时长和次数。
	since := time.Now().UTC().AddDate(0, 0, -7)
	var summary struct {
		Minutes int `json:"minutes"`
		Records int `json:"records"`
	}
	var categories []struct {
		Category string `json:"category"`
		Count    int    `json:"count"`
	}
	if err := s.DB.WithContext(ctx).Raw(`SELECT COALESCE(SUM(duration_minute),0) minutes,
		COUNT(*) records FROM study_records WHERE created_at>=?`, since).Scan(&summary).Error; err != nil {
		return nil, nil, err
	}
	if err := s.DB.WithContext(ctx).Raw(`SELECT sm.category,COUNT(*) count FROM study_modules sm
		JOIN study_records sr ON sr.id=sm.study_record_id WHERE sr.created_at>=?
		GROUP BY sm.category ORDER BY count DESC,sm.category`, since).Scan(&categories).Error; err != nil {
		return nil, nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"period_days": 7, "minutes": summary.Minutes, "records": summary.Records,
		"module_categories": categories, "fact_source": "mysql",
	})
	return body, json.RawMessage("[]"), nil
}

func (s Service) RAGQuery(ctx context.Context, input json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	// Qdrant 只负责候选召回；每个命中还要回 MySQL 验证文档为 ready，
	// 防止删除中、索引失败或残留向量成为 Agent 可见证据。
	question := questionFrom(input)
	if question == "" {
		return nil, nil, fmt.Errorf("rag_query needs question, topic, summary or title")
	}
	vectors, err := s.Embedding.Embed(ctx, []string{question})
	if err != nil {
		return nil, nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) != 1 {
		return nil, nil, fmt.Errorf("embed query returned %d vectors, want 1", len(vectors))
	}
	hits, err := s.Vectors.Hybrid(ctx, vectors[0], rag.Sparse(question), 5)
	if err != nil {
		return nil, nil, err
	}
	citations := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		chunkID, _ := hit.Payload["chunk_id"].(string)
		var chunk domain.DocumentChunk
		result := s.DB.WithContext(ctx).Table("document_chunks dc").Select("dc.*").
			Joins("JOIN documents d ON d.id=dc.document_id").
			Where("dc.id=? AND d.status='ready'", chunkID).Take(&chunk)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, nil, fmt.Errorf("load RAG citation %s: %w", chunkID, result.Error)
		}
		citations = append(citations, map[string]any{
			"source": fmt.Sprintf("S%d", len(citations)+1), "document_id": chunk.DocumentID,
			"chunk_id": chunk.ID, "title": chunk.Title, "start_line": chunk.StartLine,
			"end_line": chunk.EndLine, "summary": truncate(chunk.Content, 240), "score": hit.Score,
		})
	}
	if len(citations) == 0 {
		body, _ := json.Marshal(map[string]any{"question": question, "answer": "", "status": "no_evidence"})
		return body, json.RawMessage("[]"), nil
	}
	body, _ := json.Marshal(map[string]any{
		"question": question,
		"answer":   fmt.Sprintf("证据 [S1]：%s", citations[0]["summary"]),
		"status":   "grounded",
	})
	citationBody, _ := json.Marshal(citations)
	return body, citationBody, nil
}

func questionFrom(raw json.RawMessage) string {
	var input map[string]any
	_ = json.Unmarshal(raw, &input)
	for _, key := range []string{"question", "topic", "summary", "title"} {
		if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
