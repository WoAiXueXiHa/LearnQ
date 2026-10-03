package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validateQuestionDrafts(questions []domain.QuestionDraft) error {
	if len(questions) != 5 {
		return invalidResult("question set must contain exactly five questions")
	}
	seen := map[string]bool{}
	for _, q := range questions {
		if len(q.ReferenceItems) > 0 {
			if len(q.ReferenceItems) < 2 || len(q.ReferenceItems) > 4 {
				return invalidResult("reference items require two to four short points")
			}
			allowedChunks, allowedImages := map[string]bool{}, map[uint64]bool{}
			for _, id := range q.ChunkIDs {
				allowedChunks[id] = true
			}
			for _, id := range q.ImageRefIDs {
				allowedImages[id] = true
			}
			for _, item := range q.ReferenceItems {
				if strings.TrimSpace(item.Text) == "" || utf8.RuneCountInString(item.Text) > 240 || len(item.ChunkIDs)+len(item.ImageRefIDs) == 0 || len(item.ChunkIDs)+len(item.ImageRefIDs) > 64 {
					return invalidResult("reference item must be short and supported")
				}
				for _, id := range item.ChunkIDs {
					if !allowedChunks[id] {
						return invalidResult("reference item cites evidence outside its question")
					}
				}
				for _, id := range item.ImageRefIDs {
					if !allowedImages[id] {
						return invalidResult("reference item cites image outside its question")
					}
				}
			}
			if len(q.ReferencePoints) != len(q.ReferenceItems) {
				return invalidResult("reference points must match structured items")
			}
			for j, item := range q.ReferenceItems {
				if q.ReferencePoints[j] != item.Text {
					return invalidResult("reference points differ from structured items")
				}
			}
		}
		prompt := strings.TrimSpace(q.Prompt)
		if prompt == "" || len(prompt) > 8000 || seen[prompt] {
			return invalidResult("question prompts must be nonempty, distinct and bounded")
		}
		seen[prompt] = true
		if len(q.KnowledgePoints) == 0 || len(q.ReferencePoints) == 0 || len(q.ChunkIDs)+len(q.ImageRefIDs) == 0 {
			return invalidResult("each question requires knowledge points, reference points and evidence")
		}
		if len(q.ChunkIDs)+len(q.ImageRefIDs) > 64 || len(q.KnowledgePoints) > 32 || len(q.ReferencePoints) > 32 {
			return invalidResult("question metadata exceeds limits")
		}
		for _, point := range append(append([]string{}, q.KnowledgePoints...), q.ReferencePoints...) {
			if strings.TrimSpace(point) == "" || len(point) > 8000 {
				return invalidResult("question points must be nonempty and bounded")
			}
		}
	}
	return nil
}

// EditQuestionSet retains each authoring revision; confirmed sets are immutable.
// Evidence must belong to the exact index version, never the current live index.
func (s *Store) EditQuestionSet(ctx context.Context, id uint64, questions []domain.QuestionDraft, confirm bool) (domain.QuestionSet, error) {
	var set domain.QuestionSet
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&set, id).Error; err != nil {
			return err
		}
		if set.Status != "draft" {
			return errors.New("only draft question sets may be edited or confirmed")
		}
		for j := range questions {
			if len(questions[j].ReferencePoints) == 0 {
				var previous domain.PracticeQuestion
				if err := tx.Where("question_set_id=? AND ordinal=?", id, j+1).First(&previous).Error; err != nil {
					return errors.New("private reference points unavailable")
				}
				if previous.ReferenceItemsJSON != "" && previous.ReferenceItemsJSON != "null" {
					if err := json.Unmarshal([]byte(previous.ReferenceItemsJSON), &questions[j].ReferenceItems); err != nil {
						return err
					}
				}
				if err := json.Unmarshal([]byte(previous.ReferencePointsJSON), &questions[j].ReferencePoints); err != nil {
					return err
				}
			}
		}
		if err := validateQuestionDrafts(questions); err != nil {
			return err
		}
		for _, q := range questions {
			for _, chunkID := range q.ChunkIDs {
				var chunk domain.DocumentChunk
				if err := tx.Where("id=? AND index_id=? AND document_id=?", chunkID, set.IndexID, set.DocumentID).First(&chunk).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return invalidResult("question evidence is outside the article version")
					}
					return err
				}
				if len(q.ReferenceItems) > 0 {
					var version domain.DocumentIndex
					if err := tx.Where("id=? AND document_id=?", set.IndexID, set.DocumentID).First(&version).Error; err != nil {
						return err
					}
					if err := validateReferenceSource(version, chunk); err != nil {
						return err
					}
				}
			}
			for _, refID := range q.ImageRefIDs {
				var occurrence domain.ArticleImage
				if err := tx.Where("id=? AND index_id=? AND document_id=? AND status<>'skipped' AND image_id IS NOT NULL", refID, set.IndexID, set.DocumentID).First(&occurrence).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return invalidResult("question image evidence unavailable")
					}
					return err
				}
				var image domain.Image
				if err := tx.Where("id=? AND status='ready'", *occurrence.ImageID).First(&image).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return invalidResult("question image description not ready")
					}
					return err
				}
				var evidence domain.ImageEvidence
				if err := tx.Where("article_image_id=? AND document_id=? AND index_id=? AND image_id=? AND image_hash=? AND status='ready'", occurrence.ID, set.DocumentID, set.IndexID, image.ID, image.ContentHash).First(&evidence).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return invalidResult("question indexed image evidence unavailable")
					}
					return err
				}
				if err := evidence.ValidateContent(); err != nil {
					return invalidResult("question indexed image evidence invalid")
				}
			}
		}
		if err := tx.Where("question_set_id=?", id).Delete(&domain.PracticeQuestion{}).Error; err != nil {
			return err
		}
		for j, q := range questions {
			knowledge, _ := json.Marshal(q.KnowledgePoints)
			reference, _ := json.Marshal(q.ReferencePoints)
			items, _ := json.Marshal(q.ReferenceItems)
			evidence, _ := json.Marshal(map[string]any{"chunk_ids": q.ChunkIDs, "image_ref_ids": q.ImageRefIDs})
			row := domain.PracticeQuestion{QuestionSetID: id, Ordinal: j + 1, Prompt: q.Prompt, KnowledgePointsJSON: string(knowledge), ReferencePointsJSON: string(reference), ReferenceItemsJSON: string(items), EvidenceJSON: string(evidence)}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		body, _ := json.Marshal(questions)
		now := time.Now().UTC()
		if err := tx.Create(&domain.QuestionSetEdit{QuestionSetID: id, QuestionsJSON: string(body), CreatedAt: now}).Error; err != nil {
			return err
		}
		if confirm {
			set.Status = "confirmed"
		}
		set.UpdatedAt = now
		return tx.Save(&set).Error
	})
	return set, err
}
