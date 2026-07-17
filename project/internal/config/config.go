package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr         string
	MySQLDSN         string
	RedisAddr        string
	RedisPassword    string
	QdrantURL        string
	AIMode           string
	AIBaseURL        string
	AIAPIKey         string
	AIChatModel      string
	AIEmbeddingModel string
	EmbeddingDim     int
	TaskTimeout      time.Duration
	LeaseDuration    time.Duration
	ShutdownGrace    time.Duration
	ReportDir        string
}

func Load() Config {
	return Config{
		HTTPAddr:         env("HTTP_ADDR", "127.0.0.1:8080"),
		MySQLDSN:         env("MYSQL_DSN", "learnq:learnq@tcp(127.0.0.1:3306)/learnq?parseTime=true&charset=utf8mb4&multiStatements=true"),
		RedisAddr:        env("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:    os.Getenv("REDIS_PASSWORD"),
		QdrantURL:        env("QDRANT_URL", "http://127.0.0.1:6333"),
		AIMode:           env("AI_MODE", "fake"),
		AIBaseURL:        env("AI_BASE_URL", "https://api.openai.com/v1"),
		AIAPIKey:         os.Getenv("AI_API_KEY"),
		AIChatModel:      env("AI_CHAT_MODEL", "gpt-4.1-mini"),
		AIEmbeddingModel: env("AI_EMBEDDING_MODEL", "text-embedding-3-small"),
		EmbeddingDim:     envInt("EMBEDDING_DIM", 64),
		TaskTimeout:      45 * time.Second,
		LeaseDuration:    60 * time.Second,
		ShutdownGrace:    70 * time.Second,
		ReportDir:        env("REPORT_DIR", "data/reports"),
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
