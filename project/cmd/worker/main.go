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
	wait := make(chan struct{})
	go func() { pool.Wait(); close(wait) }()
	select {
	case <-wait:
	case <-time.After(cfg.ShutdownGrace):
		logger.Warn("worker shutdown grace exceeded")
	}
}
