package config

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// InitRedis 创建 Redis 客户端并做一次 Ping 确认连通。
// redis.NewClient 不会真的建连（池子是空的），所以必须 Ping 一次，
// 否则配置写错要等到第一个业务请求才暴露。
func InitRedis(cfg *RedisConfig) (*redis.Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("redis config is nil")
	}
	if cfg.Addr == "" {
		return nil, fmt.Errorf("redis addr is empty")
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,

		// PoolSize 是连接池上限。默认是 10 * GOMAXPROCS，
		// 在多核机器上会开出很多连接；学习项目写小一点，
		// 出问题时更容易判断「是不是连接不够用」。
		PoolSize: 10,
		// MinIdleConns 预留空闲连接，避免冷启动第一波请求全都在拨号。
		MinIdleConns: 2,

		// 建连、读、写三类超时分开设。不设的话 go-redis 各给 5 秒，
		// 网络卡住时 HTTP 请求会被拖住 5 秒才走到降级分支。
		DialTimeout:  2 * time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,

		// 关键开关：默认 false 时 go-redis 在底层把调用方 ctx 换成 Background，
		// 我们用 context.WithTimeout 设的超时就到不了 socket 读写。
		ContextTimeoutEnabled: true,
	})

	// Ping 单独给 3 秒上限：启动阶段不该被一个卡住的 Redis 拖死。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		// 连不上就关闭客户端，释放已经建好的连接，避免进程退出前泄漏。
		_ = rdb.Close()
		return nil, fmt.Errorf("connect redis: %w", err)
	}

	return rdb, nil
}
