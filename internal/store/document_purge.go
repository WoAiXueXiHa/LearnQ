package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type DocumentDeletion struct {
	ID           uint64     `json:"id"`
	DocumentID   uint64     `json:"document_id"`
	Status       string     `json:"status"`
	ClaimToken   string     `json:"-"`
	ClaimUntil   *time.Time `json:"-"`
	QuiesceUntil time.Time  `json:"quiesce_until"`
	PlanHash     string     `json:"plan_hash"`
	PlanJSON     string     `json:"-" gorm:"column:plan_json"`
	ImageIDsJSON string     `json:"-" gorm:"column:image_ids_json"`
	LastError    string     `json:"last_error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// DeletionScope contains only fixed SQL fragments, never user supplied names.
func deletionScope(documentID uint64) ([]uint64, map[string]string) {
	sets := "SELECT id FROM question_sets WHERE document_id=?"
	attempts := "SELECT id FROM practice_attempts WHERE question_set_id IN (" + sets + ")"
	feedback := "SELECT id FROM answer_feedback WHERE practice_attempt_id IN (" + attempts + ")"
	claims := "SELECT id FROM authority_claims WHERE answer_feedback_id IN (" + feedback + ")"
	checks := "SELECT id FROM authority_checks WHERE authority_claim_id IN (" + claims + ")"
	return []uint64{documentID}, map[string]string{
		"question_sets": "document_id=?", "practice_questions": "question_set_id IN (" + sets + ")", "question_set_edits": "question_set_id IN (" + sets + ")",
		"practice_attempts": "question_set_id IN (" + sets + ")", "answer_feedback": "practice_attempt_id IN (" + attempts + ")", "feedback_corrections": "answer_feedback_id IN (" + feedback + ")", "practice_reviews": "practice_attempt_id IN (" + attempts + ")",
		"authority_claims": "answer_feedback_id IN (" + feedback + ")", "authority_checks": "authority_claim_id IN (" + claims + ")", "authority_check_reviews": "authority_check_id IN (" + checks + ")",
		"image_description_corrections": "document_id=?", "article_images": "document_id=?", "image_evidence": "document_id=?", "document_indexes": "document_id=?", "document_chunks": "document_id=?",
	}
}

func (s *Store) DocumentDeletionPlan(ctx context.Context, id uint64) (map[string]any, string, error) {
	var document domain.Document
	if err := s.DB.WithContext(ctx).First(&document, id).Error; err != nil {
		return nil, "", err
	}
	_, scopes := deletionScope(id)
	counts := map[string]int64{}
	for table, scope := range scopes {
		var count int64
		if err := s.DB.WithContext(ctx).Table(table).Where(scope, id).Count(&count).Error; err != nil {
			return nil, "", err
		}
		counts[table] = count
	}
	plan := map[string]any{"document_id": id, "article_sha256": document.ContentHash, "active_index_id": document.ActiveIndexID, "counts": counts, "backups": "existing backups require separate retention expiry; live deletion does not erase them", "shared_images": "retained when referenced by another article or record"}
	body, err := json.Marshal(plan)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return plan, hex.EncodeToString(sum[:]), nil
}

func (s *Store) BeginDocumentPurge(ctx context.Context, id uint64, expected string) (DocumentDeletion, error) {
	var job DocumentDeletion
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("document_id=?", id).First(&job).Error; err == nil {
			if job.PlanHash != expected {
				return errors.New("deletion confirmation does not match existing job")
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var document domain.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&document, id).Error; err != nil {
			return err
		}
		plan, hash, err := (&Store{DB: tx}).DocumentDeletionPlan(ctx, id)
		if err != nil {
			return err
		}
		if expected != hash {
			return errors.New("deletion impact changed; review a fresh deletion plan")
		}
		var imageIDs []uint64
		if err := tx.Model(&domain.ArticleImage{}).Where("document_id=? AND image_id IS NOT NULL", id).Distinct("image_id").Pluck("image_id", &imageIDs).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		planJSON, _ := json.Marshal(plan)
		imagesJSON, _ := json.Marshal(imageIDs)
		job = DocumentDeletion{DocumentID: id, Status: "quiescing", QuiesceUntil: now.Add(70 * time.Second), PlanHash: hash, PlanJSON: string(planJSON), ImageIDsJSON: string(imagesJSON), CreatedAt: now, UpdatedAt: now}
		taskSQL := `SELECT indexing_task_id FROM documents WHERE id=? UNION SELECT generation_task_id FROM question_sets WHERE document_id=? UNION SELECT task_id FROM answer_feedback WHERE practice_attempt_id IN (SELECT id FROM practice_attempts WHERE question_set_id IN (SELECT id FROM question_sets WHERE document_id=?)) UNION SELECT task_id FROM authority_checks WHERE authority_claim_id IN (SELECT id FROM authority_claims WHERE answer_feedback_id IN (SELECT id FROM answer_feedback WHERE practice_attempt_id IN (SELECT id FROM practice_attempts WHERE question_set_id IN (SELECT id FROM question_sets WHERE document_id=?))))`
		var taskIDs []uint64
		if err := tx.Raw(taskSQL, id, id, id, id).Scan(&taskIDs).Error; err != nil {
			return err
		}
		if len(taskIDs) > 0 {
			var tasks []domain.AITask
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", taskIDs).Find(&tasks).Error; err != nil {
				return err
			}
			for _, task := range tasks {
				if task.LeaseUntil != nil && task.LeaseUntil.Add(10*time.Second).After(job.QuiesceUntil) {
					job.QuiesceUntil = task.LeaseUntil.Add(10 * time.Second)
				}
			}
			if err := tx.Model(&domain.TaskAttempt{}).Where("task_id IN ? AND status='processing'", taskIDs).Updates(map[string]any{"status": "dead", "error_message": "article permanently deleted", "finished_at": now}).Error; err != nil {
				return err
			}
			if err := tx.Model(&domain.AITask{}).Where("id IN ? AND status NOT IN ('succeeded','dead')", taskIDs).Updates(map[string]any{"status": "dead", "lease_token": "", "lease_until": nil, "execution_generation": gorm.Expr("execution_generation+1"), "last_error": "article permanently deleted", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		return tx.Model(&document).Updates(map[string]any{"status": "deleting", "updated_at": now}).Error
	})
	return job, err
}

func (s *Store) PurgeDocumentRows(ctx context.Context, job DocumentDeletion) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current DocumentDeletion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, job.ID).Error; err != nil {
			return err
		}
		if current.Status != "vectors_pending" {
			return nil
		}
		if job.ClaimToken == "" || current.ClaimToken != job.ClaimToken || current.ClaimUntil == nil || !current.ClaimUntil.After(time.Now().UTC()) {
			return errors.New("deletion claim expired")
		}
		id := job.DocumentID
		_, scopes := deletionScope(id)
		// Close all related task generations before removing their owning facts.
		taskSQL := `SELECT indexing_task_id FROM documents WHERE id=? UNION SELECT generation_task_id FROM question_sets WHERE document_id=? UNION SELECT task_id FROM answer_feedback WHERE practice_attempt_id IN (SELECT id FROM practice_attempts WHERE question_set_id IN (SELECT id FROM question_sets WHERE document_id=?)) UNION SELECT task_id FROM authority_checks WHERE authority_claim_id IN (SELECT id FROM authority_claims WHERE answer_feedback_id IN (SELECT id FROM answer_feedback WHERE practice_attempt_id IN (SELECT id FROM practice_attempts WHERE question_set_id IN (SELECT id FROM question_sets WHERE document_id=?))))`
		var taskIDs []uint64
		if err := tx.Raw(taskSQL, id, id, id, id).Scan(&taskIDs).Error; err != nil {
			return err
		}
		if len(taskIDs) > 0 {
			now := time.Now().UTC()
			if err := tx.Model(&domain.TaskAttempt{}).Where("task_id IN ? AND status='processing'", taskIDs).Updates(map[string]any{"status": "dead", "error_message": "article permanently deleted", "finished_at": now}).Error; err != nil {
				return err
			}
			if err := tx.Model(&domain.AITask{}).Where("id IN ? AND status NOT IN ('succeeded','dead')", taskIDs).Updates(map[string]any{"status": "dead", "lease_token": "", "lease_until": nil, "execution_generation": gorm.Expr("execution_generation+1"), "last_error": "article permanently deleted", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if len(taskIDs) > 0 {
			if err := tx.Where("task_id IN ?", taskIDs).Delete(&PracticeModelResult{}).Error; err != nil {
				return err
			}
		}
		for _, table := range []string{"authority_check_reviews", "authority_checks", "authority_claims", "feedback_corrections", "answer_feedback", "practice_reviews", "practice_attempts", "question_set_edits", "practice_questions", "question_sets", "image_evidence", "image_description_corrections", "article_images", "document_chunks", "document_indexes"} {
			if err := tx.Exec("DELETE FROM "+table+" WHERE "+scopes[table], id).Error; err != nil {
				return err
			}
		}
		if err := tx.Delete(&domain.Document{}, id).Error; err != nil {
			return err
		}
		return tx.Model(&current).Updates(map[string]any{"status": "images_pending", "last_error": "", "updated_at": time.Now().UTC()}).Error
	})
}
