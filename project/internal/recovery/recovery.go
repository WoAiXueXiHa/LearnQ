package recovery

import (
	"context"
	"strconv"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
)

// Reap fences expired leases in MySQL before putting retryable work back through
// the outbox. Old workers can no longer complete because their token was cleared.
func Reap(ctx context.Context, s *store.Store, q *queue.Redis) error {
	ids, err := q.Expired(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var task domain.AITask
		if s.DB.WithContext(ctx).First(&task, id).Error != nil {
			continue
		}
		if task.Status != domain.TaskProcessing || task.LeaseUntil == nil || task.LeaseUntil.After(time.Now().UTC()) {
			continue
		}
		_, _, _ = s.Fail(ctx, task, task.LeaseToken, context.DeadlineExceeded)
		_ = q.Ack(ctx, id, task.LeaseToken)
	}
	return nil
}

// Reconcile repairs derived Redis state from MySQL and removes terminal ghosts.
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
			q.Client().ZRem(ctx, queue.ReadyKey, key)
		}
	}
	return nil
}
