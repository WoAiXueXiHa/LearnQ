package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateImage 持久化上传的图片并创建 image_describe 任务与 Outbox 事件，一次事务提交。
func (s *Store) CreateImage(ctx context.Context, image domain.Image) (domain.Image, domain.AITask, error) {
	var task domain.AITask
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		// 新图片统一以 uploaded 状态和空描述占位落库，描述内容由 Worker 填充。
		image.Status = "uploaded"
		image.DescriptionJSON = "{}"
		image.DescriptionModel = ""
		image.LastError = ""
		image.CreatedAt = now
		image.UpdatedAt = now
		if err := tx.Create(&image).Error; err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"image_id": image.ID})
		task = domain.AITask{
			Kind: "image_describe", Status: domain.TaskPending, PayloadJSON: string(payload),
			ExecutionGeneration: 1, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		// 先创建任务拿到 ID，再回填图片外键；同事务内完成，不会出现悬空任务。
		image.DescriptionTaskID = task.ID
		if err := tx.Model(&domain.Image{}).Where("id=?", image.ID).
			Update("description_task_id", task.ID).Error; err != nil {
			return err
		}
		event := domain.OutboxEvent{
			AggregateID: task.ID, EventType: "image.describe",
			PayloadJSON: string(payload), CreatedAt: now,
		}
		return tx.Create(&event).Error
	})
	return image, task, err
}

// ImageForTask 从任务 payload 解析 image_id 并读取图片行，供 Worker 执行描述前加载数据。
func (s *Store) ImageForTask(ctx context.Context, task domain.AITask) (domain.Image, error) {
	imageID, err := imageIDFromTask(task)
	if err != nil {
		return domain.Image{}, err
	}
	var image domain.Image
	if err := s.DB.WithContext(ctx).First(&image, imageID).Error; err != nil {
		return domain.Image{}, err
	}
	return image, nil
}

// BeginImageDescription 确权图片：仅当仍为 uploaded 且任务匹配时转入 processing，返回确权后的图片行。
func (s *Store) BeginImageDescription(ctx context.Context, task domain.AITask) (domain.Image, error) {
	imageID, err := imageIDFromTask(task)
	if err != nil {
		return domain.Image{}, err
	}
	now := time.Now().UTC()
	// 条件更新即确权：并发重复领取时 RowsAffected 为 0，未确权则不调用视觉模型。
	result := s.DB.WithContext(ctx).Model(&domain.Image{}).
		Where("id=? AND description_task_id=? AND status='uploaded'", imageID, task.ID).
		Updates(map[string]any{"status": "processing", "last_error": "", "updated_at": now})
	if result.Error != nil {
		return domain.Image{}, result.Error
	}
	if result.RowsAffected != 1 {
		return domain.Image{}, errors.New("image state changed before description")
	}
	var image domain.Image
	if err := s.DB.WithContext(ctx).First(&image, imageID).Error; err != nil {
		return domain.Image{}, err
	}
	return image, nil
}

// CompleteImageDescription 提交图片描述成功：任务 succeeded 与图片 ready 在同一事务中写入。
func (s *Store) CompleteImageDescription(ctx context.Context, task domain.AITask, token, descriptionJSON, modelName string) error {
	imageID, err := imageIDFromTask(task)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		taskResult := tx.Exec(`UPDATE ai_tasks SET status='succeeded',lease_token='',lease_until=NULL,updated_at=?
			WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=? AND lease_until>?`,
			now, task.ID, task.ExecutionGeneration, token, now)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return errors.New("task lease lost")
		}
		// 仅 processing 的图片可置 ready；并发删除或重试会使该更新落空并回滚。
		imageResult := tx.Model(&domain.Image{}).
			Where("id=? AND description_task_id=? AND status='processing'", imageID, task.ID).
			Updates(map[string]any{
				"status": "ready", "description_json": descriptionJSON,
				"description_model": modelName, "last_error": "", "updated_at": now,
			})
		if imageResult.Error != nil {
			return imageResult.Error
		}
		if imageResult.RowsAffected != 1 {
			return errors.New("image state changed before description completion")
		}
		return tx.Exec(`UPDATE task_attempts SET status='succeeded',finished_at=?
			WHERE task_id=? AND execution_generation=? AND lease_token=?`,
			now, task.ID, task.ExecutionGeneration, token).Error
	})
}

// FailImage 是图片任务的失败入口：terminal 为 true 时直接 dead，否则按尝试次数决定重试。
func (s *Store) FailImage(ctx context.Context, task domain.AITask, token string, cause error, terminal bool) (time.Time, bool, error) {
	return s.failImage(ctx, task, token, cause, terminal, false)
}

// FailExpiredImage 供 Reaper 使用：仅接受租约已过期的失败提交。
func (s *Store) FailExpiredImage(ctx context.Context, task domain.AITask, token string, cause error) (time.Time, bool, error) {
	return s.failImage(ctx, task, token, cause, false, true)
}

