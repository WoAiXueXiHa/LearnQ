package main

import (
	"context"
	"log"
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
	"github.com/WoAiXueXiHa/LeranQ/project/internal/rag"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/skill"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/store"
)

func main() {
	cfg := config.Load()
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		log.Fatal(err)
	}
	chat, embedding := bootstrap.Models(cfg)
	vectors := rag.Qdrant{BaseURL: cfg.QdrantURL, Collection: "learnq_chunks"}
	registry := skill.New(chat)
	tools := agenttool.Service{DB: db, Embedding: embedding, Vectors: vectors}
	registry.RegisterTool("weekly_stats", tools.WeeklyStats)
	registry.RegisterTool("rag_query", tools.RAGQuery)
	handler := api.New(store.New(db), registry, api.WithRAG(embedding, vectors)).Handler()
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("api listening", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
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
