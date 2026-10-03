package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) QueueQuestionSet(ctx context.Context, documentID uint64) (domain.QuestionSet, error) {
	var set domain.QuestionSet
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var document domain.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='ready' AND active_index_id<>0", documentID).First(&document).Error; err != nil {
			return err
		}
		if document.MediaType != "md" && document.MediaType != ".md" && document.MediaType != "text/markdown" {
			return errors.New("practice requires a Markdown article")
		}
		var version domain.DocumentIndex
		if err := tx.Where("id=? AND status='active'", document.ActiveIndexID).First(&version).Error; err != nil {
			return err
		}
		var pending int64
		if err := tx.Model(&domain.ArticleImage{}).Where("index_id=? AND status<>'skipped' AND (image_id IS NULL OR NOT EXISTS (SELECT 1 FROM images i WHERE i.id=article_images.image_id AND i.status='ready') OR NOT EXISTS (SELECT 1 FROM image_evidence e WHERE e.article_image_id=article_images.id AND e.status='ready'))", version.ID).Count(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return errors.New("article image evidence is incomplete")
		}
		now := time.Now().UTC()
		set = domain.QuestionSet{DocumentID: documentID, IndexID: version.ID, Status: "generating", OriginalJSON: "{}", PromptVersion: "five-questions-v2", CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&set).Error; err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"question_set_id": set.ID})
		task := domain.AITask{Kind: "question_generate", Status: domain.TaskPending, PayloadJSON: string(payload), ExecutionGeneration: 1, AvailableAt: now, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		set.GenerationTaskID = task.ID
		if err := tx.Save(&set).Error; err != nil {
			return err
		}
		return tx.Create(&domain.OutboxEvent{AggregateID: task.ID, EventType: "question.generate", PayloadJSON: string(payload), CreatedAt: now}).Error
	})
	return set, err
}

func (s *Store) CompleteQuestionGeneration(ctx context.Context, task domain.AITask, token string, setID uint64, original, model string, questions []domain.QuestionDraft) error {
	if err := validateQuestionDrafts(questions); err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		result := tx.Exec(`UPDATE ai_tasks SET status='succeeded',lease_token='',lease_until=NULL,updated_at=? WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=? AND lease_until>?`, now, task.ID, task.ExecutionGeneration, token, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task lease lost")
		}
		result = tx.Model(&domain.QuestionSet{}).Where("id=? AND generation_task_id=? AND status='generating'", setID, task.ID).Updates(map[string]any{"status": "draft", "original_json": original, "model": model, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("question generation state changed")
		}
		if _, err := (&Store{DB: tx}).EditQuestionSet(ctx, setID, questions, false); err != nil {
			return err
		}
		return tx.Exec("UPDATE task_attempts SET status='succeeded',finished_at=? WHERE task_id=? AND execution_generation=? AND lease_token=?", now, task.ID, task.ExecutionGeneration, token).Error
	})
}

func (s *Store) FailQuestionGeneration(ctx context.Context, task domain.AITask, token string, cause error, terminal bool) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nested := &Store{DB: tx}
		var retry bool
		var err error
		if terminal {
			_, retry, err = nested.FailTerminal(ctx, task, token, cause)
		} else {
			_, retry, err = nested.Fail(ctx, task, token, cause)
		}
		if err != nil {
			return err
		}
		updates := map[string]any{"last_error": cause.Error(), "updated_at": time.Now().UTC()}
		if !retry {
			updates["status"] = "failed"
		}
		return tx.Model(&domain.QuestionSet{}).Where("generation_task_id=? AND status='generating'", task.ID).Updates(updates).Error
	})
}
