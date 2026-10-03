// 项目配置中心，将运行时配置集中定义、加载、校验，目标：
// 开箱可用——每个字段都有合理默认值，git clone 后无需额外配置，直接能跑
// 所有校验都在 Validate() 中，不在任务执行到一半才报错
// 整个进程的配置只有一个 Config struct 实例

package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// 进程内唯一的配置结构：由 Load() 从环境变量填充（含默认值）、Validate() 校验，
// 其余包只读取不改写。
type Config struct {
	AuthoritySnapshotTTL  time.Duration
	ArticleMaxImages      int
	AIDailyBudgetMicroCNY int
	AICallReserveMicroCNY int
	HTTPAddr              string
	MySQLDSN              string
	RedisAddr             string
	RedisPassword         string
	QdrantURL             string
	DocumentChunkVersion  string
	RAGCollection         string
	AIMode                string
	AIChatBaseURL         string
	AIChatAPIKey          string
	AIChatModel           string
	AIEmbeddingBaseURL    string
	AIEmbeddingAPIKey     string
	AIEmbeddingModel      string
	EmbeddingDim          int
	AIVisionProvider      string
	AIVisionBaseURL       string
	AIVisionAPIKey        string
	AIVisionModel         string
	AIVisionContext       int
	TaskTimeout           time.Duration
	LeaseDuration         time.Duration
	ShutdownGrace         time.Duration
	ReportDir             string
	ImageDir              string
}

