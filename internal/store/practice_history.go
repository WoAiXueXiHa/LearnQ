package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) RetryPracticeTask(ctx context.Context, taskID uint64) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task domain.AITask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, taskID).Error; err != nil {
			return err
		}
		if task.Status != domain.TaskDead {
			return errors.New("only failed tasks may be retried")
		}
		now := time.Now().UTC()
		switch task.Kind {
		case "authority_check":
			result := tx.Model(&domain.AuthorityCheck{}).Where("task_id=? AND status='failed'", taskID).Updates(map[string]any{"status": "pending", "explanation": "", "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("authority check is not retryable")
			}
		case "question_generate":
			result := tx.Model(&domain.QuestionSet{}).Where("generation_task_id=? AND status='failed'", taskID).Updates(map[string]any{"status": "generating", "last_error": "", "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("question set is not retryable")
			}
		case "practice_feedback":
			var feedback domain.AnswerFeedback
			if err := tx.Where("task_id=? AND status='failed'", taskID).First(&feedback).Error; err != nil {
				return err
			}
			// Serialize retry state aggregation with feedback completion. The
			// earlier identity lookup may already have created an RR read view.
			var attempt domain.PracticeAttempt
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&attempt, feedback.PracticeAttemptID).Error; err != nil {
				return err
			}
			if attempt.SubmittedAt == nil || attempt.Status == "feedback_ready" {
				return errors.New("practice feedback is not retryable")
			}
			if err := tx.Model(&feedback).Updates(map[string]any{"status": "pending", "last_error": "", "updated_at": now}).Error; err != nil {
				return err
			}
			var current []domain.AnswerFeedback
			if err := tx.Select("id", "status").Clauses(clause.Locking{Strength: "UPDATE"}).Where("practice_attempt_id=?", feedback.PracticeAttemptID).Find(&current).Error; err != nil {
				return err
			}
			remainingFailed := 0
			for _, row := range current {
				if row.Status == "failed" {
					remainingFailed++
				}
			}
			updates := map[string]any{"status": "feedback_pending", "last_error": "", "updated_at": now}
			if remainingFailed > 0 {
				updates["status"] = "feedback_failed"
				updates["last_error"] = "other question feedback still requires retry"
			}
			if err := tx.Model(&domain.PracticeAttempt{}).Where("id=? AND submitted_at IS NOT NULL AND status<>'feedback_ready'", feedback.PracticeAttemptID).Updates(updates).Error; err != nil {
				return err
			}
		default:
			return errors.New("unsupported practice task kind")
		}
		if err := tx.Exec("UPDATE ai_tasks SET status='pending',attempt_no=0,execution_generation=execution_generation+1,available_at=?,last_error='',updated_at=? WHERE id=?", now, now, taskID).Error; err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"task_id": taskID})
		return tx.Create(&domain.OutboxEvent{AggregateID: taskID, EventType: "practice.manual_retry", PayloadJSON: string(payload), CreatedAt: now}).Error
	})
}

func (s *Store) CorrectFeedback(ctx context.Context, feedbackID uint64, disposition, comment string) (domain.FeedbackCorrection, error) {
	var correction domain.FeedbackCorrection
	if disposition != "accepted" && disposition != "disputed" && disposition != "uncertain" {
		return correction, errors.New("invalid correction disposition")
	}
	if strings.TrimSpace(comment) == "" || len(comment) > 16000 {
		return correction, errors.New("correction comment must be nonempty and bounded")
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var feedback domain.AnswerFeedback
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='ready'", feedbackID).First(&feedback).Error; err != nil {
			return err
		}
		correction = domain.FeedbackCorrection{AnswerFeedbackID: feedbackID, OriginalItemsJSON: feedback.ItemsJSON, Disposition: disposition, Comment: comment, CreatedAt: time.Now().UTC()}
		return tx.Create(&correction).Error
	})
	return correction, err
}