// failImage 是图片失败路径的公共实现，与 store.fail 对称：额外在同一事务内同步图片状态。
func (s *Store) failImage(ctx context.Context, task domain.AITask, token string, cause error, terminal, expired bool) (time.Time, bool, error) {
	if cause == nil {
		cause = errors.New("unknown error")
	}
	imageID, err := imageIDFromTask(task)
	if err != nil {
		return time.Time{}, false, err
	}
	delay, retry := domain.RetryDelay(task.AttemptNo)
	if terminal {
		retry = false
	}
	status := domain.TaskDead
	available := time.Now().UTC()
	if retry {
		status = domain.TaskRetryWait
		available = available.Add(delay)
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		// 常规失败要求租约仍有效，Reaper 要求租约已过期；两条路径共用一条 SQL 模板。
		leasePredicate := "lease_until>?"
		if expired {
			leasePredicate = "lease_until<=?"
		}
		result := tx.Exec(`UPDATE ai_tasks SET status=?,available_at=?,lease_token='',lease_until=NULL,
			last_error=?,updated_at=? WHERE id=? AND status='processing' AND execution_generation=? AND lease_token=? AND `+leasePredicate,
			status, available, cause.Error(), now, task.ID, task.ExecutionGeneration, token, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task lease lost")
		}
		imageStatus := "failed"
		errorMessage := cause.Error()
		if retry {
			imageStatus = "uploaded"
			errorMessage = ""
		}
		// 已 ready 或正在删除的图片不回退，防止把成功状态打成失败。
		imageResult := tx.Model(&domain.Image{}).
			Where("id=? AND description_task_id=? AND status NOT IN ('ready','deleting')", imageID, task.ID).
			Updates(map[string]any{
				"status": imageStatus, "last_error": errorMessage, "updated_at": now,
			})
		if imageResult.Error != nil {
			return imageResult.Error
		}
		// 重试同样写 Outbox 而非直接写 Redis，复用同一条可靠投递路径。
		if retry {
			payload, _ := json.Marshal(map[string]any{"image_id": imageID})
			event := domain.OutboxEvent{
				AggregateID: task.ID, EventType: "image.describe.retry",
				PayloadJSON: string(payload), CreatedAt: now,
			}
			if err := tx.Create(&event).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`UPDATE task_attempts SET status=?,error_message=?,finished_at=?
			WHERE task_id=? AND execution_generation=? AND lease_token=?`,
			status, cause.Error(), now, task.ID, task.ExecutionGeneration, token).Error
	})
	return available, retry, err
}

// RetryImage 手动重试：仅 failed 图片可重试；任务从 dead 转 pending 并递增 generation，图片回 uploaded。
func (s *Store) RetryImage(ctx context.Context, imageID uint64) (domain.Image, error) {
	var image domain.Image
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 行级锁串行化并发重试，避免两个请求同时重建任务。
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&image, imageID).Error; err != nil {
			return err
		}
		if image.Status != "failed" {
			return errors.New("only failed images may be retried")
		}
		now := time.Now().UTC()
		// 仅 dead 任务可重试；generation+1 使所有旧租约的 fencing token 即刻失效。
		result := tx.Exec(`UPDATE ai_tasks SET status='pending',attempt_no=0,
			execution_generation=execution_generation+1,available_at=?,last_error='',updated_at=?
			WHERE id=? AND status='dead'`, now, now, image.DescriptionTaskID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("image description task is not retryable")
		}
		if err := tx.Model(&image).Updates(map[string]any{
			"status": "uploaded", "last_error": "", "updated_at": now,
		}).Error; err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"image_id": image.ID})
		return tx.Create(&domain.OutboxEvent{
			AggregateID: image.DescriptionTaskID, EventType: "image.describe.manual_retry",
			PayloadJSON: string(payload), CreatedAt: now,
		}).Error
	})
	return image, err
}

// CreateDocumentFromImage 把 ready 图片的衍生描述作为文档加入知识库；幂等，重复调用返回既有结果。
func (s *Store) CreateDocumentFromImage(ctx context.Context, imageID uint64, filename, content, contentHash string) (domain.Image, domain.Document, domain.AITask, error) {
	var image domain.Image
	var document domain.Document
	var task domain.AITask
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 行锁防止并发索引同一图片产生重复文档。
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&image, imageID).Error; err != nil {
			return err
		}
		if image.Status != "ready" {
			return errors.New("only ready images may be added to the knowledge base")
		}
		// 幂等分支：已存在衍生文档则原样返回，不重复创建任务与事件。
		if image.DerivedDocumentID != nil {
			if err := tx.First(&document, *image.DerivedDocumentID).Error; err != nil {
				return err
			}
			if err := tx.First(&task, document.IndexingTaskID).Error; err != nil {
				return err
			}
			return nil
		}
		now := time.Now().UTC()
		document = domain.Document{
			Filename: filename, MediaType: "md", ContentHash: contentHash, Content: content,
			Status: "uploaded", CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&document).Error; err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"document_id": document.ID})
		task = domain.AITask{
			Kind: "document_index", Status: domain.TaskPending, PayloadJSON: string(payload),
			ExecutionGeneration: 1, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		document.IndexingTaskID = task.ID
		if err := tx.Model(&document).Update("indexing_task_id", task.ID).Error; err != nil {
			return err
		}
		if err := tx.Model(&image).Update("derived_document_id", document.ID).Error; err != nil {
			return err
		}
		image.DerivedDocumentID = &document.ID
		return tx.Create(&domain.OutboxEvent{
			AggregateID: task.ID, EventType: "document.index",
			PayloadJSON: string(payload), CreatedAt: now,
		}).Error
	})
	return image, document, task, err
}

