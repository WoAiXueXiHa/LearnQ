package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"gorm.io/gorm"
)

type Store struct{ DB *gorm.DB }

func New(db *gorm.DB) *Store { return &Store{DB: db} }

type CreateRecord struct {
	Title              string
	Summary            string
	DurationMinutes    int
	ContentFingerprint string
	Modules            []domain.StudyModule
}

func (s *Store) CreateStudyRecord(ctx context.Context, input CreateRecord) (domain.StudyRecord, domain.AITask, error) {
	var record domain.StudyRecord
	var task domain.AITask
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		record, task, err = CreateStudyRecordTx(tx, input)
		return err
	})
	return record, task, err
}

// CreateStudyRecordTx lets API idempotency and the business write share one
// transaction. Callers must already be inside a transaction.
func CreateStudyRecordTx(tx *gorm.DB, input CreateRecord) (domain.StudyRecord, domain.AITask, error) {
	now := time.Now().UTC()
	record := domain.StudyRecord{
		Title: input.Title, Summary: input.Summary, DurationMinute: input.DurationMinutes,
		ContentFingerprint: input.ContentFingerprint, CreatedAt: now,
	}
	if err := tx.Create(&record).Error; err != nil {
		return record, domain.AITask{}, err
	}
	for i := range input.Modules {
		input.Modules[i].StudyRecordID = record.ID
	}
	if len(input.Modules) > 0 {
		if err := tx.Create(&input.Modules).Error; err != nil {
			return record, domain.AITask{}, err
		}
	}
	record.Modules = input.Modules
	payload, _ := json.Marshal(map[string]any{"study_record_id": record.ID})
	task := domain.AITask{Kind: "study_report", Status: domain.TaskPending, PayloadJSON: string(payload), ExecutionGeneration: 1, AvailableAt: now, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&task).Error; err != nil {
		return record, task, err
	}
	event := domain.OutboxEvent{AggregateID: task.ID, EventType: "ai_task.created", PayloadJSON: string(payload), CreatedAt: now}
	return record, task, tx.Create(&event).Error
}

// DispatchOutbox holds row locks until Redis has accepted each idempotent ZADD and
// the task/outbox state is committed. A crash between stores is repaired by replay.
func (s *Store) DispatchOutbox(ctx context.Context, enqueue func(context.Context, uint64, time.Time) error, limit int) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []domain.OutboxEvent
		if err := tx.Raw(`SELECT * FROM outbox_events WHERE published_at IS NULL ORDER BY id LIMIT ? FOR UPDATE SKIP LOCKED`, limit).Scan(&events).Error; err != nil {
			return err
		}
		for _, event := range events {
			var task domain.AITask
			if err := tx.First(&task, event.AggregateID).Error; err != nil {
				return err
			}
			if err := enqueue(ctx, task.ID, task.AvailableAt); err != nil {
				return err
			}
			now := time.Now().UTC()
			if err := tx.Exec(`UPDATE ai_tasks SET status='queued', updated_at=? WHERE id=? AND status IN ('pending','retry_wait')`, now, task.ID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE outbox_events SET published_at=? WHERE id=? AND published_at IS NULL`, now, event.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Acquire(ctx context.Context, id uint64, token string, leaseUntil time.Time) (domain.AITask, bool, error) {
	now := time.Now().UTC()
	var task domain.AITask
	acquired := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`UPDATE ai_tasks SET status='processing', lease_token=?, lease_until=?,
			attempt_no=attempt_no+1, updated_at=? WHERE id=? AND status='queued'`, token, leaseUntil, now, id)
		if result.Error != nil || result.RowsAffected != 1 {
			return result.Error
		}
		if err := tx.First(&task, id).Error; err != nil {
			return err
		}
		attempt := domain.TaskAttempt{TaskID: id, ExecutionGeneration: task.ExecutionGeneration, AttemptNo: task.AttemptNo, LeaseToken: token, Status: "processing", StartedAt: now}
		if err := tx.Create(&attempt).Error; err != nil {
			return err
		}
		acquired = true
		return nil
	})
	return task, acquired, err
}

func (s *Store) Complete(ctx context.Context, task domain.AITask, token, markdown string) (domain.Report, error) {
	var report domain.Report
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		// The token and generation are fencing conditions: an expired worker cannot
		// overwrite the valid result produced by a later lease.
		result := tx.Exec(`UPDATE ai_tasks SET status='succeeded', lease_token='', lease_until=NULL, updated_at=?
			WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=? AND lease_until>=?`,
			now, task.ID, task.ExecutionGeneration, token, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task lease lost")
		}
		report = domain.Report{TaskID: task.ID, MarkdownContent: markdown, ExportStatus: "pending", CreatedAt: now}
		if err := tx.Create(&report).Error; err != nil {
			return err
		}
		interval, _ := domain.ReviewInterval(0)
		review := domain.ReviewTask{ReportID: report.ID, Mastery: 0, Status: "scheduled", DueAt: now.Add(interval), CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&review).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE task_attempts SET status='succeeded', finished_at=? WHERE task_id=? AND execution_generation=? AND lease_token=?`,
			now, task.ID, task.ExecutionGeneration, token).Error
	})
	return report, err
}

