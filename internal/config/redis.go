package config

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

func InitRedis(cfg *RedisConfig) (*redis.Client, error) {
	if cfg==nil  {
		return nil,fmt.Errorf("redis config is nil ")
	}
	if cfg.Addr == ""{
		return nil,fmt.Errorf("redis addr is empty")
	}
	rdb:=redis.NewClient(&redis.Options{   //创建客户端  创建一个 *redis.Client 对象   初始化连接池（但池子是空的）
		Addr:cfg.Addr,
		Password: cfg.Password,
		DB:cfg.DB,
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
		// 依据见 v9.23.0 的 options.go:174-176 与 redis.go:1889-1896。
		ContextTimeoutEnabled: true,
	})
	ctx,cancel:=context.WithTimeout(context.Background(),3*time.Second) 
	defer cancel()
	if err:=rdb.Ping(ctx).Err();err!=nil{//Ping 测连通性
		_=rdb.Close() // 连接失败就关闭客户端，释放资源
		return nil,fmt.Errorf("connect redis:%w",err)
	}

	return rdb,nil
	
}
