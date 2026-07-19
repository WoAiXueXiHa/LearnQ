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

// CreateStudyRecordTx 在调用方已有事务中创建学习记录、AI 任务和 Outbox 事件。
// API 的幂等键与这些业务写入因此可以共享一次提交：响应被记住时，任务也一定已经存在。
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

// DispatchOutbox 使用 SKIP LOCKED 让多个 Dispatcher 分摊事件而不重复争抢同一行。
// Redis ZADD 本身幂等；若进程在 Redis 成功后、MySQL 提交前崩溃，未发布事件会被再次投递，
// 因而这里选择“至少一次投递 + 幂等入队”，而不是追求跨存储的伪原子事务。
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
			result := tx.Exec(`UPDATE ai_tasks SET status='queued', updated_at=? WHERE id=? AND status IN ('pending','retry_wait')`, now, task.ID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("task state changed while dispatching")
			}
			if err := tx.Exec(`UPDATE outbox_events SET published_at=? WHERE id=? AND published_at IS NULL`, now, event.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Acquire(ctx context.Context, id uint64, token string, leaseUntil time.Time) (domain.AITask, bool, error) {
	// Redis Claim 只取得调度资格；这里用 status='queued' 的条件更新在 MySQL 中最终确权。
	// 只有确权成功才创建 Attempt，避免重复消息导致同一任务被并发执行。
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
		// token、generation 和未过期 lease 是三重栅栏：旧 Worker 即使晚到，
		// 也不能覆盖新租约已经产生的有效结果。
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
	return s.fail(ctx, task, token, cause, false, false)
}

// FailTerminal 持久化不可重试错误（如凭证无效或请求参数被供应商拒绝），
// 直接进入 dead，避免浪费后续尝试和外部调用额度。
func (s *Store) FailTerminal(ctx context.Context, task domain.AITask, token string, cause error) (time.Time, bool, error) {
	return s.fail(ctx, task, token, cause, false, true)
}

// FailDocument 在同一事务中修改任务和文档：可重试时文档回到 uploaded，
// 终止失败时对外显示 failed，避免两个聚合暴露互相矛盾的状态。
func (s *Store) FailDocument(ctx context.Context, task domain.AITask, token string, cause error, terminal bool) (time.Time, bool, error) {
	return s.fail(ctx, task, token, cause, true, terminal)
}

func (s *Store) fail(ctx context.Context, task domain.AITask, token string, cause error, document, forceTerminal bool) (time.Time, bool, error) {
	if cause == nil {
		cause = errors.New("unknown error")
	}
	delay, retry := domain.RetryDelay(task.AttemptNo)
	if forceTerminal {
		retry = false
	}
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
		if document {
			documentID, err := documentIDFromTask(task)
			if err != nil {
				return err
			}
			documentStatus := "failed"
			errorMessage := cause.Error()
			if retry {
				documentStatus = "uploaded"
				errorMessage = ""
			}
			if err := tx.Model(&domain.Document{}).
				Where("id=? AND indexing_task_id=? AND status NOT IN ('ready','deleting')", documentID, task.ID).
				Updates(map[string]any{"status": documentStatus, "error_message": errorMessage, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if retry {
			// 重试不直接写 Redis，而是再次写 Outbox，复用同一条可靠投递路径。
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
	// 原文、索引任务和 Outbox 一次提交；客户端拿到 202 时即可确信异步索引意图不会丢失。
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

func (s *Store) CompleteDocumentIndex(ctx context.Context, task domain.AITask, token string, documentID uint64) error {
	// 任务成功与文档 ready 必须原子提交，否则检索端可能读取到尚未完整落库的切片。
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
		documentResult := tx.Exec(`UPDATE documents SET status='ready',error_message='',updated_at=?
			WHERE id=? AND indexing_task_id=? AND status='indexing'`, now, documentID, task.ID)
		if documentResult.Error != nil {
			return documentResult.Error
		}
		if documentResult.RowsAffected != 1 {
			return errors.New("document state changed before index completion")
		}
		return tx.Exec(`UPDATE task_attempts SET status='succeeded',finished_at=?
			WHERE task_id=? AND execution_generation=? AND lease_token=?`, now, task.ID, task.ExecutionGeneration, token).Error
	})
}

func documentIDFromTask(task domain.AITask) (uint64, error) {
	var payload struct {
		DocumentID uint64 `json:"document_id"`
	}
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil || payload.DocumentID == 0 {
		return 0, errors.New("invalid document task payload")
	}
	return payload.DocumentID, nil
}
