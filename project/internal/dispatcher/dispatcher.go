package dispatcher

import (
	"context"
	"log/slog"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
)

func Run(ctx context.Context, s *store.Store, q *queue.Redis, logger *slog.Logger) {
	// Dispatcher 只搬运 Outbox，不执行任务。短轮询换取低投递延迟，每批设上限避免
	// 大量历史事件长期占有事务和行锁。
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
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
