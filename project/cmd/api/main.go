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
	// API 进程只负责同步请求、查询与依赖探活，不在请求协程中执行耗时 AI 任务。
	// 学习报告和文档索引统一写入任务表，由独立 Worker 消费，避免模型延迟拖垮 HTTP 服务。
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
	// Chat、Embedding 和向量库都通过窄接口注入，fake/real 模式共用同一条业务链路。
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
		// ReadHeaderTimeout 防慢请求；WriteTimeout 要覆盖一次同步 Skill/RAG 调用的最长耗时。
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
	// 先停止接收新请求，再给在途请求最多 15 秒完成，防止响应写到一半被进程终止。
	ctx, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("shutdown", "error", err)
		os.Exit(1)
	}
}
