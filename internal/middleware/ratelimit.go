package middleware

import (
	"fmt"
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
	Capacity  int
	Rate      float64
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
-- TTL 取补满整桶所需时间的两倍，活跃 key 每次请求续期，闲置后自动释放。
local ttl_ms = math.max(1000, math.ceil(capacity * 2000 / rate))
redis.call('PEXPIRE', key, ttl_ms)
return { allowed, retry_ms }
`

// RateLimit 令牌桶限流，参数在装配时校验一次。
func RateLimit(rdb *redis.Client, cfg RateLimitConfig) (gin.HandlerFunc, error) {
	if cfg.Capacity < 1 || cfg.Rate <= 0 || cfg.KeyPrefix == "" {
		return nil, fmt.Errorf("middleware: invalid rate limit config: %+v", cfg)
	}

	script := redis.NewScript(tokenBucketLua)

	return func(c *gin.Context) {
		key := cfg.KeyPrefix + "ip:" + c.ClientIP()
		res, err := script.Run(c.Request.Context(), rdb, []string{key}, cfg.Capacity, cfg.Rate).Slice()
		if err != nil {
			// Redis 抖动时降级放行，避免一次依赖故障演变成全站 500。
			log.Printf("rate limit check %s: %v", key, err)
			c.Next()
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
	}, nil
}