func (s *Store) Fail(ctx context.Context, task domain.AITask, token string, cause error) (time.Time, bool, error) {
	delay, retry := domain.RetryDelay(task.AttemptNo)
	status := domain.TaskDead
	available := time.Now().UTC()
	if retry {
		status = domain.TaskRetryWait
		available = available.Add(delay)
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		result := tx.Exec(`UPDATE ai_tasks SET status=?, available_at=?, lease_token='', lease_until=NULL,
			last_error=?, updated_at=? WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=?`,
			status, available, cause.Error(), now, task.ID, task.ExecutionGeneration, token)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task lease lost")
		}
		if retry {
			payload, _ := json.Marshal(map[string]any{"task_id": task.ID})
			event := domain.OutboxEvent{AggregateID: task.ID, EventType: "ai_task.retry", PayloadJSON: string(payload), CreatedAt: now}
			if err := tx.Create(&event).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`UPDATE task_attempts SET status=?, error_message=?, finished_at=? WHERE task_id=? AND execution_generation=? AND lease_token=?`,
			status, cause.Error(), now, task.ID, task.ExecutionGeneration, token).Error
	})
	return available, retry, err
}

func HashRequest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateDocument(ctx context.Context, document domain.Document) (domain.Document, domain.AITask, error) {
	var task domain.AITask
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		document.Status = "uploaded"
		document.CreatedAt = now
		document.UpdatedAt = now
		if err := tx.Create(&document).Error; err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"document_id": document.ID})
		task = domain.AITask{Kind: "document_index", Status: domain.TaskPending, PayloadJSON: string(payload), ExecutionGeneration: 1, AvailableAt: now, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		document.IndexingTaskID = task.ID
		if err := tx.Model(&document).Update("indexing_task_id", task.ID).Error; err != nil {
			return err
		}
		return tx.Create(&domain.OutboxEvent{AggregateID: task.ID, EventType: "document.index", PayloadJSON: string(payload), CreatedAt: now}).Error
	})
	return document, task, err
}

func (s *Store) CompleteWithoutReport(ctx context.Context, task domain.AITask, token string) error {
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`UPDATE ai_tasks SET status='succeeded',lease_token='',lease_until=NULL,updated_at=?
			WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=? AND lease_until>=?`,
			now, task.ID, task.ExecutionGeneration, token, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task lease lost")
		}
		return tx.Exec(`UPDATE task_attempts SET status='succeeded',finished_at=?
			WHERE task_id=? AND execution_generation=? AND lease_token=?`, now, task.ID, task.ExecutionGeneration, token).Error
	})
}
