package dispatcher

import (
	"context"
	"log/slog"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
)

// Run 启动 Outbox → Redis 的搬运循环，直到 ctx 取消。
// 每轮先执行一次投递再等待 ticker，保证启动瞬间就有一次投递机会。
func Run(ctx context.Context, s *store.Store, q *queue.Redis, logger *slog.Logger) {
	// Dispatcher 只搬运 Outbox，不执行任务。短轮询换取低投递延迟，每批设上限避免
	// 大量历史事件长期占有事务和行锁。
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		// 关闭过程中 ctx 取消会令 DispatchOutbox 报错，仅当 ctx 仍存活时才记录日志，
		// 避免关停时的预期错误刷屏。
		if err := s.DispatchOutbox(ctx, q.Enqueue, 50); err != nil && ctx.Err() == nil {
			logger.Error("dispatch outbox", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
