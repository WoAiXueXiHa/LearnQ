package bootstrap

import (
	"net/http"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/model"
	"github.com/go-redis/redis/v8"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func MySQL(cfg config.Config) (*gorm.DB, error) {
	return gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
}

func Redis(cfg config.Config) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
}

func Models(cfg config.Config) (model.ChatModel, model.EmbeddingModel) {
	if cfg.AIMode == "real" {
		chat := &model.OpenAICompatible{
			BaseURL: cfg.AIChatBaseURL, APIKey: cfg.AIChatAPIKey, ChatModel: cfg.AIChatModel,
			Client: &http.Client{Timeout: cfg.TaskTimeout},
		}
		embedding := &model.OpenAICompatible{
			BaseURL: cfg.AIEmbeddingBaseURL, APIKey: cfg.AIEmbeddingAPIKey,
			EmbeddingModel: cfg.AIEmbeddingModel, Dimension: cfg.EmbeddingDim,
			Client: &http.Client{Timeout: cfg.TaskTimeout},
		}
		return chat, embedding
	}
	fake := &model.Fake{Dimension: cfg.EmbeddingDim}
	return fake, fake
}
