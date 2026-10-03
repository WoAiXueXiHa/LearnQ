package api

import (
	"context"
	"slices"

	"github.com/WoAiXueXiHa/LearnQ/internal/evaluation"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
)

type activeVectorRetriever interface {
	DenseActive(context.Context, []float32, int, []string) ([]rag.Hit, error)
	SparseActive(context.Context, rag.SparseVector, int, []string) ([]rag.Hit, error)
	HybridActive(context.Context, []float32, rag.SparseVector, int, []string) ([]rag.Hit, error)
}

// Evaluation uses the same active-only candidate scope as online RAG.
type activeEvaluationRetriever struct {
	evaluation.Retriever
	active  activeVectorRetriever
	ids     []string
	indexes []evaluation.IndexMetadata
}

func (r activeEvaluationRetriever) filter(hits []rag.Hit, err error) ([]rag.Hit, error) {
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(r.ids))
	for _, id := range r.ids {
		allowed[id] = true
	}
	result := make([]rag.Hit, 0, len(hits))
	for _, hit := range hits {
		id, _ := hit.Payload["chunk_id"].(string)
		if allowed[id] {
			result = append(result, hit)
		}
	}
	return result, nil
}

func (r activeEvaluationRetriever) Dense(ctx context.Context, vector []float32, k int) ([]rag.Hit, error) {
	if r.active != nil {
		return r.filter(r.active.DenseActive(ctx, vector, k, r.ids))
	}
	return r.filter(r.Retriever.Dense(ctx, vector, k))
}

func (r activeEvaluationRetriever) Sparse(ctx context.Context, vector rag.SparseVector, k int) ([]rag.Hit, error) {
	if r.active != nil {
		return r.filter(r.active.SparseActive(ctx, vector, k, r.ids))
	}
	return r.filter(r.Retriever.Sparse(ctx, vector, k))
}

func (r activeEvaluationRetriever) Hybrid(ctx context.Context, dense []float32, sparse rag.SparseVector, k int) ([]rag.Hit, error) {
	if r.active != nil {
		return r.filter(r.active.HybridActive(ctx, dense, sparse, k, r.ids))
	}
	return r.filter(r.Retriever.Hybrid(ctx, dense, sparse, k))
}

func (s *Server) evaluationRetriever(ctx context.Context) (evaluation.Retriever, error) {
	var rows []struct {
		ID string
		evaluation.IndexMetadata
	}
	err := s.store.DB.WithContext(ctx).Table("document_chunks dc").
		Select("dc.id, dc.document_id, dc.index_id, CASE WHEN dc.index_id=0 THEN d.content_hash ELSE ix.content_hash END AS article_sha256, CASE WHEN dc.index_id=0 THEN d.index_version ELSE ix.index_version END AS index_version, CASE WHEN dc.index_id=0 THEN 'legacy-rune-800-overlap-120-unverified' ELSE ix.chunk_version END AS chunk_version, COALESCE(ix.dimension,0) AS dimension").
		Joins("JOIN documents d ON d.id=dc.document_id").
		Joins("LEFT JOIN document_indexes ix ON ix.id=dc.index_id AND ix.document_id=dc.document_id").
		Where("d.status NOT IN ('deleting','archived') AND (d.active_index_id>0 OR d.index_version<>'' OR d.status='ready') AND dc.index_id=d.active_index_id").Order("dc.id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	indexes := make([]evaluation.IndexMetadata, 0)
	seen := make(map[[2]uint64]bool)
	for _, row := range rows {
		ids = append(ids, row.ID)
		key := [2]uint64{row.DocumentID, row.IndexID}
		if !seen[key] {
			indexes = append(indexes, row.IndexMetadata)
			seen[key] = true
		}
	}
	slices.SortFunc(indexes, func(a, b evaluation.IndexMetadata) int {
		if a.DocumentID < b.DocumentID {
			return -1
		}
		if a.DocumentID > b.DocumentID {
			return 1
		}
		return 0
	})
	active, _ := s.vectors.(activeVectorRetriever)
	return activeEvaluationRetriever{Retriever: s.vectors, active: active, ids: ids, indexes: indexes}, nil
}

func (s *Server) evaluationScopeUnchanged(ctx context.Context, before evaluation.Retriever) (bool, error) {
	after, err := s.evaluationRetriever(ctx)
	if err != nil {
		return false, err
	}
	a, b := before.(activeEvaluationRetriever), after.(activeEvaluationRetriever)
	return slices.Equal(a.ids, b.ids) && slices.Equal(a.indexes, b.indexes), nil
}
