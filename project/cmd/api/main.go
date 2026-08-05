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
	"github.com/WoAiXueXiHa/LeranQ/project/internal/imagestore"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/rag"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/skill"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
)

// main 是 API 进程入口：加载并校验配置，装配 DB/Redis/Qdrant/模型/队列等依赖，
// 构造 Gin Handler 并启动 HTTP 服务；收到 SIGINT/SIGTERM 后先停止接收新请求，
// 再在 15 秒宽限期内等待在途请求完成。启动期任一依赖失败即 os.Exit(1)。
func main() {
	// API 进程只负责同步请求、查询与依赖探活，不在请求协程中执行耗时 AI 任务。
	// 学习报告和文档索引统一写入任务表，由独立 Worker 消费，避免模型延迟拖垮 HTTP 服务。
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	// 连接数据库
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		slog.Error("mysql", "error", err)
		os.Exit(1)
	}
	// 连接 Chat、Embedding模型
	chat, embedding := bootstrap.Models(cfg)
	// Chat、Embedding 和向量库都通过窄接口注入，fake/real 模式共用同一条业务链路。
	// 连接向量数据库
	// Qdrant 客户端超时固定 10 秒，与任务执行超时相互独立：向量查询是请求同步链路的一部分，需要各自明确的上限。
	vectors := rag.Qdrant{BaseURL: cfg.QdrantURL, Collection: cfg.RAGCollection, Client: &http.Client{Timeout: 10 * time.Second}}
	// 连接 Redis 队列
	redisClient := bootstrap.Redis(cfg)
	defer redisClient.Close()
	taskQueue := queue.New(redisClient)
	registry := skill.New(chat)
	tools := agenttool.Service{DB: db, Embedding: embedding, Vectors: vectors}
	registry.RegisterTool("weekly_stats", tools.WeeklyStats)
	registry.RegisterTool("rag_query", tools.RAGQuery)
	runtimeChatModel, runtimeVisionModel, runtimeEmbeddingModel :=
		cfg.AIChatModel, cfg.AIVisionModel, cfg.AIEmbeddingModel
	// 选择模型
	// fake 模式下替换为占位模型名：这些名字与 Fake 实现响应中报告的 Model 字段一致，保证就绪探针展示的运行时信息与实际执行路径相符。
	if cfg.AIMode == "fake" {
		runtimeChatModel = "learnq-fake-chat-v1"
		runtimeVisionModel = "learnq-fake-vision-v1"
		runtimeEmbeddingModel = "learnq-fake-embedding-v1"
	}
	// 组装 API Handler，注入数据库、模型、向量库、队列和工具函数
	handler := api.New(store.New(db), registry, api.WithRAG(chat, embedding, vectors),
		api.WithImageStore(imagestore.New(cfg.ImageDir)),
		api.WithRuntimeInfo(cfg.AIMode, runtimeChatModel, runtimeVisionModel, runtimeEmbeddingModel),
		// 依赖探活：Redis ping 与 Qdrant Ready 都通过才认为就绪。
		api.WithHealthChecks(func(ctx context.Context) error {
			return redisClient.Ping(ctx).Err()
		}, vectors.Ready),
		// 任务链路探活：心跳键存在即视为至少有一个 Worker 在轮询，避免任务无人消费时仍报就绪。
		api.WithWorkerCheck(func(ctx context.Context) (bool, error) {
			return taskQueue.WorkerAlive(ctx)
		})).Handler()
	// 启动 HTTP 服务
	server := &http.Server{
		// ReadHeaderTimeout 防慢请求；WriteTimeout 要覆盖一次同步 Skill/RAG 调用的最长耗时。
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	// 监听系统信号，关闭 HTTP 服务
	go func() {
		slog.Info("api listening", "address", cfg.HTTPAddr)
		// 过滤 ErrServerClosed：正常停机路径下 Shutdown 必然使 ListenAndServe 返回该错误，不能据此判定监听失败。
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("api listen", "error", err)
			os.Exit(1)
		}
	}()
	// 等待 SIGINT/SIGTERM 信号，关闭 HTTP 服务
	// NotifyContext 把信号统一转成 ctx 取消，主流程无需单独维护信号通道；SIGKILL 无法捕获，不在处理范围。
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-stop.Done()
	// 先停止接收新请求，再给在途请求最多 15 秒完成，防止响应写到一半被进程终止。
	// 15 秒是固定兜底：超时后 Shutdown 返回错误，随即 os.Exit(1) 终止进程，未完成的长请求被强制断开。
	ctx, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("shutdown", "error", err)
		os.Exit(1)
	}
}
