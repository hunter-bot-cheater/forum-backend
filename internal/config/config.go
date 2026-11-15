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

// Config 全局配置
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	MySQL     MySQLConfig     `mapstructure:"mysql"`
	Redis     RedisConfig     `mapstructure:"redis"`
	JWT       JWTConfig       `mapstructure:"jwt"`
	WS        WSConfig        `mapstructure:"ws"`
	RateLimit RateLimitConfig `mapstructure:"rate_limit"`
}

type ServerConfig struct {
	Port           int      `mapstructure:"port"`
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

type MySQLConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	DBName   string `mapstructure:"dbname"`
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
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

type RateLimitConfig struct {
	Capacity  int     `mapstructure:"capacity"`
	Rate      float64 `mapstructure:"rate"`
	KeyPrefix string  `mapstructure:"key_prefix"`
}

const minJWTSecretLength = 32

// 这两个值随仓库公开，用它们启动等于没有鉴权。
var weakJWTSecrets = map[string]bool{
	"change_me_to_a_long_random_string": true,
	"replace_with_a_long_random_secret": true,
}

// Load 从 CONFIG_PATH 指定的文件读取配置，缺省为 config/config.yaml。
// 敏感项由同名环境变量覆盖，密码与密钥不必写进配置文件。
func Load() (*Config, error) {
	v := viper.New()

	v.SetDefault("server.port", 8080)
	// 注册空列表，否则 Unmarshal 不会遍历这个键，环境变量覆盖会失效。
	v.SetDefault("server.trusted_proxies", []string{})
	v.SetDefault("jwt.expire", 24)
	v.SetDefault("rate_limit.capacity", 10)
	v.SetDefault("rate_limit.rate", 2)
	v.SetDefault("rate_limit.key_prefix", "forum:dev:rate:")

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config/config.yaml"
	}
	v.SetConfigFile(configPath)

	// 区分文件缺失与格式错误，启动失败时能直接看出原因
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("config file not found: %w", err)
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if len(cfg.JWT.Secret) < minJWTSecretLength || weakJWTSecrets[cfg.JWT.Secret] {
		return nil, fmt.Errorf("config: jwt.secret must be at least %d random characters and not a published placeholder", minJWTSecretLength)
	}
	return cfg, nil
}
