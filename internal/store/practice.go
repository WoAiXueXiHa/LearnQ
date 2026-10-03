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

// StartPractice creates an independent attempt; re-practice never overwrites
// answers or feedback belonging to an earlier submission.
func (s *Store) StartPractice(ctx context.Context, setID uint64) (domain.PracticeAttempt, error) {
	var attempt domain.PracticeAttempt
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var set domain.QuestionSet
		if err := tx.Where("id=? AND status='confirmed'", setID).First(&set).Error; err != nil {
			return err
		}
		var document domain.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status IN ('ready','archived')", set.DocumentID).First(&document).Error; err != nil {
			return errors.New("article unavailable for a new practice attempt")
		}
		// Match purge/index activation: article first, then its question set.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND document_id=? AND status='confirmed'", setID, document.ID).First(&set).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&domain.PracticeQuestion{}).Where("question_set_id=?", setID).Count(&count).Error; err != nil {
			return err
		}
		if count != 5 {
			return errors.New("confirmed question set must contain five questions")
		}
		now := time.Now().UTC()
		attempt = domain.PracticeAttempt{QuestionSetID: setID, Status: "in_progress", AnswersJSON: `["","","","",""]`, CreatedAt: now, UpdatedAt: now}
		return tx.Create(&attempt).Error
	})
	return attempt, err
}

func (s *Store) SavePracticeAnswers(ctx context.Context, attemptID uint64, answers []string, submit bool) (domain.PracticeAttempt, error) {
	var attempt domain.PracticeAttempt
	if len(answers) != 5 {
		return attempt, errors.New("exactly five answers are required")
	}
	for _, answer := range answers {
		if len(answer) > 32000 {
			return attempt, errors.New("answer exceeds 32000 bytes")
		}
		if submit && strings.TrimSpace(answer) == "" {
			return attempt, errors.New("all five answers must be nonempty before submission")
		}
	}
	body, err := json.Marshal(answers)
	if err != nil {
		return attempt, err
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var identity struct{ DocumentID uint64 }
		if err := tx.Table("practice_attempts a").Select("q.document_id").Joins("JOIN question_sets q ON q.id=a.question_set_id").Where("a.id=?", attemptID).Take(&identity).Error; err != nil {
			return err
		}
		var document domain.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status IN ('ready','archived')", identity.DocumentID).First(&document).Error; err != nil {
			return errors.New("article unavailable for saving or submitting practice")
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&attempt, attemptID).Error; err != nil {
			return err
		}
		if attempt.Status != "in_progress" {
			return errors.New("submitted answers are immutable")
		}
		now := time.Now().UTC()
		attempt.AnswersJSON = string(body)
		attempt.UpdatedAt = now
		if submit {
			attempt.Status = "feedback_pending"
			attempt.SubmittedAt = &now
			for ordinal := 1; ordinal <= 5; ordinal++ {
				payload, _ := json.Marshal(map[string]any{"practice_attempt_id": attempt.ID, "ordinal": ordinal})
				task := domain.AITask{Kind: "practice_feedback", Status: domain.TaskPending, PayloadJSON: string(payload), ExecutionGeneration: 1, AvailableAt: now, CreatedAt: now, UpdatedAt: now}
				if err := tx.Create(&task).Error; err != nil {
					return err
				}
				feedback := domain.AnswerFeedback{PracticeAttemptID: attempt.ID, Ordinal: ordinal, TaskID: task.ID, Status: "pending", ItemsJSON: "[]", OriginalJSON: "{}", PromptVersion: "practice-feedback-v1", CreatedAt: now, UpdatedAt: now}
				if err := tx.Create(&feedback).Error; err != nil {
					return err
				}
				if err := tx.Create(&domain.OutboxEvent{AggregateID: task.ID, EventType: "practice.feedback", PayloadJSON: string(payload), CreatedAt: now}).Error; err != nil {
					return err
				}
			}
		}
		return tx.Save(&attempt).Error
	})
	return attempt, err
}
