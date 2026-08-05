package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/internal/agenttool"
	"github.com/WoAiXueXiHa/LeranQ/internal/bootstrap"
	"github.com/WoAiXueXiHa/LeranQ/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/internal/dispatcher"
	"github.com/WoAiXueXiHa/LeranQ/internal/imagestore"
	"github.com/WoAiXueXiHa/LeranQ/internal/indexer"
	"github.com/WoAiXueXiHa/LeranQ/internal/queue"
	"github.com/WoAiXueXiHa/LeranQ/internal/rag"
	"github.com/WoAiXueXiHa/LeranQ/internal/recovery"
	"github.com/WoAiXueXiHa/LeranQ/internal/skill"
	"github.com/WoAiXueXiHa/LeranQ/internal/store"
	"github.com/WoAiXueXiHa/LeranQ/internal/worker"
)

// main 是 Worker 进程入口：加载校验配置，预检 MySQL/Redis/Qdrant，启动心跳、
// Dispatcher、Reaper/Reconciler 与 Worker Pool 四个后台循环；收到 SIGINT/SIGTERM 后
// 停止领取新任务，在 SHUTDOWN_GRACE 宽限期内等已领取任务落库，逾期直接退出，遗留任务由下次启动的 Reaper 接管。
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
	// 启动前预检 Redis 连通性（5 秒超时）：队列不可用时不带病启动，故障在启动期暴露而非首个任务领取时。
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := redisClient.Ping(pingCtx).Err(); err != nil {
		pingCancel()
		slog.Error("redis", "error", err)
		os.Exit(1)
	}
	pingCancel()
	s := store.New(db)
	q := queue.New(redisClient)
	// JSON 结构化日志输出到 stdout，容器场景直接采集，不写本地文件。
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// SIGINT/SIGTERM 经 NotifyContext 统一转为 ctx 取消，下方所有后台循环共用这一个信号源。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		// 心跳只表示“至少有一个 Worker 正常轮询”，供 API readiness 使用，
		// TTL 大于刷新周期，短暂调度抖动不会立即把服务判为不可用。
		const heartbeatTTL = 10 * time.Second
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			// 心跳失败只记录不退出：Redis 短暂不可用不应杀死 Worker，任务状态仍完整保存在 MySQL。
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
	// Dispatcher 常驻轮询 MySQL outbox，把待发布事件搬运进 Redis 就绪队列。
	go dispatcher.Run(ctx, s, q, logger)
	go func() {
		// Reap 处理租约超时，Reconcile 修补 MySQL 与 Redis 的双写间隙；
		// 两者都可重复执行，因此进程重启后无需依赖内存状态恢复。
		// 固定 5 秒周期：租约过期的任务延迟回收有确定上限，扫描 MySQL 的压力不随任务量增长。
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// 同一周期内先 Reap 后 Reconcile，串行执行，避免两个恢复循环并发操作同一批任务。
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
	// 该客户端同时用于集合校验与文档索引写入，超时固定 10 秒，与任务执行超时相互独立。
	vectors := rag.Qdrant{BaseURL: cfg.QdrantURL, Collection: cfg.RAGCollection, Client: &http.Client{Timeout: 10 * time.Second}}
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
	// 通过 With* 选项装配 Pool：WithTiming 注入任务超时与租约，WithVision/WithSkills/WithIndexer
	// 分别支撑 image_describe、study_report、document_index 三类任务的执行。
	pool := worker.New(s, q, chat, cfg.ReportDir, logger)
	pool.WithTiming(cfg.TaskTimeout, cfg.LeaseDuration)
	pool.WithVision(bootstrap.Vision(cfg), imagestore.New(cfg.ImageDir))
	pool.WithSkills(registry)
	pool.WithIndexer(&indexer.Indexer{
		Store: s, Embedding: embedding, Dimension: cfg.EmbeddingDim,
		Vectors: vectors,
	})
	pool.Run(ctx)
	// Run 启动固定数量 goroutine 的领取循环后返回，主 goroutine 在此等待取消信号。
	<-ctx.Done()
	// 取消信号先阻止领取新任务；已领取任务在宽限期内完成持久化，超时任务之后由 Reaper 接管。
	wait := make(chan struct{})
	go func() { pool.Wait(); close(wait) }()
	// 宽限期到点仍有人未完成则直接退出：遗留任务的租约会过期，下次启动的 Reaper 会回收重试，
	// MySQL 保存全部状态，强杀进程不会丢失任务。
	select {
	case <-wait:
	case <-time.After(cfg.ShutdownGrace):
		logger.Warn("worker shutdown grace exceeded")
	}
}
