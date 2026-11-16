package middleware

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func TestRateLimitValidatesConfig(t *testing.T) {
	invalid := []RateLimitConfig{
		{Capacity: 0, Rate: 2, KeyPrefix: "p:"},
		{Capacity: 10, Rate: 0, KeyPrefix: "p:"},
		{Capacity: 10, Rate: 2, KeyPrefix: ""},
	}
	for _, cfg := range invalid {
		if _, err := RateLimit(nil, cfg); err == nil {
			t.Fatalf("config %+v should return an error", cfg)
		}
	}

	valid := RateLimitConfig{Capacity: 10, Rate: 2, KeyPrefix: "p:"}
	if _, err := RateLimit(nil, valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

// silentServer 起一个只接受连接、永不回包的 TCP 服务，用来模拟「Redis 卡住」。
//
// 为什么不用 miniredis 的故障注入：它返回的是立刻失败的连接错误，
// 那测的是「连不上」而不是「连上了但很慢」。真正拖慢请求的是后者，
// 也是线上最常见的形态（Redis 在做大 key 删除、被 CLIENT PAUSE、机器 swap）。
func silentServer(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	// 接受连接后什么都不做，也不关：对端会一直读不到响应，直到超时。
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // 监听被关掉，正常退出
			}
			_ = conn // 故意不关，让对端卡在等响应上
		}
	}()
	return ln.Addr().String()
}

// TestRateLimitFailsOpenFast 验证 Redis 卡住时限流快速降级放行。
//
// 这条约束比「限流准不准」重要得多：限流在请求链路最前面，
// 每个请求都要过。如果它要等满客户端的 500ms 才降级，
// Redis 抖一下就会让所有请求都慢半秒，HTTP 连接迅速堆积，
// 整站跟着雪崩。所以这里断言的是耗时有上限，而不是状态码好看。
func TestRateLimitFailsOpenFast(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: silentServer(t),
		// 和 config.InitRedis 保持一致的两个关键项：
		// ContextTimeoutEnabled 让 ctx 超时能传到 socket 读写，
		// MaxRetries=-1 关闭重试（否则失败三次，每次各等一轮）。
		ContextTimeoutEnabled: true,
		ReadTimeout:           500 * time.Millisecond,
		MaxRetries:            -1,
	})
	t.Cleanup(func() { _ = rdb.Close() })

	mw, err := RateLimit(rdb, RateLimitConfig{Capacity: 10, Rate: 2, KeyPrefix: "test:rl:"})
	if err != nil {
		t.Fatalf("构造中间件失败: %v", err)
	}

	r := gin.New()
	r.Use(mw)
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.RemoteAddr = "203.0.113.7:12345"

	begin := time.Now()
	r.ServeHTTP(w, req)
	elapsed := time.Since(begin)

	// 降级放行：Redis 挂了不该让用户被 429。
	if w.Code != http.StatusOK {
		t.Errorf("status = %d，期望 200（降级放行）, body=%s", w.Code, w.Body.String())
	}

	// 预算 50ms，留一倍余量给调度和建连。超过 200ms 说明没走独立超时，
	// 而是在等客户端的 500ms。
	if elapsed > 200*time.Millisecond {
		t.Errorf("耗时 %s，超过 200ms：限流没有走独立的短超时", elapsed)
	}
}
