// Package config 负责加载项目配置（viper）。
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config 对应 config.yaml 的整体结构，一个字段对应 yaml 里的一个顶层键。
// mapstructure 标签写的是 yaml 里的键名，两者不一致时以标签为准。
// 新增配置项的标准动作：加字段 + 加 mapstructure 标签 + 在 Load 里决定要不要给默认值。
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	MySQL     MySQLConfig     `mapstructure:"mysql"`
	Redis     RedisConfig     `mapstructure:"redis"`
	JWT       JWTConfig       `mapstructure:"jwt"`
	WS        WSConfig        `mapstructure:"ws"`
	RateLimit RateLimitConfig `mapstructure:"rate_limit"`
	CORS      CORSConfig      `mapstructure:"cors"`
	Log       LogConfig       `mapstructure:"log"`
	Metrics   MetricsConfig   `mapstructure:"metrics"`
	Admin     AdminConfig     `mapstructure:"admin"`
	LLM       LLMConfig       `mapstructure:"llm"`
}

// AdminConfig 是管理类接口的白名单。
//
// 项目里没有角色表，也不打算为了一两个运营接口引入一套 RBAC：
// 需要权限的接口就那几个，把用户 id 列在配置里最直接，改了重启即可生效。
// 代价是「谁算管理员」只能改文件，不能在线授予，这对当前规模是合理的。
type AdminConfig struct {
	// UserIDs 是允许调用管理接口的用户 id。
	//
	// 留空表示没有人可以调用，这是刻意的安全默认：
	// 忘记配置时接口是关闭的，而不是对全部登录用户开放。
	// 反过来的话，一次配置遗漏就等于把创建活动的权限交给了任何注册用户。
	UserIDs []uint `mapstructure:"user_ids"`
}

type ServerConfig struct {
	Port int `mapstructure:"port"`
	// TrustedProxies 是允许解析 X-Forwarded-For 的反向代理地址。
	// 留空表示不信任任何代理，ClientIP() 取直连 IP。
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

type MySQLConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	DBName   string `mapstructure:"dbname"`
	// AutoMigrate 决定服务启动时要不要执行建表。
	// 本机开发开着方便（改模型后直接重启）；生产建议关掉，
	// 改表结构走独立的 migrate 命令，避免多实例同时启动时并发建表。
	AutoMigrate bool `mapstructure:"auto_migrate"`
	// SlowQueryMS 是慢查询阈值，超过它的 SQL 会按 warn 级别记一条日志。
	// 设成 0 表示用默认值 200ms；调小能抓到更多可疑查询，代价是日志变多。
	SlowQueryMS int `mapstructure:"slow_query_ms"`
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	// PoolSize 是连接池上限。0 表示交给 go-redis 按 CPU 核数算（10 * GOMAXPROCS）。
	// 显式设一个值只在「Redis 有连接数配额」时才需要，比如云托管的免费档。
	PoolSize int `mapstructure:"pool_size"`
}

type JWTConfig struct {
	Secret string `mapstructure:"secret"`
	Expire int    `mapstructure:"expire"` // 单位：小时
}

