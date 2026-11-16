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

		// PoolSize 是连接池上限。留空（0）交给 go-redis 按 CPU 核数算，
		// 也就是 10 * GOMAXPROCS。原来写死 10，在 8 核机器上等于把并发上限
		// 压到 10 个连接：Redis 稍微慢一点，连接池立刻占满，
		// 后面的请求全部卡在「等一个空闲连接」上，缓存和限流一起失效。
		PoolSize: cfg.PoolSize,
		// MinIdleConns 预留空闲连接，避免冷启动第一波请求全都在拨号。
		MinIdleConns: 2,

		// 建连、读、写三类超时分开设。不设的话 go-redis 各给 5 秒，
		// 网络卡住时 HTTP 请求会被拖住 5 秒才走到降级分支。
		DialTimeout:  2 * time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,

		// PoolTimeout 是「连接池空了，等别人还回来」的上限，默认是
		// ReadTimeout + 1s，也就是 1.5 秒。这个默认值在故障时非常贵：
		// Redis 卡住时连接全被占着，新请求在这里排队等 1.5 秒才肯降级，
		// 比一次读写超时还长三倍。压到 100ms，和其它 Redis 预算保持同一量级。
		PoolTimeout: 100 * time.Millisecond,

		// MaxRetries 默认是 3 次，带退避。对本项目没有意义：
		// Redis 的每个用途（限流、缓存、热榜）都是可降级的，连不上就应当立刻
		// 走降级分支，重试三次只会把一次故障放大成三倍延迟，
		// 而且三次都失败之后还是要降级，白等。
		// -1 表示关闭重试（0 会被当成「用默认值」，不能写 0）。
		MaxRetries: -1,

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
