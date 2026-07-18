package recovery

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
	"gorm.io/gorm"
)

// Reap scans the MySQL source of truth, so a Redis restart or a lost processing
// entry cannot strand a task forever. Fail fences the old worker before ACK.
func Reap(ctx context.Context, s *store.Store, q *queue.Redis) error {
	var tasks []domain.AITask
	if err := s.DB.WithContext(ctx).
		Where("status=? AND lease_until IS NOT NULL AND lease_until<=?", domain.TaskProcessing, time.Now().UTC()).
		Order("lease_until,id").Limit(100).Find(&tasks).Error; err != nil {
		return err
	}
	for _, task := range tasks {
		id := task.ID
		var err error
		if task.Kind == "document_index" {
			_, _, err = s.FailDocument(ctx, task, task.LeaseToken, context.DeadlineExceeded, false)
		} else {
			_, _, err = s.Fail(ctx, task, task.LeaseToken, context.DeadlineExceeded)
		}
		if err != nil {
			slog.Warn("reaper fail", "task_id", id, "error", err)
			continue
		}
		if err := q.Ack(ctx, id, task.LeaseToken); err != nil {
			slog.Warn("reaper ack", "task_id", id, "error", err)
		}
	}
	return nil
}

// Reconcile rebuilds ready work and removes stale ready/processing Redis ghosts.
func Reconcile(ctx context.Context, s *store.Store, q *queue.Redis) error {
	var queued []domain.AITask
	if err := s.DB.WithContext(ctx).Where("status='queued'").Find(&queued).Error; err != nil {
		return err
	}
	for _, task := range queued {
		if err := q.Enqueue(ctx, task.ID, task.AvailableAt); err != nil {
			return err
		}
	}
	if err := cleanupReady(ctx, s, q); err != nil {
		return err
	}
	return cleanupProcessing(ctx, s, q)
}

func cleanupReady(ctx context.Context, s *store.Store, q *queue.Redis) error {
	keys, err := q.Client().ZRange(ctx, queue.ReadyKey, 0, -1).Result()
	if err != nil {
		return err
	}
	for _, key := range keys {
		id, parseErr := strconv.ParseUint(key, 10, 64)
		if parseErr != nil {
			continue
		}
		var status string
		result := s.DB.WithContext(ctx).Raw("SELECT status FROM ai_tasks WHERE id=?", id).Scan(&status)
		if result.RowsAffected == 0 || status == string(domain.TaskSucceeded) || status == string(domain.TaskDead) {
			if err := q.Client().ZRem(ctx, queue.ReadyKey, key).Err(); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanupProcessing(ctx context.Context, s *store.Store, q *queue.Redis) error {
	keys, err := q.Processing(ctx)
	if err != nil {
		return err
	}
	for _, key := range keys {
		id, parseErr := strconv.ParseUint(key, 10, 64)
		if parseErr != nil {
			continue
		}
		var task domain.AITask
		result := s.DB.WithContext(ctx).Select("id", "status").First(&task, id)
		if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
			return result.Error
		}
		if result.Error == gorm.ErrRecordNotFound || task.Status != domain.TaskProcessing {
			if err := q.Cleanup(ctx, id); err != nil {
				return err
			}
		}
	}
	return nil
}
