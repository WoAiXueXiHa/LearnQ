package bootstrap

import (
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
		real := &model.OpenAICompatible{
			BaseURL: cfg.AIBaseURL, APIKey: cfg.AIAPIKey, ChatModel: cfg.AIChatModel,
			EmbeddingModel: cfg.AIEmbeddingModel, Dimension: cfg.EmbeddingDim,
		}
		return real, real
	}
	fake := &model.Fake{Dimension: cfg.EmbeddingDim}
	return fake, fake
}
