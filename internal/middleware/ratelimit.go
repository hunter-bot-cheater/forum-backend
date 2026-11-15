package middleware

import (
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"forum-backend/pkg/response"
)

// RateLimitConfig 是限流参数。
// Capacity 是桶容量，也是允许的瞬时突发量。
// Rate 是每秒补充的令牌数，决定长期平均速率。
type RateLimitConfig struct {
	Capacity  int     //Capacity（容量）：桶最多能存多少个令牌。决定了允许的瞬时最大爆发量
	Rate      float64 //Rate（速率）：每秒往桶里扔多少个令牌。决定了长期的平均访问速率
	KeyPrefix string
}

// tokenBucketLua 在 Redis 服务端一次完成读状态、补令牌、扣令牌、写回与设过期。
// KEYS[1] 是限流 key，ARGV[1] 是容量，ARGV[2] 是每秒补充速率。
// 返回 { allowed, retry_ms }，两个都是整数，交给 Go 层组装 HTTP 响应。
const tokenBucketLua = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])

-- Redis TIME 让多个应用实例共享同一个时钟，避免本机时间偏差。
local t = redis.call('TIME')
local now_ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local tokens = tonumber(redis.call('HGET', key, 'tokens')) or capacity
local last_ms = tonumber(redis.call('HGET', key, 'last_ms')) or now_ms
local elapsed_ms = math.max(0, now_ms - last_ms)

-- 按经过时间补令牌，上限是容量。时钟回拨时 elapsed_ms 被夹成 0。
tokens = math.min(capacity, tokens + elapsed_ms * rate / 1000)
local allowed = 0
local retry_ms = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  -- 还差多少令牌，换算成需要等多少毫秒。
  retry_ms = math.ceil((1 - tokens) * 1000 / rate)
end

redis.call('HSET', key, 'tokens', tokens, 'last_ms', now_ms)
-- TTL 至少 1 秒，且大于补满整桶所需时间的两倍，避免活跃 key 被提前清掉。
local ttl_ms = math.max(1000, math.ceil(capacity * 2000 / rate))  -- 从空桶补满所需时间，乘 2 就是 TTL
redis.call('PEXPIRE', key, ttl_ms)  --每次用户发起请求,PEXPIRE 都会把这个 Key 的存活时间续期到这个长度
-- 一旦用户超过「补满时间 * 2」没有再发请求，这个 IP 就算没有活动了，
-- 对应的 Key 会自动过期销毁，释放内存。
return { allowed, retry_ms }
`

// RateLimit 令牌桶限流。参数在构造时校验，出错让服务启动期失败。
func RateLimit(rdb *redis.Client, cfg RateLimitConfig) gin.HandlerFunc {
	if cfg.Capacity < 1 {
		panic("RateLimit: Capacity must be positive")
	}
	if cfg.Rate <= 0 {
		panic("RateLimit: Rate must be positive")
	}
	if cfg.KeyPrefix == "" {
		panic("RateLimit: KeyPrefix is required")
	}

	script := redis.NewScript(tokenBucketLua)

	return func(c *gin.Context) {
		key := cfg.KeyPrefix + "ip:" + c.ClientIP() //按照客户端ip限流
		res, err := script.Run(
			c.Request.Context(), // ctx：请求取消会中断等待（Redis 端脚本仍会跑完）
			rdb,                 // *redis.Client
			[]string{key},       // KEYS[1]，如 "ratelimit:ip:1.2.3.4"
			cfg.Capacity,        // ARGV[1] 桶容量（允许的突发量）
			cfg.Rate,            // ARGV[2] 每秒补充的令牌数
		).Slice() // 把 Lua 返回的 table 解析成 []interface{}
		if err != nil {
			log.Printf("rate limit check %s: %v", key, err)
			c.Next() // fail-open，如果redis 挂了 那么请求继续 不因为redis挂了就把所有请求拒绝
			return
		}
		allowed := res[0].(int64)
		retryMs := res[1].(int64)

		if allowed == 0 {
			// tokens < 1 时 retry_ms 至少为 1，向上取整后必然 >= 1 秒。
			retrySec := (retryMs + 999) / 1000
			c.Header("Retry-After", strconv.FormatInt(retrySec, 10))
			response.ErrorWithCode(c, http.StatusTooManyRequests, response.ErrCodeRateLimited, "请求太频繁，请稍后再试")
			c.Abort()
			return
		}
		c.Next()
	}
}
