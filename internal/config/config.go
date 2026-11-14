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
	Server ServerConfig `mapstructure:"server"`
	MySQL  MySQLConfig  `mapstructure:"mysql"`
	Redis  RedisConfig  `mapstructure:"redis"`
	JWT    JWTConfig    `mapstructure:"jwt"`
}

type ServerConfig struct {
	Port int `mapstructure:"port"`
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

// Load 从 config/config.yaml 读取配置。
// 健壮性说明：
//  1. 配置文件路径可用环境变量 CONFIG_PATH 指定（部署时传绝对路径），默认 config/config.yaml
//  2. 关键项有默认值兜底，缺失的必填项直接报错，绝不静默用空值
//  3. 所有错误都带上下文，方便排查
//  4. 区分"文件不存在"和"文件格式错误"
func Load() (*Config, error) {
	v := viper.New()

	// 默认值：配置文件里没写的项用这里的兜底，避免空值
	v.SetDefault("server.port", 8080)
	v.SetDefault("jwt.expire", 24)

	// 明确指定配置文件，不再依赖 CWD 做路径搜索；
	// 部署时可通过环境变量 CONFIG_PATH 传入绝对路径
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config/config.yaml"
	}
	v.SetConfigFile(configPath)

	// 读配置：把"文件不存在"和"格式错误"区分开，错误都带上下文
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound)  || errors.Is(err, fs.ErrNotExist){
			return nil, fmt.Errorf("config file not found: %w", err)
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	// 环境变量覆盖：部署时可通过 JWT_SECRET / MYSQL_PASSWORD 覆盖 yaml，
	// 敏感信息不必写进配置文件
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	_ = v.BindEnv("jwt.secret")
	_ = v.BindEnv("mysql.password")

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// 必填项校验：缺了直接报错，而不是用空密钥签名（等于没有鉴权）
	if cfg.JWT.Secret == "" {
		return nil, fmt.Errorf("config: jwt.secret is required")
	}
	return cfg, nil
}
