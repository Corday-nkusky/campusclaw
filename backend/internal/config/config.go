// Package config 负责把配置从环境变量读入结构体。
//
// 设计约束（见 openspec/changes/add-auth-rbac-class-knowledge/design.md D7）：
//   - 全部凭据只来自环境变量；
//   - 任一必填项缺失时返回错误，由调用方终止启动；
//   - 本包内**不存在**任何内置默认密钥或默认口令。
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是进程的全部配置。字段与 .env.example 的条目一一对应。
type Config struct {
	// JWT 签发与验证
	// SessionSecret 语义 = JWT HMAC-SHA256 签名密钥（沿用 SESSION_SECRET 配置名）
	//
	// add-refresh-token design D1/D6：
	//   AccessTTL  = access token 的 exp - iat（配置 ACCESS_TTL_MINUTES，默认 15 分钟）
	//   RefreshTTL = refresh token 的有效期（配置 REFRESH_TTL_DAYS，默认 30 天）
	// SESSION_TTL_HOURS 降级为 access TTL 的**兼容回退**：仅当 ACCESS_TTL_MINUTES
	// 未设置时用于推导 AccessTTL，避免既有 .env 因缺新项而启动失败。
	SessionSecret []byte
	AccessTTL     time.Duration
	RefreshTTL    time.Duration

	// 数据库
	DBHost     string
	DBPort     string
	DBName     string
	DBUser     string
	DBPassword string

	// 上传
	UploadDir      string
	MaxUploadBytes int64

	// 种子账号口令（明文只用于初始化时算 bcrypt 哈希，不落库）
	SeedTeacherPassword string
	SeedStudentPassword string

	// 登录失败限流
	LoginFailThreshold int
	LoginLockFor       time.Duration

	// 监听
	APIPort string

	// 检索依赖（add-class-scoped-retrieval design.md D9）
	EmbeddingAPIBase string
	EmbeddingAPIKey  string
	EmbeddingModel   string
	EmbeddingDim     int
	ChatAPIBase      string
	ChatAPIKey       string
	ChatModel        string
	QdrantURL        string
	QdrantCollection string
}

// 必填项清单。用于一次性报出所有缺失项，而不是让使用者逐个试。
//
// 注意 SESSION_TTL_HOURS 仍是必填：它是 access TTL 的兼容回退（design D6）。
// 新增的 ACCESS_TTL_MINUTES / REFRESH_TTL_DAYS 有默认值，**不在必填清单**内。
var requiredKeys = []string{
	"SESSION_SECRET",
	"SESSION_TTL_HOURS",
	"DB_HOST",
	"DB_PORT",
	"DB_NAME",
	"DB_USER",
	"DB_PASSWORD",
	"UPLOAD_DIR",
	"MAX_UPLOAD_BYTES",
	"SEED_TEACHER_PASSWORD",
	"SEED_STUDENT_PASSWORD",
	"LOGIN_FAIL_THRESHOLD",
	"LOGIN_LOCK_MINUTES",
	// 检索九项（add-class-scoped-retrieval R8.1：缺任一项启动失败）
	"EMBEDDING_API_BASE",
	"EMBEDDING_API_KEY",
	"EMBEDDING_MODEL",
	"EMBEDDING_DIM",
	"CHAT_API_BASE",
	"CHAT_API_KEY",
	"CHAT_MODEL",
	"QDRANT_URL",
	"QDRANT_COLLECTION",
}

