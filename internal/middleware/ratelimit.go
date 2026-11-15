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
// Capacity 是桶容量，也是允许的瞬时突发量：攒满后可以连着打这么多次。
// Rate 是每秒补充的令牌数，决定长期平均速率（2 表示平均每秒 2 次）。
// KeyPrefix 是 Redis key 前缀，不同环境或不同服务用不同前缀，避免互相占额度。
type RateLimitConfig struct {
	Capacity  int
	Rate      float64
	KeyPrefix string
}

// 令牌桶的思路：桶里最多放 Capacity 个令牌，按 Rate 持续往里补。
// 每个请求必须拿走 1 个，拿不到就拒绝。
// 相比「固定每秒最多 N 次」，它允许用户短时间攒一波再突发，同时把长期平均速率卡住。
//
// tokenBucketLua 在 Redis 服务端一次完成「补令牌 → 扣令牌 → 写回 → 续期」。
// 必须放在 Redis 里执行才安全：多个后端实例同时扣同一个桶时，
// Lua 脚本是原子的，不会出现两个请求都读到同一个余额、都判定通过的超发。
//
// 入参：KEYS[1] 是限流 key，ARGV[1] 是容量，ARGV[2] 是每秒补充速率。
// 返回：{ allowed, retry_ms }，两个整数，交给 Go 层组装 HTTP 响应。
const tokenBucketLua = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])

-- 用 Redis 自己的 TIME 而不是 Go 的 time.Now()：
-- 多个实例共享同一个时钟，避免某台机器时间快几秒导致多补令牌。
local t = redis.call('TIME')
local now_ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

-- 桶状态用 Hash 存两个字段：当前令牌数 tokens 和上次结算时间 last_ms。
-- 用 Hash 而不是两个独立 key，是因为它们必须同时读写，分开存容易出现只更新一半。
-- key 不存在时按「满桶、此刻刚结算」初始化，也就是新 IP 第一次请求直接放行。
local tokens = tonumber(redis.call('HGET', key, 'tokens')) or capacity
local last_ms = tonumber(redis.call('HGET', key, 'last_ms')) or now_ms

-- 从上一次结算到现在过了多久，按时间补令牌，补到容量为止。
-- elapsed_ms 用 max(0, ...) 夹一下：万一机器时钟回拨，差值算成负数会让令牌反向减少。
local elapsed_ms = math.max(0, now_ms - last_ms)
tokens = math.min(capacity, tokens + elapsed_ms * rate / 1000)

local allowed = 0
local retry_ms = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  -- 不够一个令牌：算出还差几个、按速率要等多少毫秒，回给 Go 层写进 Retry-After。
  retry_ms = math.ceil((1 - tokens) * 1000 / rate)
end

redis.call('HSET', key, 'tokens', tokens, 'last_ms', now_ms)

-- TTL 取「从空桶补满所需时间」的两倍。每次请求都会续期，
-- 所以这是一个滑动过期：IP 闲置超过这个时长，key 自动消失，内存不会无限增长。
local ttl_ms = math.max(1000, math.ceil(capacity * 2000 / rate))
redis.call('PEXPIRE', key, ttl_ms)

return { allowed, retry_ms }
`

// RateLimit 返回按客户端 IP 分桶的令牌桶中间件。
// 参数在装配期校验一次，配错直接让服务起不来，而不是等到每个请求都判一遍。
func RateLimit(rdb *redis.Client, cfg RateLimitConfig) (gin.HandlerFunc, error) {
	if cfg.Capacity < 1 || cfg.Rate <= 0 || cfg.KeyPrefix == "" {
		return nil, fmt.Errorf("middleware: invalid rate limit config: %+v", cfg)
	}

	// NewScript 会先试 EVALSHA（只传脚本摘要），没缓存过才退回 EVAL 传整段脚本，
	// 这样每次请求不用把上面那一大段 Lua 再发一遍。
	script := redis.NewScript(tokenBucketLua)

	return func(c *gin.Context) {
		// 一个 IP 一个桶：同一出口 IP 下的用户共享额度，不同 IP 互不影响。
		// ClientIP() 取的是直连 IP，是否解析 X-Forwarded-For 由 router 的 trusted_proxies 决定。
		key := cfg.KeyPrefix + "ip:" + c.ClientIP()

		res, err := script.Run(c.Request.Context(), rdb, []string{key}, cfg.Capacity, cfg.Rate).Slice()
		if err != nil {
			// Redis 抖动时降级放行，避免一次依赖故障演变成全站 500。
			// 代价是这段时间限流失效，所以要打日志，别静默降级。
			log.Printf("rate limit check %s: %v", key, err)
			c.Next()
			return
		}

		// Lua 返回的整数在 Go 侧统一是 int64，顺序与脚本里的 return 一致。
		allowed := res[0].(int64)
		retryMs := res[1].(int64)

		if allowed == 0 {
			// Retry-After 单位是秒，Lua 给的是毫秒；向上取整，避免算出 0 秒让客户端立刻重试。
			retrySec := (retryMs + 999) / 1000
			c.Header("Retry-After", strconv.FormatInt(retrySec, 10))
			response.ErrorWithCode(c, http.StatusTooManyRequests, response.ErrCodeRateLimited, "请求太频繁，请稍后再试")
			c.Abort()
			return
		}
		c.Next()
	}, nil
}
