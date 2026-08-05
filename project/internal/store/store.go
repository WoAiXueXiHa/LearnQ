// MySQL 操作层，写 Task、写Outbox、查任务、改状态

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

// Store 封装 *gorm.DB，所有 MySQL 读写都通过它执行；字段导出供需要直接操作 DB 的调用方使用。
type Store struct{ DB *gorm.DB }

func New(db *gorm.DB) *Store { return &Store{DB: db} }

// CreateRecord 是创建学习记录的输入参数：标题、摘要、时长、内容指纹与可选的模块列表。
type CreateRecord struct {
	Title              string
	Summary            string
	DurationMinutes    int
	ContentFingerprint string
	Modules            []domain.StudyModule
}

// CreateStudyRecord 自行开启事务，把学习记录、AI 任务与 Outbox 事件一次提交。
// 与 CreateStudyRecordTx 的区别：本函数管理事务边界，Tx 版本由调用方传入事务。
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
	// 时间统一取 UTC 落库，避免服务器时区影响排序与到期计算。
	now := time.Now().UTC()
	record := domain.StudyRecord{
		Title:              input.Title,
		Summary:            input.Summary,
		DurationMinute:     input.DurationMinutes,
		ContentFingerprint: input.ContentFingerprint,
		CreatedAt:          now,
	}
	// 1. 事务内创建 StudyRecord
	if err := tx.Create(&record).Error; err != nil {
		return record, domain.AITask{}, err
	}
	// 批量插入前回填父记录外键，GORM 不会从关联关系自动推导。
	for i := range input.Modules {
		input.Modules[i].StudyRecordID = record.ID
	}
	// 空切片直接 Create 会返回 ErrEmptySlice，需显式跳过。
	if len(input.Modules) > 0 {
		// 2. 事务内创建一条 StudyRecord 的明细
		if err := tx.Create(&input.Modules).Error; err != nil {
			return record, domain.AITask{}, err
		}
	}
	record.Modules = input.Modules
	// 结构固定的 map 序列化不会失败，错误可安全忽略。
	payload, _ := json.Marshal(map[string]any{"study_record_id": record.ID})
	// 新任务从第 1 代开始执行；人工重试时 generation 递增，使旧租约的 fencing token 失效。
	task := domain.AITask{
		Kind:                "study_report",
		Status:              domain.TaskPending,
		PayloadJSON:         string(payload),
		ExecutionGeneration: 1,
		AvailableAt:         now,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	// 3. 事务内创建 AITask
	if err := tx.Create(&task).Error; err != nil {
		return record, task, err
	}
	event := domain.OutboxEvent{
		AggregateID: task.ID,
		EventType:   "ai_task.created",
		PayloadJSON: string(payload),
		CreatedAt:   now,
	}
	// 4. 事务内创建 OutboxEvent
	return record, task, tx.Create(&event).Error
}

