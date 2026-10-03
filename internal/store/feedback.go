package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) CompleteFeedback(ctx context.Context, task domain.AITask, token string, response, model string, items []domain.FeedbackItem) error {
	// Preserve the provider response verbatim, while storing a separate ordered
	// presentation. Do not rely on the model to obey feedback priority.
	items = orderedFeedback(items)
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var feedback domain.AnswerFeedback
		if err := tx.Where("task_id=? AND status='pending'", task.ID).First(&feedback).Error; err != nil {
			return err
		}
		var attempt domain.PracticeAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&attempt, feedback.PracticeAttemptID).Error; err != nil {
			return err
		}
		if attempt.SubmittedAt == nil {
			return invalidResult("feedback requires frozen answers")
		}
		var set domain.QuestionSet
		if err := tx.First(&set, attempt.QuestionSetID).Error; err != nil {
			return err
		}
		var question domain.PracticeQuestion
		if err := tx.Where("question_set_id=? AND ordinal=?", attempt.QuestionSetID, feedback.Ordinal).First(&question).Error; err != nil {
			return err
		}
		var evidence struct {
			ChunkIDs    []string `json:"chunk_ids"`
			ImageRefIDs []uint64 `json:"image_ref_ids"`
		}
		if err := json.Unmarshal([]byte(question.EvidenceJSON), &evidence); err != nil {
			return err
		}
		chunks := map[string]bool{}
		images := map[uint64]bool{}
		for _, id := range evidence.ChunkIDs {
			chunks[id] = true
		}
		for _, id := range evidence.ImageRefIDs {
			images[id] = true
		}
		if len(items) == 0 || len(items) > 32 {
			return invalidResult("feedback must contain 1 to 32 items")
		}
		for _, item := range items {
			if len(item.ChunkIDs)+len(item.ImageRefIDs) > 64 {
				return invalidResult("feedback evidence exceeds limits")
			}
			if item.Type != "conflict" && item.Type != "omission" && item.Type != "expression" {
				return invalidResult("invalid feedback type")
			}
			if strings.TrimSpace(item.Explanation) == "" || len(item.Explanation) > 16000 {
				return invalidResult("invalid feedback explanation")
			}
			// Until official-source verification is attached, a suspected conflict
			// is only pending; article evidence alone cannot establish truth.
			if item.Type == "conflict" && item.JudgmentStatus != "pending_verification" && item.JudgmentStatus != "unable_to_judge" {
				return invalidResult("conflict requires external verification")
			}
			if item.JudgmentStatus != "article_only" && item.JudgmentStatus != "pending_verification" && item.JudgmentStatus != "unable_to_judge" {
				return invalidResult("unverified feedback judgment status")
			}
			if item.JudgmentStatus == "article_only" && len(item.ChunkIDs)+len(item.ImageRefIDs) == 0 {
				return invalidResult("article-supported feedback requires evidence")
			}
			for _, id := range item.ChunkIDs {
				if !chunks[id] {
					return invalidResult("feedback cites unprovided chunk")
				}
			}
			for _, id := range item.ImageRefIDs {
				if !images[id] {
					return invalidResult("feedback cites unprovided image")
				}
				var indexed domain.ImageEvidence
				if err := tx.Table("image_evidence AS e").Select("e.*").Joins("JOIN article_images AS a ON a.id=e.article_image_id AND a.image_id=e.image_id AND a.document_id=e.document_id AND a.index_id=e.index_id").Joins("JOIN images AS i ON i.id=e.image_id AND i.content_hash=e.image_hash AND i.status='ready'").Where("e.article_image_id=? AND e.document_id=? AND e.index_id=? AND e.status='ready' AND a.status<>'skipped'", id, set.DocumentID, set.IndexID).Take(&indexed).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return invalidResult("feedback indexed image evidence unavailable")
					}
					return err
				}
				if err := indexed.ValidateContent(); err != nil {
					return invalidResult("feedback indexed image evidence invalid")
				}
			}
		}
		now := time.Now().UTC()
		result := tx.Exec("UPDATE ai_tasks SET status='succeeded',lease_token='',lease_until=NULL,updated_at=? WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=? AND lease_until>?", now, task.ID, task.ExecutionGeneration, token, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task lease lost")
		}
		body, _ := json.Marshal(items)
		if err := tx.Model(&feedback).Updates(map[string]any{"status": "ready", "items_json": string(body), "original_json": response, "model": model, "last_error": "", "updated_at": now}).Error; err != nil {
			return err
		}
		for ordinal, item := range items {
			if item.Type == "conflict" {
				if err := tx.Create(&domain.AuthorityClaim{AnswerFeedbackID: feedback.ID, ItemOrdinal: ordinal + 1, Assertion: item.Explanation, Status: "pending_verification", CreatedAt: now}).Error; err != nil {
					return err
				}
			}
		}
		// The initial feedback lookup established an RR read view before the
		// attempt lock. Use current reads after that lock so the final worker
		// observes earlier completions instead of leaving the attempt pending.
		var statuses []domain.AnswerFeedback
		if err := tx.Select("id", "status").Clauses(clause.Locking{Strength: "UPDATE"}).Where("practice_attempt_id=?", attempt.ID).Find(&statuses).Error; err != nil {
			return err
		}
		ready := 0
		for _, row := range statuses {
			if row.Status == "ready" {
				ready++
			}
		}
		if len(statuses) == 5 && ready == 5 {
			if err := tx.Model(&attempt).Updates(map[string]any{"status": "feedback_ready", "last_error": "", "updated_at": now}).Error; err != nil {
				return err
			}
			if err := tx.Model(&domain.PracticeReview{}).Where("completed_attempt_id=? AND status='in_progress'", attempt.ID).Updates(map[string]any{"status": "completed", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return tx.Exec("UPDATE task_attempts SET status='succeeded',finished_at=? WHERE task_id=? AND execution_generation=? AND lease_token=?", now, task.ID, task.ExecutionGeneration, token).Error
	})
}

func orderedFeedback(items []domain.FeedbackItem) []domain.FeedbackItem {
	ordered := append([]domain.FeedbackItem(nil), items...)
	priority := map[string]int{"conflict": 0, "omission": 1, "expression": 2}
	sort.SliceStable(ordered, func(i, j int) bool {
		return priority[ordered[i].Type] < priority[ordered[j].Type]
	})
	return ordered
}