// Load 读取并校验环境变量。
//
// 返回的错误会让 main 直接退出——这是 R7.1 的实现：缺配置即启动失败。
func Load() (*Config, error) {
	if missing := missingKeys(); len(missing) > 0 {
		return nil, fmt.Errorf(
			"缺少必填配置项：%s\n"+
				"（本程序不提供内置默认值，请复制 .env.example 为 .env 并填写）",
			strings.Join(missing, ", "),
		)
	}

	ttlHours, err := strconv.Atoi(os.Getenv("SESSION_TTL_HOURS"))
	if err != nil || ttlHours <= 0 {
		return nil, errors.New("SESSION_TTL_HOURS 必须是正整数小时数")
	}

	// access TTL：优先 ACCESS_TTL_MINUTES；未设则回退 SESSION_TTL_HOURS×60 分钟（design D6）。
	accessTTL := time.Duration(ttlHours) * time.Hour
	if raw := strings.TrimSpace(os.Getenv("ACCESS_TTL_MINUTES")); raw != "" {
		mins, err := strconv.Atoi(raw)
		if err != nil || mins <= 0 {
			return nil, errors.New("ACCESS_TTL_MINUTES 必须是正整数分钟数")
		}
		accessTTL = time.Duration(mins) * time.Minute
	}

	// refresh TTL：缺省 30 天。
	refreshTTL := 30 * 24 * time.Hour
	if raw := strings.TrimSpace(os.Getenv("REFRESH_TTL_DAYS")); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil || days <= 0 {
			return nil, errors.New("REFRESH_TTL_DAYS 必须是正整数天数")
		}
		refreshTTL = time.Duration(days) * 24 * time.Hour
	}

	maxUpload, err := strconv.ParseInt(os.Getenv("MAX_UPLOAD_BYTES"), 10, 64)
	if err != nil || maxUpload <= 0 {
		return nil, errors.New("MAX_UPLOAD_BYTES 必须是正整数字节数")
	}

	failThreshold, err := strconv.Atoi(os.Getenv("LOGIN_FAIL_THRESHOLD"))
	if err != nil || failThreshold <= 0 {
		return nil, errors.New("LOGIN_FAIL_THRESHOLD 必须是正整数")
	}

	lockMinutes, err := strconv.Atoi(os.Getenv("LOGIN_LOCK_MINUTES"))
	if err != nil || lockMinutes <= 0 {
		return nil, errors.New("LOGIN_LOCK_MINUTES 必须是正整数")
	}

	apiPort := os.Getenv("API_PORT")
	if apiPort == "" {
		apiPort = "8080" // 监听端口不是密钥，给默认值不构成安全风险
	}

	// EMBEDDING_DIM：正整数（R8.1 / tasks 1.2）
	dim, err := strconv.Atoi(strings.TrimSpace(os.Getenv("EMBEDDING_DIM")))
	if err != nil || dim <= 0 {
		return nil, errors.New("EMBEDDING_DIM 必须是正整数（向量维度）")
	}

	// 网关 base 去尾部斜杠，统一拼接行为
	trimBase := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

	return &Config{
		SessionSecret:       []byte(os.Getenv("SESSION_SECRET")),
		AccessTTL:           accessTTL,
		RefreshTTL:          refreshTTL,
		DBHost:              os.Getenv("DB_HOST"),
		DBPort:              os.Getenv("DB_PORT"),
		DBName:              os.Getenv("DB_NAME"),
		DBUser:              os.Getenv("DB_USER"),
		DBPassword:          os.Getenv("DB_PASSWORD"),
		UploadDir:           os.Getenv("UPLOAD_DIR"),
		MaxUploadBytes:      maxUpload,
		SeedTeacherPassword: os.Getenv("SEED_TEACHER_PASSWORD"),
		SeedStudentPassword: os.Getenv("SEED_STUDENT_PASSWORD"),
		LoginFailThreshold:  failThreshold,
		LoginLockFor:        time.Duration(lockMinutes) * time.Minute,
		APIPort:             apiPort,
		EmbeddingAPIBase:    trimBase(os.Getenv("EMBEDDING_API_BASE")),
		EmbeddingAPIKey:     os.Getenv("EMBEDDING_API_KEY"),
		EmbeddingModel:      os.Getenv("EMBEDDING_MODEL"),
		EmbeddingDim:        dim,
		ChatAPIBase:         trimBase(os.Getenv("CHAT_API_BASE")),
		ChatAPIKey:          os.Getenv("CHAT_API_KEY"),
		ChatModel:           os.Getenv("CHAT_MODEL"),
		QdrantURL:           trimBase(os.Getenv("QDRANT_URL")),
		QdrantCollection:    os.Getenv("QDRANT_COLLECTION"),
	}, nil
}

// DSN 组装 go-sql-driver/mysql 的连接串。
func (c *Config) DSN() string {
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local&multiStatements=false",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName,
	)
}

func missingKeys() []string {
	var missing []string
	for _, k := range requiredKeys {
		if strings.TrimSpace(os.Getenv(k)) == "" {
			missing = append(missing, k)
		}
	}
	return missing
}
