package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/gin-gonic/gin"
)

// This metadata is called only after submission. The evidence endpoint remains
// responsible for proving source/hash/byte integrity when the user opens it.
func (s *Server) practiceEvidenceMetadata(ctx context.Context, set domain.QuestionSet, questions []domain.PracticeQuestion) (map[string]gin.H, error) {
	chunks, images := []string{}, []uint64{}
	for _, q := range questions {
		var refs struct {
			ChunkIDs    []string `json:"chunk_ids"`
			ImageRefIDs []uint64 `json:"image_ref_ids"`
		}
		if err := json.Unmarshal([]byte(q.EvidenceJSON), &refs); err != nil {
			return nil, err
		}
		chunks = append(chunks, refs.ChunkIDs...)
		images = append(images, refs.ImageRefIDs...)
	}
	var doc domain.Document
	if err := s.store.DB.WithContext(ctx).First(&doc, set.DocumentID).Error; err != nil {
		return nil, err
	}
	out := map[string]gin.H{}
	if len(chunks) > 0 {
		var rows []domain.DocumentChunk
		if err := s.store.DB.WithContext(ctx).Where("document_id=? AND index_id=? AND id IN ?", set.DocumentID, set.IndexID, chunks).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			var path []string
			if row.HeadingPathJSON != "" {
				if err := json.Unmarshal([]byte(row.HeadingPathJSON), &path); err != nil {
					return nil, err
				}
			}
			out["chunk:"+row.ID] = gin.H{"document_name": doc.Filename, "index_id": set.IndexID, "source_kind": "article", "heading_path": path, "start_line": row.StartLine, "end_line": row.EndLine, "evidence_url": fmt.Sprintf("/api/v1/documents/%d/indexes/%d/chunks/%s", set.DocumentID, set.IndexID, row.ID)}
		}
	}
	if len(images) > 0 {
		var rows []domain.ArticleImage
		if err := s.store.DB.WithContext(ctx).Where("document_id=? AND index_id=? AND id IN ?", set.DocumentID, set.IndexID, images).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[fmt.Sprintf("image:%d", row.ID)] = gin.H{"document_name": doc.Filename, "index_id": set.IndexID, "source_kind": "image_observation", "title": row.AltText, "start_line": row.StartLine, "end_line": row.EndLine, "evidence_url": fmt.Sprintf("/api/v1/documents/%d/indexes/%d/images/%d", set.DocumentID, set.IndexID, row.ID)}
		}
	}
	return out, nil
}