type WSConfig struct {
	// AllowedOrigins 是允许连 WebSocket 的页面来源，只写 host:port，不带协议。
	// 浏览器握手会带 Origin 头，服务端拿 Host 部分和这里逐个比对。
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

// RateLimitConfig 是令牌桶限流参数，含义见 internal/middleware/ratelimit.go。
type RateLimitConfig struct {
	Capacity  int     `mapstructure:"capacity"`
	Rate      float64 `mapstructure:"rate"`
	KeyPrefix string  `mapstructure:"key_prefix"`
}

// CORSConfig 只在前后端分属不同域名时才需要填，同域反代留空即可。
type CORSConfig struct {
	// AllowedOrigins 必须写完整来源，例如 https://example.com（含协议、不带路径）。
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

// LogConfig 决定日志打到哪、打成什么格式，具体组装见 pkg/logging。
type LogConfig struct {
	// Level 取 debug / info / warn / error。线上用 info，排查问题临时调 debug。
	Level string `mapstructure:"level"`
	// Format 取 json / text。json 便于机器检索，text 便于本机肉眼读。
	Format string `mapstructure:"format"`
	// Console 控制是否同时写标准输出。容器和穿透工具靠 stdout 收日志，线上必须为 true。
	Console bool `mapstructure:"console"`
	// File.Path 为空表示不落盘。没有 systemd/journald 的机器才需要文件，
	// 落盘后要自己注意磁盘占用，靠 MaxSizeMB * MaxBackups 兜住上限。
	File LogFileConfig `mapstructure:"file"`
}

// MetricsConfig 控制 /metrics 抓取端点。
//
// 端点挂在根路径、不进 /api，因此不限流：抓取方（Prometheus）本该高频轮询，
// 挤占业务令牌桶会把正常用户限死。
type MetricsConfig struct {
	// Enabled 为 false 时完全不注册这个路由，请求会落到统一的 404。
	Enabled bool `mapstructure:"enabled"`
	// Token 非空时要求请求带 Authorization: Bearer <token>。
	// 指标里有路由名和流量规模，直接暴露在公网等于给扫描器送情报；
	// 内网抓取一般靠 Nginx 的 allow/deny 限制，跨网段抓取才用 token。
	Token string `mapstructure:"token"`
}

// LLMConfig 是自动生成标签用的大模型接入参数。
//
// 目前只支持 OpenAI 兼容的 /chat/completions 协议，智谱、DeepSeek、本地 vLLM
// 都走这一套，换供应商只改 base_url 与 model，不用改代码。
//
// Enabled 与 APIKey 都要成立才会真的发请求：只填地址没填密钥时按关闭处理，
// 而不是每次发布都发一次注定 401 的请求、再在日志里刷一行失败。
type LLMConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// BaseURL 不含结尾的 /chat/completions，只到版本段，例如
	// https://open.bigmodel.cn/api/paas/v4
	BaseURL string `mapstructure:"base_url"`
	// APIKey 是供应商密钥，属于机密：只能写在被 .gitignore 忽略的 config.yaml 里，
	// 或者用环境变量 LLM_API_KEY 覆盖，不要写进仓库里的任何模板文件。
	APIKey string `mapstructure:"api_key"`
	Model  string `mapstructure:"model"`
	// TimeoutSeconds 是单次生成的上限。
	// 它同时是「发布文章最多被拖慢多久」：文章已经写进库，标签只是附加信息，
	// 超时就直接放弃这个标签，不能让一次网络抖动把发布卡住十几秒。
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
}

// LogFileConfig 是日志文件的切分与保留策略。
type LogFileConfig struct {
	Path       string `mapstructure:"path"`
	MaxSizeMB  int    `mapstructure:"max_size_mb"`
	MaxBackups int    `mapstructure:"max_backups"`
	MaxAgeDays int    `mapstructure:"max_age_days"`
}

// HMAC-SHA256 的密钥强度下限：256 bit 对应 32 个字符，短于这个长度可被暴力猜解。
const minJWTSecretLength = 32

// 这几个值出现在 config.example.yaml 和旧版文档里，等于公开。
// 有人直接复制模板上线时，靠这张表在启动期拦住。
var weakJWTSecrets = map[string]bool{
	"change_me_to_a_long_random_string": true,
	"replace_with_a_long_random_secret": true,
}

// Load 是配置的唯一入口，启动时被 cmd/server/main.go 调用，出错则进程直接退出。
//
// 整个流程分五步：注册默认值 → 决定读哪个文件 → 读文件 → 用环境变量覆盖 → 转成结构体并校验。
// 敏感项（数据库密码、JWT 密钥）可以用同名环境变量顶掉 yaml 里的值，不必写进配置文件。
func Load() (*Config, error) {
	v := viper.New()

	// 第一步：注册默认值。yaml 里没写的项就用这里的值，保证结构体里不会是零值。
	v.SetDefault("server.port", 8080)
	// 列表类型的键必须注册一个空列表。viper 的 Unmarshal 只遍历「已知的键」，
	// 既不在 yaml 又没有默认值的键会被直接忽略，导致环境变量写了也不生效。
	v.SetDefault("server.trusted_proxies", []string{})
	// 默认开自动迁移：本机开发改完模型重启就生效，不用多敲一条命令。
	// 生产配置里显式写 mysql.auto_migrate: false，改表交给 migrate 命令。
	v.SetDefault("mysql.auto_migrate", true)
	v.SetDefault("jwt.expire", 24)
	v.SetDefault("rate_limit.capacity", 10)
	v.SetDefault("rate_limit.rate", 2)
	v.SetDefault("rate_limit.key_prefix", "forum:dev:rate:")
	v.SetDefault("cors.allowed_origins", []string{})
	// 管理员白名单默认是空列表：不写就是「没人能调管理接口」。
	// 同样必须显式注册默认值，否则 yaml 里没写这一项时 Unmarshal 会跳过整个键。
	v.SetDefault("admin.user_ids", []uint{})
	// 慢查询阈值 200ms：低于它的 SQL 在开发机上看不出慢，抓到的都是真该改的。
	v.SetDefault("mysql.slow_query_ms", 200)
	// 日志默认按 JSON 打控制台。本机想看人话就在 config.yaml 里把 format 改成 text。
	// 文件留空：开发机不需要落盘，免得 logs/ 越攒越大；生产配置里显式开。
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("log.console", true)
	v.SetDefault("log.file.path", "")
	v.SetDefault("log.file.max_size_mb", 50)
	v.SetDefault("log.file.max_backups", 7)
	v.SetDefault("log.file.max_age_days", 30)
	// 指标默认开着：本机看 /metrics 不需要额外配置，生产模板里按需加 token。
	v.SetDefault("metrics.enabled", true)
	v.SetDefault("metrics.token", "")
	// 自动标签默认关闭：没有密钥时开着只会让每次发布多等一次网络失败。
	// 打开需要在 config.yaml 里显式写 enabled: true 并填 api_key。
	// base_url / model 给默认值，是为了让配置里只需要出现「开关 + 密钥」两行；
	// 换供应商时再覆盖这两项。超时 8 秒：正常的短文本分类不到 1 秒，
	// 8 秒还没回来说明链路有问题，继续等只会拖慢发布。
	v.SetDefault("llm.enabled", false)
	v.SetDefault("llm.base_url", "https://open.bigmodel.cn/api/paas/v4")
	v.SetDefault("llm.model", "glm-4-flash")
	v.SetDefault("llm.api_key", "")
	v.SetDefault("llm.timeout_seconds", 8)

	// 第二步：决定读哪个文件。部署时用 CONFIG_PATH 传绝对路径，本机默认读项目内的 config/config.yaml。
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config/config.yaml"
	}
	v.SetConfigFile(configPath)

	// 第三步：读文件。文件不存在和文件语法错误要分开报，
	// 否则运维看到一句 read config 根本不知道该改路径还是改格式。
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("config file not found: %w", err)
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	// 第四步：打开环境变量覆盖。AutomaticEnv 让 viper 去查环境变量，
	// Replacer 把层级键的 . 换成 _，于是 jwt.secret 对应环境变量 JWT_SECRET，
	// server.trusted_proxies 对应 SERVER_TRUSTED_PROXIES（列表用逗号分隔）。
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// 第五步：把 viper 里的键值对填进结构体。
	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// 最后做一次安全校验：密钥太短或被换回仓库里公开的示例值时直接拒绝启动。
	// 用弱密钥签发 token 等于没有鉴权，谁都能伪造任意用户身份。
	if len(cfg.JWT.Secret) < minJWTSecretLength || weakJWTSecrets[cfg.JWT.Secret] {
		return nil, fmt.Errorf("config: jwt.secret must be at least %d random characters and not a published placeholder", minJWTSecretLength)
	}
	return cfg, nil
}
