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
	// 选择模型实现集中在装配层，领域代码只依赖接口。这样 fake 模式可以离线、确定性地
	// 覆盖完整工作流，real 模式也不会把供应商 SDK 类型扩散到业务包。
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
