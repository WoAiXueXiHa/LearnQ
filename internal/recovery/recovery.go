package recovery

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/queue"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
)

const reconcileBatch = 200

type Stats struct {
	ExpiredReaped     int
	QueuedEnqueued    int
	ReadyCleaned      int
	ProcessingCleaned int
}

func Reap(ctx context.Context, s *store.Store, q *queue.Redis) error {
	_, err := ReapWithStats(ctx, s, q)
	return err
}

func ReapWithStats(ctx context.Context, s *store.Store, q *queue.Redis) (Stats, error) {
	stats := Stats{}
	var tasks []domain.AITask
	if err := s.DB.WithContext(ctx).
		Where("status=? AND lease_until IS NOT NULL AND lease_until<=?", domain.TaskProcessing, time.Now().UTC()).
		Order("lease_until,id").Limit(100).Find(&tasks).Error; err != nil {
		return stats, err
	}
	for _, task := range tasks {
		var err error
		switch task.Kind {
		case "document_index":
			_, _, err = s.FailExpiredDocument(ctx, task, task.LeaseToken, context.DeadlineExceeded)
		case "image_describe":
			_, _, err = s.FailExpiredImage(ctx, task, task.LeaseToken, context.DeadlineExceeded)
		default:
			_, _, err = s.FailExpired(ctx, task, task.LeaseToken, context.DeadlineExceeded)
		}
		if err != nil {
			slog.Warn("reaper fail", "task_id", task.ID, "error", err)
			continue
		}
		stats.ExpiredReaped++
		if err := q.Ack(ctx, task.ID, task.LeaseToken); err != nil {
			slog.Warn("reaper ack", "task_id", task.ID, "error", err)
		}
	}
	return stats, nil
}

func Reconcile(ctx context.Context, s *store.Store, q *queue.Redis) error {
	_, err := ReconcileWithStats(ctx, s, q)
	return err
}

func ReconcileWithStats(ctx context.Context, s *store.Store, q *queue.Redis) (Stats, error) {
	stats := Stats{}
	count, err := reconcileQueued(ctx, s, q)
	if err != nil {
		return stats, err
	}
	stats.QueuedEnqueued = count
	count, err = cleanupReady(ctx, s, q)
	if err != nil {
		return stats, err
	}
	stats.ReadyCleaned = count
	count, err = cleanupProcessing(ctx, s, q)
	if err != nil {
		return stats, err
	}
	stats.ProcessingCleaned = count
	return stats, nil
}

func reconcileQueued(ctx context.Context, s *store.Store, q *queue.Redis) (int, error) {
	cursor, err := q.RecoveryCursor(ctx, "mysql_queued_id")
	if err != nil {
		return 0, err
	}
	var tasks []domain.AITask
	if err := s.DB.WithContext(ctx).Where("status='queued' AND id>?", cursor).
		Order("id").Limit(reconcileBatch).Find(&tasks).Error; err != nil {
		return 0, err
	}
	if len(tasks) == 0 {
		return 0, q.SetRecoveryCursor(ctx, "mysql_queued_id", 0)
	}
	for _, task := range tasks {
		if err := q.Enqueue(ctx, task.ID, task.AvailableAt); err != nil {
			return 0, err
		}
	}
	return len(tasks), q.SetRecoveryCursor(ctx, "mysql_queued_id", tasks[len(tasks)-1].ID)
}

func cleanupReady(ctx context.Context, s *store.Store, q *queue.Redis) (int, error) {
	cursor, err := q.RecoveryCursor(ctx, "redis_ready")
	if err != nil {
		return 0, err
	}
	keys, next, err := q.ScanZSet(ctx, queue.ReadyKey, cursor, reconcileBatch)
	if err != nil {
		return 0, err
	}
	ids, invalid := parseMembers(keys)
	statuses, err := taskStatuses(ctx, s, ids)
	if err != nil {
		return 0, err
	}
	remove := append([]string{}, invalid...)
	for id, key := range ids {
		status, exists := statuses[id]
		if !exists || status == domain.TaskSucceeded || status == domain.TaskDead {
			remove = append(remove, key)
		}
	}
	if len(remove) > 0 {
		values := make([]any, len(remove))
		for index := range remove {
			values[index] = remove[index]
		}
		if err := q.Client().ZRem(ctx, queue.ReadyKey, values...).Err(); err != nil {
			return 0, err
		}
	}
	return len(remove), q.SetRecoveryCursor(ctx, "redis_ready", next)
}

func cleanupProcessing(ctx context.Context, s *store.Store, q *queue.Redis) (int, error) {
	cursor, err := q.RecoveryCursor(ctx, "redis_processing")
	if err != nil {
		return 0, err
	}
	keys, next, err := q.ScanZSet(ctx, queue.ProcessingKey, cursor, reconcileBatch)
	if err != nil {
		return 0, err
	}
	ids, invalid := parseMembers(keys)
	statuses, err := taskStatuses(ctx, s, ids)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, key := range invalid {
		if err := q.Client().ZRem(ctx, queue.ProcessingKey, key).Err(); err != nil {
			return cleaned, err
		}
		cleaned++
	}
	for id := range ids {
		if status, exists := statuses[id]; !exists || status != domain.TaskProcessing {
			if err := q.Cleanup(ctx, id); err != nil {
				return cleaned, err
			}
			cleaned++
		}
	}
	return cleaned, q.SetRecoveryCursor(ctx, "redis_processing", next)
}

func parseMembers(keys []string) (map[uint64]string, []string) {
	ids := make(map[uint64]string, len(keys))
	invalid := make([]string, 0)
	for _, key := range keys {
		id, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			invalid = append(invalid, key)
			continue
		}
		ids[id] = key
	}
	return ids, invalid
}

func taskStatuses(ctx context.Context, s *store.Store, ids map[uint64]string) (map[uint64]domain.TaskStatus, error) {
	statuses := make(map[uint64]domain.TaskStatus, len(ids))
	if len(ids) == 0 {
		return statuses, nil
	}
	values := make([]uint64, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	var tasks []domain.AITask
	if err := s.DB.WithContext(ctx).Select("id", "status").Where("id IN ?", values).Find(&tasks).Error; err != nil {
		return nil, err
	}
	for _, task := range tasks {
		statuses[task.ID] = task.Status
	}
	return statuses, nil
}
