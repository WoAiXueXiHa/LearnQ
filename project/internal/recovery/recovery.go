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

// Reap 从 MySQL 真相源扫描过期租约，因此 Redis 重启或 processing 项丢失都不会永久卡住任务。
// 先用 Store.Fail 的租约条件写入封锁旧 Worker，再 ACK Redis，顺序不能颠倒。
func Reap(ctx context.Context, s *store.Store, q *queue.Redis) error {
	var tasks []domain.AITask
	// 只取租约已到期的 processing 任务，按到期时间升序每轮最多 100 条，
	// 防止大量过期任务在单次扫描中占满事务。
	if err := s.DB.WithContext(ctx).
		Where("status=? AND lease_until IS NOT NULL AND lease_until<=?", domain.TaskProcessing, time.Now().UTC()).
		Order("lease_until,id").Limit(100).Find(&tasks).Error; err != nil {
		return err
	}
	for _, task := range tasks {
		id := task.ID
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
			// 单个任务回收失败不阻断整轮；其租约已过期，下轮扫描会再次尝试。
			slog.Warn("reaper fail", "task_id", id, "error", err)
			continue
		}
		if err := q.Ack(ctx, id, task.LeaseToken); err != nil {
			slog.Warn("reaper ack", "task_id", id, "error", err)
		}
	}
	return nil
}

// Reconcile 以 MySQL 状态重建 ready 队列，并删除 Redis 中已经终态或不存在的幽灵项。
// 整个过程幂等，可周期执行，也可用于服务重启后的自愈。
func Reconcile(ctx context.Context, s *store.Store, q *queue.Redis) error {
	var queued []domain.AITask
	if err := s.DB.WithContext(ctx).Where("status='queued'").Find(&queued).Error; err != nil {
		return err
	}
	for _, task := range queued {
		// ZADD 对已存在成员是幂等更新分数（AvailableAt），重复对账不会产生重复任务。
		if err := q.Enqueue(ctx, task.ID, task.AvailableAt); err != nil {
			return err
		}
	}
	if err := cleanupReady(ctx, s, q); err != nil {
		return err
	}
	return cleanupProcessing(ctx, s, q)
}

// cleanupReady 扫描 Redis ready ZSet，剔除已终态（succeeded/dead）或数据库中
// 已不存在的任务。这类残留条目即便被 Worker 领取，也会因 fencing 校验失败而空转。
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
		// 记录缺失或已终态（succeeded/dead）的条目没有恢复价值，直接从队列删除；
		// 其余状态（pending/queued/processing）的条目无法判定为残留，保留不动。
		if result.RowsAffected == 0 || status == string(domain.TaskSucceeded) || status == string(domain.TaskDead) {
			if err := q.Client().ZRem(ctx, queue.ReadyKey, key).Err(); err != nil {
				return err
			}
		}
	}
	return nil
}

// cleanupProcessing 扫描 Redis processing ZSet，清理记录已删除或已脱离 processing
// 状态的任务。Redis 崩溃重启后 processing 集合是孤儿数据，只能靠这里回收。
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
