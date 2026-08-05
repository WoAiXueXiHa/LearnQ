package bootstrap

import (
	"net/http"

	"github.com/WoAiXueXiHa/LeranQ/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/internal/model"
	"github.com/go-redis/redis/v8"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// MySQL 打开 GORM 连接。GORM 默认在 Open 时自动 ping 一次，连接不可达会立即返回错误；
// 日志级别设为 Warn，正常执行的 SQL 不打印，仅保留警告与错误。
func MySQL(cfg config.Config) (*gorm.DB, error) {
	return gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
}

// Redis 创建客户端但不在启动期建连（惰性连接），连通性由调用方负责：
// Worker 启动时显式 Ping 预检，API 则通过健康检查探活。
func Redis(cfg config.Config) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
}

// Models 按 AI_MODE 返回 Chat 与 Embedding 模型实现。real 模式下两者复用同一个
// OpenAICompatible 类型（分别承载两种角色），各持独立 http.Client，超时均取
// TaskTimeout——模型调用不会长于任务本身的执行窗口。
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

// Vision 按 provider 返回视觉模型实现：ollama 走原生 /api/chat 接口并携带上下文长度，
// openai 走 OpenAI 兼容接口。fake 模式返回 Fake 占位实现：不做真实识别，但保留相同的
// 格式校验与描述契约，使上传→任务→Fencing 持久化链路无模型也能跑通。
func Vision(cfg config.Config) model.VisionModel {
	if cfg.AIMode == "real" {
		if cfg.AIVisionProvider == "ollama" {
			return &model.OllamaVision{
				BaseURL:       cfg.AIVisionBaseURL,
				Model:         cfg.AIVisionModel,
				ContextLength: cfg.AIVisionContext,
				Client:        &http.Client{Timeout: cfg.TaskTimeout},
			}
		}
		return &model.OpenAICompatibleVision{
			BaseURL: cfg.AIVisionBaseURL,
			APIKey:  cfg.AIVisionAPIKey,
			Model:   cfg.AIVisionModel,
			Client:  &http.Client{Timeout: cfg.TaskTimeout},
		}
	}
	return &model.Fake{Dimension: cfg.EmbeddingDim}
}
