package testutil

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// NewRedis 起一个进程内的 Redis（miniredis），返回连到它的客户端。
//
// 每个测试一份独立实例，测试之间不会互相看到对方写的 key，
// 也不用担心跑测试把本地开发库里的热榜、限流桶清掉。
// 测试结束自动关闭，不需要调用方 defer。
func NewRedis(t *testing.T) *redis.Client {
	t.Helper()

	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("启动内存 Redis 失败: %v", err)
	}
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		// 关不掉只记一笔：测试已经结束，此时 t.Fatal 会 panic 且没有意义。
		if err := client.Close(); err != nil {
			t.Logf("关闭测试 Redis 客户端失败: %v", err)
		}
	})
	return client
}
