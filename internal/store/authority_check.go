package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) QueueAuthorityCheck(ctx context.Context, claimID, snapshotID uint64, excerpt, note string) (domain.AuthorityCheck, error) {
	var check domain.AuthorityCheck
	if strings.TrimSpace(excerpt) == "" || len(excerpt) > 16000 || strings.TrimSpace(note) == "" || len(note) > 8000 {
		return check, errors.New("bounded excerpt and version/context note are required")
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var identity struct{ DocumentID uint64 }
		if err := tx.Table("authority_claims c").Select("q.document_id").Joins("JOIN answer_feedback f ON f.id=c.answer_feedback_id JOIN practice_attempts a ON a.id=f.practice_attempt_id JOIN question_sets q ON q.id=a.question_set_id").Where("c.id=?", claimID).Take(&identity).Error; err != nil {
			return err
		}
		var document domain.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status IN ('ready','archived')", identity.DocumentID).First(&document).Error; err != nil {
			return errors.New("article unavailable for authority check")
		}
		var claim domain.AuthorityClaim
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&claim, claimID).Error; err != nil {
			return err
		}
		var snapshot domain.AuthoritySnapshot
		if err := tx.First(&snapshot, snapshotID).Error; err != nil {
			return err
		}
		if err := ValidateAuthorityEvidence(snapshot, excerpt); err != nil {
			return err
		}
		key := authorityRequestKey(claimID, snapshotID, excerpt, note)
		// Use a current read after the article/claim locks. Under MySQL's
		// REPEATABLE READ, the earlier identity lookup can establish a snapshot
		// that predates a concurrent winner's commit.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("idempotency_key=?", key).First(&check).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().UTC()
		payload, _ := json.Marshal(map[string]any{"claim_id": claimID, "snapshot_id": snapshotID})
		task := domain.AITask{Kind: "authority_check", Status: domain.TaskPending, PayloadJSON: string(payload), ExecutionGeneration: 1, AvailableAt: now, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		check = domain.AuthorityCheck{IdempotencyKey: &key, AuthorityClaimID: claimID, AuthoritySnapshotID: snapshotID, Excerpt: excerpt, ContextNote: note, Status: "pending", Judgment: "uncertain", TaskID: task.ID, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&check).Error; err != nil {
			return err
		}
		return tx.Create(&domain.OutboxEvent{AggregateID: task.ID, EventType: "authority.check", PayloadJSON: string(payload), CreatedAt: now}).Error
	})
	return check, err
}

func ValidateAuthoritySnapshot(snapshot domain.AuthoritySnapshot) error {
	if strings.TrimSpace(snapshot.VersionLabel) == "" || !snapshot.ExpiresAt.After(time.Now().UTC()) {
		return errors.New("authority version unknown or snapshot expired")
	}
	raw := sha256.Sum256([]byte(snapshot.Content))
	text := sha256.Sum256([]byte(snapshot.ExtractedText))
	if hex.EncodeToString(raw[:]) != snapshot.ContentHash || hex.EncodeToString(text[:]) != snapshot.TextHash {
		return errors.New("authority snapshot hash mismatch")
	}
	return nil
}

func (s *Store) CompleteAuthorityCheck(ctx context.Context, task domain.AITask, token, judgment, explanation, model, originalResponse string) error {
	if judgment != "supported" && judgment != "refuted" && judgment != "uncertain" {
		return invalidResult("invalid authority judgment")
	}
	if strings.TrimSpace(explanation) == "" || len(explanation) > 16000 {
		return invalidResult("invalid authority explanation")
	}
	if len(originalResponse) > 256*1024 || !json.Valid([]byte(originalResponse)) {
		return invalidResult("invalid authority raw response")
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var check domain.AuthorityCheck
		if err := tx.Where("task_id=? AND status='pending'", task.ID).First(&check).Error; err != nil {
			return err
		}
		var snapshot domain.AuthoritySnapshot
		if err := tx.First(&snapshot, check.AuthoritySnapshotID).Error; err != nil {
			return err
		}
		if err := ValidateAuthorityEvidence(snapshot, check.Excerpt); err != nil {
			return invalidResult(err.Error())
		}
		now := time.Now().UTC()
		result := tx.Exec("UPDATE ai_tasks SET status='succeeded',lease_token='',lease_until=NULL,updated_at=? WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=? AND lease_until>?", now, task.ID, task.ExecutionGeneration, token, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task lease lost")
		}
		if err := tx.Model(&check).Updates(map[string]any{"status": "ready", "judgment": judgment, "explanation": explanation, "model": model, "original_response": originalResponse, "updated_at": now}).Error; err != nil {
			return err
		}
		// The claim retains pending verification: a model check is inspectable
		// evidence, and does not silently rewrite the original learner feedback.
		return tx.Exec("UPDATE task_attempts SET status='succeeded',finished_at=? WHERE task_id=? AND execution_generation=? AND lease_token=?", now, task.ID, task.ExecutionGeneration, token).Error
	})
}

// ValidateAuthorityEvidence checks the immutable source before calls and confirmations.
func ValidateAuthorityEvidence(snapshot domain.AuthoritySnapshot, excerpt string) error {
	if err := ValidateAuthoritySnapshot(snapshot); err != nil {
		return err
	}
	if strings.TrimSpace(excerpt) == "" || len(excerpt) > 16000 || !strings.Contains(snapshot.ExtractedText, excerpt) {
		return errors.New("excerpt is absent from source snapshot or invalid")
	}
	return nil
}

// Delimit fields with JSON so concatenation or embedded separators cannot alias.
func authorityRequestKey(claimID, snapshotID uint64, excerpt, note string) string {
	body, _ := json.Marshal([]any{"authority-request-v1", claimID, snapshotID, excerpt, note})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
