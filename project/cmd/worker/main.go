package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/agenttool"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/bootstrap"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/dispatcher"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/indexer"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/rag"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/recovery"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/skill"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/worker"
)

func main() {
	// Worker 同时承载 Outbox 投递、故障恢复和任务执行；MySQL 始终是状态真相源，
	// Redis 仅保存可从数据库重建的就绪/处理中索引。
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		slog.Error("mysql", "error", err)
		os.Exit(1)
	}
	redisClient := bootstrap.Redis(cfg)
	defer redisClient.Close()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := redisClient.Ping(pingCtx).Err(); err != nil {
		pingCancel()
		slog.Error("redis", "error", err)
		os.Exit(1)
	}
	pingCancel()
	s := store.New(db)
	q := queue.New(redisClient)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		// 心跳只表示“至少有一个 Worker 正常轮询”，供 API readiness 使用，
		// TTL 大于刷新周期，短暂调度抖动不会立即把服务判为不可用。
		const heartbeatTTL = 10 * time.Second
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			if err := q.Heartbeat(ctx, heartbeatTTL); err != nil {
				logger.Error("worker heartbeat failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go dispatcher.Run(ctx, s, q, logger)
	go func() {
		// Reap 处理租约超时，Reconcile 修补 MySQL 与 Redis 的双写间隙；
		// 两者都可重复执行，因此进程重启后无需依赖内存状态恢复。
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := recovery.Reap(ctx, s, q); err != nil {
					logger.Error("reaper", "error", err)
				}
				if err := recovery.Reconcile(ctx, s, q); err != nil {
					logger.Error("reconciler", "error", err)
				}
			}
		}
	}()
	chat, embedding := bootstrap.Models(cfg)
	vectors := rag.Qdrant{BaseURL: cfg.QdrantURL, Collection: "learnq_chunks", Client: &http.Client{Timeout: 10 * time.Second}}
	collectionCtx, collectionCancel := context.WithTimeout(context.Background(), 10*time.Second)
	// 启动时校验向量维度，尽早暴露“模型维度与既有集合不一致”，避免索引到一半才失败。
	if err := vectors.EnsureCollection(collectionCtx, cfg.EmbeddingDim); err != nil {
		collectionCancel()
		logger.Error("qdrant collection", "error", err)
		os.Exit(1)
	}
	collectionCancel()
	registry := skill.New(chat)
	tools := agenttool.Service{DB: db, Embedding: embedding, Vectors: vectors}
	registry.RegisterTool("weekly_stats", tools.WeeklyStats)
	registry.RegisterTool("rag_query", tools.RAGQuery)
	pool := worker.New(s, q, chat, cfg.ReportDir, logger)
	pool.WithSkills(registry)
	pool.WithIndexer(&indexer.Indexer{
		Store: s, Embedding: embedding, Dimension: cfg.EmbeddingDim,
		Vectors: vectors,
	})
	pool.Run(ctx)
	<-ctx.Done()
	// 取消信号先阻止领取新任务；已领取任务在宽限期内完成持久化，超时任务之后由 Reaper 接管。
	wait := make(chan struct{})
	go func() { pool.Wait(); close(wait) }()
	select {
	case <-wait:
	case <-time.After(cfg.ShutdownGrace):
		logger.Warn("worker shutdown grace exceeded")
	}
}