// 没有参数，只负责组装，不校验，一定会返回一个 Config 实例，但不保证有效
func Load() Config {
	// 默认 fake 模式保证仓库开箱可验证；real 模式必须显式提供 Chat、Embedding 与 Vision 配置。
	mode := env("AI_MODE", "fake") // 默认 fake，保证仓库没有 API key 时也能正常运行
	embeddingModel := env("AI_EMBEDDING_MODEL", "qwen3-embedding:0.6b")
	// 按模式隔离 collection：切换 AI_MODE 不会检索到另一模式写入的向量。
	collection := "learnq_chunks_fake_v1"
	if mode == "real" {
		// 模型名拼入 collection 名前必须清洗：Qdrant 集合名禁止冒号、斜杠等特殊字符，而模型名（如 qwen3-embedding:0.6b）含冒号，不能直接使用。
		collection = "learnq_chunks_real_" + sanitizeCollectionPart(embeddingModel)
	}
	return Config{
		AuthoritySnapshotTTL:  envDuration("AUTHORITY_SNAPSHOT_TTL", 720*time.Hour),
		ArticleMaxImages:      envInt("ARTICLE_MAX_IMAGES", 32),
		AIDailyBudgetMicroCNY: envInt("AI_DAILY_BUDGET_MICROCNY", 0),
		AICallReserveMicroCNY: envInt("AI_CALL_RESERVE_MICROCNY", 0),
		// 默认 DSN 开启 multiStatements：迁移文件里的多条 DDL 才能在一次 Exec 中执行。
		// Chat/Embedding 密钥不用 env() 兜底：未设置则保持空串，交由 Validate() 判定；AI_VISION_API_KEY 例外（有 "ollama" 默认值，见 Validate 注释）。
		HTTPAddr:             env("HTTP_ADDR", "127.0.0.1:8080"),
		MySQLDSN:             env("MYSQL_DSN", "learnq:learnq@tcp(127.0.0.1:3306)/learnq?parseTime=true&charset=utf8mb4&multiStatements=true"),
		RedisAddr:            env("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:        os.Getenv("REDIS_PASSWORD"),
		QdrantURL:            env("QDRANT_URL", "http://127.0.0.1:6333"),
		RAGCollection:        env("RAG_COLLECTION", collection),
		DocumentChunkVersion: env("DOCUMENT_CHUNK_VERSION", "markdown-block-800-v1"),
		AIMode:               mode,
		AIChatBaseURL:        env("AI_CHAT_BASE_URL", "https://api.deepseek.com"),
		AIChatAPIKey:         os.Getenv("AI_CHAT_API_KEY"),
		AIChatModel:          env("AI_CHAT_MODEL", "deepseek-v4-pro"),
		AIEmbeddingBaseURL:   env("AI_EMBEDDING_BASE_URL", "http://127.0.0.1:11434/v1"),
		AIEmbeddingAPIKey:    os.Getenv("AI_EMBEDDING_API_KEY"),
		AIEmbeddingModel:     embeddingModel,
		EmbeddingDim:         envInt("EMBEDDING_DIM", 1024),
		AIVisionProvider:     env("AI_VISION_PROVIDER", "ollama"),
		AIVisionBaseURL:      env("AI_VISION_BASE_URL", "http://127.0.0.1:11434/v1"),
		AIVisionAPIKey:       env("AI_VISION_API_KEY", "ollama"),
		AIVisionModel:        env("AI_VISION_MODEL", "qwen3-vl:4b"),
		AIVisionContext:      envInt("AI_VISION_CONTEXT_LENGTH", 8192),
		TaskTimeout:          envDuration("TASK_TIMEOUT", 45*time.Second),
		LeaseDuration:        envDuration("LEASE_DURATION", 60*time.Second),
		ShutdownGrace:        envDuration("SHUTDOWN_GRACE", 70*time.Second),
		ReportDir:            env("REPORT_DIR", "data/reports"),
		ImageDir:             env("IMAGE_DIR", "data/images"),
	}
}

// Validate 保留完整校验语义，兼容需要同时运行所有 AI 能力的调用方。
func (c Config) Validate() error { return c.validate(true) }

// ValidateAPI 校验 API 进程实际需要的配置。Vision 只由 Worker 使用，
// 因此视觉服务故障或未配置不应阻止核心 API 启动。
func (c Config) ValidateAPI() error { return c.validate(false) }

// ValidateWorker 校验 Worker 承载的完整 AI 能力。
func (c Config) ValidateWorker() error { return c.validate(true) }

// validate 在进程启动阶段集中失败，避免任务领取后才发现密钥或 URL 配置不可用。
func (c Config) validate(requireVision bool) error {
	if c.AuthoritySnapshotTTL <= 0 || c.AuthoritySnapshotTTL > 365*24*time.Hour {
		return fmt.Errorf("AUTHORITY_SNAPSHOT_TTL must be positive and at most 8760h")
	}
	if c.DocumentChunkVersion != "" && c.DocumentChunkVersion != "markdown-block-800-v1" && c.DocumentChunkVersion != "rune-800-overlap-120-v1" {
		return fmt.Errorf("DOCUMENT_CHUNK_VERSION must be markdown-block-800-v1 or rune-800-overlap-120-v1")
	}
	// 不区分模型，所有模式可用
	if c.EmbeddingDim <= 0 {
		return fmt.Errorf("EMBEDDING_DIM must be a positive integer")
	}
	// lease租约：我做这个任务存活的时间
	// timeout超时：必须在这个时间节点前交付
	// 必须保证活干完了才能交接，别租约比超时还小，活干一半滚蛋了，很尴尬
	// 或者活干完了，交付不了，也很尴尬
	// 通俗理解：
	// 给你一把有时限的🔑，超时自动失效，想要提交结果，必须先验证🔑有效，过期就滚蛋
	if c.TaskTimeout <= 0 || c.LeaseDuration <= c.TaskTimeout {
		return fmt.Errorf("LEASE_DURATION must be greater than TASK_TIMEOUT")
	}
	if c.ShutdownGrace <= 0 {
		return fmt.Errorf("SHUTDOWN_GRACE must be greater than zero")
	}
	// 按照模型分支
	switch c.AIMode {
	case "fake":
		return nil
	case "real":
		missing := make([]string, 0, 9)
		// 减少 if 重复代码，以后新加入字段只需要在 map 里加一行就行
		required := map[string]string{
			"AI_CHAT_BASE_URL":      c.AIChatBaseURL,
			"AI_CHAT_API_KEY":       c.AIChatAPIKey,
			"AI_CHAT_MODEL":         c.AIChatModel,
			"AI_EMBEDDING_BASE_URL": c.AIEmbeddingBaseURL,
			"AI_EMBEDDING_API_KEY":  c.AIEmbeddingAPIKey,
			"AI_EMBEDDING_MODEL":    c.AIEmbeddingModel,
		}
		if requireVision {
			required["AI_VISION_BASE_URL"] = c.AIVisionBaseURL
			required["AI_VISION_MODEL"] = c.AIVisionModel
		}
		for key, value := range required {
			if strings.TrimSpace(value) == "" {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("real AI mode requires %s", strings.Join(missing, ", "))
		}
		if requireVision {
			// 视觉配置按 provider 分叉校验：ollama 原生接口依赖上下文长度，openai 兼容接口依赖 API key。
			// 注意 AIVisionAPIKey 在 ollama 分支有 "ollama" 默认值，因此只对 openai 分支强制检查。
			if c.AIVisionProvider != "ollama" && c.AIVisionProvider != "openai" {
				return fmt.Errorf("AI_VISION_PROVIDER must be ollama or openai")
			}
			if c.AIVisionProvider == "ollama" && c.AIVisionContext <= 0 {
				return fmt.Errorf("AI_VISION_CONTEXT_LENGTH must be a positive integer")
			}
			if c.AIVisionProvider == "openai" && strings.TrimSpace(c.AIVisionAPIKey) == "" {
				return fmt.Errorf("real AI mode with openai vision requires AI_VISION_API_KEY")
			}
		}
		// url.Parse 对无协议前缀的字符串很宽容（如 "localhost:11434" 会被解析为 scheme=localhost、无 host），
		// 必须显式要求 http(s) 且带主机名，否则下游会以非预期根地址发起请求。
		urls := map[string]string{
			"AI_CHAT_BASE_URL":      c.AIChatBaseURL,
			"AI_EMBEDDING_BASE_URL": c.AIEmbeddingBaseURL,
		}
		if requireVision {
			urls["AI_VISION_BASE_URL"] = c.AIVisionBaseURL
		}
		for key, value := range urls {
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

// env 读取环境变量：空串视为未设置，回退到默认值。
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// envInt 解析失败返回 0 而非 fallback：非法数值会命中 Validate 的边界检查而拒绝启动，
// 静默回退默认值反而会掩盖配置拼写错误。
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

// envDuration 与 envInt 同策略：解析失败返回 0，再由 Config 校验拒绝非法值。
func envDuration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0
	}
	return value
}

// sanitizeCollectionPart 把模型名清洗为 Qdrant 合法的集合名片段：
// 仅保留小写字母与数字，其余字符替换为下划线，再去除首尾下划线。
func sanitizeCollectionPart(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	return strings.Trim(builder.String(), "_")
}