// DispatchOutbox 使用 SKIP LOCKED 让多个 Dispatcher 分摊事件而不重复争抢同一行。
// Redis ZADD 本身幂等；若进程在 Redis 成功后、MySQL 提交前崩溃，未发布事件会被再次投递，
// 因而这里选择“至少一次投递 + 幂等入队”，而不是追求跨存储的伪原子事务。
func (s *Store) DispatchOutbox(ctx context.Context, enqueue func(context.Context, uint64, time.Time) error, limit int) error {
	// 投递和保存是两个不同的动作，投递自己新开一个事务
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []domain.OutboxEvent
		// 按 id 升序取最旧的未发布事件；SKIP LOCKED 让多个 Dispatcher 并发分摊而互不阻塞。
		if err := tx.Raw(`SELECT * FROM outbox_events WHERE published_at IS NULL ORDER BY id LIMIT ? FOR UPDATE SKIP LOCKED`, limit).Scan(&events).Error; err != nil {
			return err
		}
		for _, event := range events {
			var task domain.AITask
			if err := tx.First(&task, event.AggregateID).Error; err != nil {
				return err
			}
			// 入队失败则整个事务回滚，事件保持未发布状态留待下一轮投递。
			if err := enqueue(ctx, task.ID, task.AvailableAt); err != nil {
				return err
			}
			now := time.Now().UTC()
			// 条件更新：仅当任务仍处于 pending/retry_wait 时才置 queued；若已被并发投递，
			// RowsAffected 为 0，整个事务回滚让事件留待重试。
			result := tx.Exec(`UPDATE ai_tasks SET status='queued', updated_at=? WHERE id=? AND status IN ('pending','retry_wait')`, now, task.ID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("task state changed while dispatching")
			}
			// published_at 仅在仍为空时写入，同一事件只标记一次发布。
			if err := tx.Exec(`UPDATE outbox_events SET published_at=? WHERE id=? AND published_at IS NULL`, now, event.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Acquire 是领取的最终确权：queued → processing 并记录租约与 Attempt；返回的 bool 表示本次是否确权成功。
func (s *Store) Acquire(ctx context.Context, id uint64, token string, leaseUntil time.Time) (domain.AITask, bool, error) {
	// Redis Claim 只取得调度资格；这里用 status='queued' 的条件更新在 MySQL 中最终确权。
	// 只有确权成功才创建 Attempt，避免重复消息导致同一任务被并发执行。
	now := time.Now().UTC()
	var task domain.AITask
	acquired := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// attempt_no 在条件更新中原子递增，与下方的 Attempt 审计记录同事务写入。
		result := tx.Exec(`UPDATE ai_tasks SET status='processing', lease_token=?, lease_until=?,
			attempt_no=attempt_no+1, updated_at=? WHERE id=? AND status='queued'`, token, leaseUntil, now, id)
		// RowsAffected 为 0 说明状态已不是 queued（任务已被其他 Worker 领取），acquired 保持 false。
		if result.Error != nil || result.RowsAffected != 1 {
			return result.Error
		}
		if err := tx.First(&task, id).Error; err != nil {
			return err
		}
		// 每次确权都落一条 Attempt 审计记录，构成不可丢的执行历史。
		attempt := domain.TaskAttempt{TaskID: id, ExecutionGeneration: task.ExecutionGeneration, AttemptNo: task.AttemptNo, LeaseToken: token, Status: "processing", StartedAt: now}
		if err := tx.Create(&attempt).Error; err != nil {
			return err
		}
		acquired = true
		return nil
	})
	return task, acquired, err
}

// Complete 提交成功结果：任务置 succeeded、生成报告与首条复习计划，全部在同一事务中提交。
func (s *Store) Complete(ctx context.Context, task domain.AITask, token, markdown string) (domain.Report, error) {
	var report domain.Report
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		// token、generation 和未过期 lease 是三重栅栏：旧 Worker 即使晚到，
		// 也不能覆盖新租约已经产生的有效结果。
		result := tx.Exec(`UPDATE ai_tasks SET status='succeeded', lease_token='', lease_until=NULL, updated_at=?
			WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=? AND lease_until>?`,
			now, task.ID, task.ExecutionGeneration, token, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task lease lost")
		}
		// 报告先以 pending 导出状态落库，导出动作由报告管线异步完成。
		report = domain.Report{TaskID: task.ID, MarkdownContent: markdown, ExportStatus: "pending", CreatedAt: now}
		if err := tx.Create(&report).Error; err != nil {
			return err
		}
		// ReviewInterval(0) 恒合法故忽略错误；掌握度从 0 起步，首轮复习间隔为 1 天。
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

// Fail 是常规失败入口：按尝试次数决定进入 retry_wait（写 Outbox 重试）还是 dead。
// 返回值为下次可用时间与是否可重试。
func (s *Store) Fail(ctx context.Context, task domain.AITask, token string, cause error) (time.Time, bool, error) {
	return s.fail(ctx, task, token, cause, false, false, false)
}

// FailTerminal 持久化不可重试错误（如凭证无效或请求参数被供应商拒绝），
// 直接进入 dead，避免浪费后续尝试和外部调用额度。
func (s *Store) FailTerminal(ctx context.Context, task domain.AITask, token string, cause error) (time.Time, bool, error) {
	return s.fail(ctx, task, token, cause, false, true, false)
}

// FailDocument 在同一事务中修改任务和文档：可重试时文档回到 uploaded，
// 终止失败时对外显示 failed，避免两个聚合暴露互相矛盾的状态。
func (s *Store) FailDocument(ctx context.Context, task domain.AITask, token string, cause error, terminal bool) (time.Time, bool, error) {
	return s.fail(ctx, task, token, cause, true, terminal, false)
}

// FailExpired is reserved for the Reaper and can only win after the lease deadline.
func (s *Store) FailExpired(ctx context.Context, task domain.AITask, token string, cause error) (time.Time, bool, error) {
	return s.fail(ctx, task, token, cause, false, false, true)
}

// FailExpiredDocument 供 Reaper 使用：仅接受租约已过期的失败提交，并在同一事务内同步文档状态。
func (s *Store) FailExpiredDocument(ctx context.Context, task domain.AITask, token string, cause error) (time.Time, bool, error) {
	return s.fail(ctx, task, token, cause, true, false, true)
}

// fail 是全部失败路径的公共实现。forceTerminal 跳过重试直接 dead；
// requireExpired 仅允许租约已过期的提交（Reaper 专用）。
func (s *Store) fail(ctx context.Context, task domain.AITask, token string, cause error, document, forceTerminal, requireExpired bool) (time.Time, bool, error) {
	if cause == nil {
		cause = errors.New("unknown error")
	}
	delay, retry := domain.RetryDelay(task.AttemptNo)
	if forceTerminal {
		retry = false
	}
	// 可重试：进 retry_wait 并把可用时间推迟 delay；不可重试：直接 dead。
	status := domain.TaskDead
	available := time.Now().UTC()
	if retry {
		status = domain.TaskRetryWait
		available = available.Add(delay)
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		// 常规失败要求租约仍有效，Reaper 则要求租约已过期；两条路径共用一条 SQL 模板。
		leasePredicate := "lease_until>?"
		if requireExpired {
			leasePredicate = "lease_until<=?"
		}
		result := tx.Exec(`UPDATE ai_tasks SET status=?, available_at=?, lease_token='', lease_until=NULL,
			last_error=?, updated_at=? WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=? AND `+leasePredicate,
			status, available, cause.Error(), now, task.ID, task.ExecutionGeneration, token, now)
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
			// 已 ready 或正在删除的文档不回退，防止把成功状态打成失败。
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
		// Attempt 行按 generation + token 精确匹配，保证只闭合本次执行的那条审计记录。
		return tx.Exec(`UPDATE task_attempts SET status=?, error_message=?, finished_at=? WHERE task_id=? AND execution_generation=? AND lease_token=?`,
			status, cause.Error(), now, task.ID, task.ExecutionGeneration, token).Error
	})
	return available, retry, err
}

// HashRequest 对请求体取 SHA-256 的十六进制串，用于幂等键去重。
func HashRequest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// CreateDocument 持久化文档并创建 document_index 任务，同一事务写入 Outbox。
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
		// 先创建任务拿到 ID，再回填文档外键；同事务完成，不会出现悬空任务。
		document.IndexingTaskID = task.ID
		if err := tx.Model(&document).Update("indexing_task_id", task.ID).Error; err != nil {
			return err
		}
		return tx.Create(&domain.OutboxEvent{AggregateID: task.ID, EventType: "document.index", PayloadJSON: string(payload), CreatedAt: now}).Error
	})
	return document, task, err
}

// CompleteDocumentIndex 提交文档索引成功结果：校验租约后在同一事务内置任务 succeeded 与文档 ready。
func (s *Store) CompleteDocumentIndex(ctx context.Context, task domain.AITask, token string, documentID uint64) error {
	// 任务成功与文档 ready 必须原子提交，否则检索端可能读取到尚未完整落库的切片。
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`UPDATE ai_tasks SET status='succeeded',lease_token='',lease_until=NULL,updated_at=?
			WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=? AND lease_until>?`,
			now, task.ID, task.ExecutionGeneration, token, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task lease lost")
		}
		// 文档必须处于 indexing 才能置 ready；删除等并发操作会改变状态使本更新落空并回滚。
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

// documentIDFromTask 从任务 payload 解析 document_id；字段缺失或为 0 视为非法 payload。
func documentIDFromTask(task domain.AITask) (uint64, error) {
	var payload struct {
		DocumentID uint64 `json:"document_id"`
	}
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil || payload.DocumentID == 0 {
		return 0, errors.New("invalid document task payload")
	}
	return payload.DocumentID, nil
}
