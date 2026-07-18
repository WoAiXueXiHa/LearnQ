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
	"github.com/WoAiXueXiHa/LeranQ/project/internal/api"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/bootstrap"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/rag"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/skill"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
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
	chat, embedding := bootstrap.Models(cfg)
	vectors := rag.Qdrant{BaseURL: cfg.QdrantURL, Collection: "learnq_chunks", Client: &http.Client{Timeout: 10 * time.Second}}
	redisClient := bootstrap.Redis(cfg)
	defer redisClient.Close()
	taskQueue := queue.New(redisClient)
	registry := skill.New(chat)
	tools := agenttool.Service{DB: db, Embedding: embedding, Vectors: vectors}
	registry.RegisterTool("weekly_stats", tools.WeeklyStats)
	registry.RegisterTool("rag_query", tools.RAGQuery)
	handler := api.New(store.New(db), registry, api.WithRAG(chat, embedding, vectors),
		api.WithHealthChecks(func(ctx context.Context) error {
			return redisClient.Ping(ctx).Err()
		}, vectors.Ready),
		api.WithWorkerCheck(func(ctx context.Context) (bool, error) {
			return taskQueue.WorkerAlive(ctx)
		})).Handler()
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		slog.Info("api listening", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("api listen", "error", err)
			os.Exit(1)
		}
	}()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-stop.Done()
	ctx, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("shutdown", "error", err)
		os.Exit(1)
	}
}