// PrepareImageDelete 进入删除流程：图片置 deleting、终止相关任务，并连带处理衍生文档。
func (s *Store) PrepareImageDelete(ctx context.Context, imageID uint64) (domain.Image, error) {
	var image domain.Image
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&image, imageID).Error; err != nil {
			return err
		}
		// 已在删除中则直接返回，重复调用幂等。
		if image.Status == "deleting" {
			return nil
		}
		now := time.Now().UTC()
		if err := terminateTaskForDelete(tx, image.DescriptionTaskID, "image deleted by user", now); err != nil {
			return err
		}
		// 有衍生文档时同步置 deleting，避免图片删除后文档仍可被检索。
		if image.DerivedDocumentID != nil {
			var document domain.Document
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&document, *image.DerivedDocumentID).Error; err != nil {
				return err
			}
			if document.Status != "deleting" {
				// 条件更新校验 status 未被并发修改，RowsAffected 为 0 即视为冲突。
				result := tx.Model(&domain.Document{}).
					Where("id=? AND status=?", document.ID, document.Status).
					Updates(map[string]any{
						"status": "deleting", "error_message": "", "updated_at": now,
					})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return errors.New("derived document state changed during image deletion")
				}
			}
			if err := terminateTaskForDelete(
				tx, document.IndexingTaskID, "source image deleted by user", now,
			); err != nil {
				return err
			}
		}
		return tx.Model(&image).Updates(map[string]any{
			"status": "deleting", "last_error": "", "updated_at": now,
		}).Error
	})
	return image, err
}

// FinalizeImageDelete 物理删除：清空衍生文档切片、删除文档与图片行；仅接受 deleting 状态。
func (s *Store) FinalizeImageDelete(ctx context.Context, imageID uint64) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 只在 deleting 状态执行物理删除，防止误删仍在使用的图片。
		var image domain.Image
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND status='deleting'", imageID).First(&image).Error; err != nil {
			return err
		}
		if image.DerivedDocumentID != nil {
			if err := tx.Exec(
				"DELETE FROM document_chunks WHERE document_id=?", *image.DerivedDocumentID,
			).Error; err != nil {
				return err
			}
			result := tx.Where("id=? AND status='deleting'", *image.DerivedDocumentID).
				Delete(&domain.Document{})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("derived document is not ready to be deleted")
			}
		}
		result := tx.Where("id=? AND status='deleting'", imageID).Delete(&domain.Image{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("image is not ready to be deleted")
		}
		return nil
	})
}

// terminateTaskForDelete 把相关任务终止为 dead；进行中的 attempt 一并闭合，并携带删除原因。
func terminateTaskForDelete(tx *gorm.DB, taskID uint64, reason string, now time.Time) error {
	// taskID 为 0 说明任务尚未创建，无需处理。
	if taskID == 0 {
		return nil
	}
	var task domain.AITask
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, taskID).Error; err != nil {
		return err
	}
	// 已终结的任务保持原状，避免改写成功历史。
	if task.Status == domain.TaskSucceeded || task.Status == domain.TaskDead {
		return nil
	}
	// 有 Worker 正在执行时先闭合 attempt；随后任务行更新会因 token 失效挡住旧 Worker 的迟到提交。
	if task.Status == domain.TaskProcessing {
		result := tx.Model(&domain.TaskAttempt{}).
			Where("task_id=? AND execution_generation=? AND lease_token=? AND status='processing'",
				task.ID, task.ExecutionGeneration, task.LeaseToken).
			Updates(map[string]any{
				"status": "dead", "error_message": reason, "finished_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("processing task attempt is missing during deletion")
		}
	}
	result := tx.Model(&domain.AITask{}).
		Where("id=? AND status=? AND execution_generation=? AND lease_token=?",
			task.ID, task.Status, task.ExecutionGeneration, task.LeaseToken).
		Updates(map[string]any{
			"status": domain.TaskDead, "last_error": reason,
			"lease_token": "", "lease_until": nil, "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("task state changed during deletion")
	}
	return nil
}

// imageIDFromTask 从任务 payload 解析 image_id；字段缺失或为 0 视为非法 payload。
func imageIDFromTask(task domain.AITask) (uint64, error) {
	var payload struct {
		ImageID uint64 `json:"image_id"`
	}
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil || payload.ImageID == 0 {
		return 0, errors.New("invalid image task payload")
	}
	return payload.ImageID, nil
}
