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
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
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
