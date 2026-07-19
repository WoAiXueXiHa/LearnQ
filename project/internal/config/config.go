package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr           string
	MySQLDSN           string
	RedisAddr          string
	RedisPassword      string
	QdrantURL          string
	AIMode             string
	AIChatBaseURL      string
	AIChatAPIKey       string
	AIChatModel        string
	AIEmbeddingBaseURL string
	AIEmbeddingAPIKey  string
	AIEmbeddingModel   string
	EmbeddingDim       int
	TaskTimeout        time.Duration
	LeaseDuration      time.Duration
	ShutdownGrace      time.Duration
	ReportDir          string
}

func Load() Config {
	// 默认 fake 模式保证仓库开箱可验证；real 模式必须显式提供两类模型配置。
	return Config{
		HTTPAddr:           env("HTTP_ADDR", "127.0.0.1:8080"),
		MySQLDSN:           env("MYSQL_DSN", "learnq:learnq@tcp(127.0.0.1:3306)/learnq?parseTime=true&charset=utf8mb4&multiStatements=true"),
		RedisAddr:          env("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:      os.Getenv("REDIS_PASSWORD"),
		QdrantURL:          env("QDRANT_URL", "http://127.0.0.1:6333"),
		AIMode:             env("AI_MODE", "fake"),
		AIChatBaseURL:      env("AI_CHAT_BASE_URL", "https://api.deepseek.com"),
		AIChatAPIKey:       os.Getenv("AI_CHAT_API_KEY"),
		AIChatModel:        env("AI_CHAT_MODEL", "deepseek-v4-pro"),
		AIEmbeddingBaseURL: env("AI_EMBEDDING_BASE_URL", "http://127.0.0.1:11434/v1"),
		AIEmbeddingAPIKey:  os.Getenv("AI_EMBEDDING_API_KEY"),
		AIEmbeddingModel:   env("AI_EMBEDDING_MODEL", "qwen3-embedding:0.6b"),
		EmbeddingDim:       envInt("EMBEDDING_DIM", 64),
		TaskTimeout:        45 * time.Second,
		LeaseDuration:      60 * time.Second,
		ShutdownGrace:      70 * time.Second,
		ReportDir:          env("REPORT_DIR", "data/reports"),
	}
}

func (c Config) Validate() error {
	// 在进程启动阶段集中失败，避免任务领取后才发现密钥或 URL 配置不可用。
	if c.EmbeddingDim <= 0 {
		return fmt.Errorf("EMBEDDING_DIM must be a positive integer")
	}
	switch c.AIMode {
	case "fake":
		return nil
	case "real":
		missing := make([]string, 0, 7)
		for key, value := range map[string]string{
			"AI_CHAT_BASE_URL": c.AIChatBaseURL, "AI_CHAT_API_KEY": c.AIChatAPIKey,
			"AI_CHAT_MODEL": c.AIChatModel, "AI_EMBEDDING_BASE_URL": c.AIEmbeddingBaseURL,
			"AI_EMBEDDING_API_KEY": c.AIEmbeddingAPIKey, "AI_EMBEDDING_MODEL": c.AIEmbeddingModel,
		} {
			if strings.TrimSpace(value) == "" {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("real AI mode requires %s", strings.Join(missing, ", "))
		}
		for key, value := range map[string]string{
			"AI_CHAT_BASE_URL":      c.AIChatBaseURL,
			"AI_EMBEDDING_BASE_URL": c.AIEmbeddingBaseURL,
		} {
			parsed, err := url.Parse(value)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("%s must be an absolute http(s) URL", key)
			}
		}
		return nil
	default:
		return fmt.Errorf("AI_MODE must be fake or real")
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}
