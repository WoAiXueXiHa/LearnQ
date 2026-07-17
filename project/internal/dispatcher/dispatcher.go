package dispatcher

import (
	"context"
	"log/slog"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
)

func Run(ctx context.Context, s *store.Store, q *queue.Redis, logger *slog.Logger) {
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
