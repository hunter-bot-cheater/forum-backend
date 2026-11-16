// 本文件是登录失败锁定：同一用户名连续输错就暂停验证一段时间。
//
// 它和限流解决的不是同一件事：
//   - 限流按 IP 计数，挡住「一台机器狂打接口」，但撞库的人换 IP 就绕过了；
//   - 锁定按用户名计数，挡住「盯着一个账号猜密码」，换 IP 也没用。
//
// 两者叠加才有意义：IP 维度挡流量，账号维度挡猜解。
package user

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"forum-backend/internal/keys"
)

// 锁定的默认参数。放在这里而不是配置里，是因为调这个值需要同时理解
// 「用户多久会发现自己被锁」和「攻击者 15 分钟能试多少次」，
// 属于安全策略而不是部署差异；真要按环境调，改这一处就够。
const (
	loginMaxAttempts = 5
	loginFailWindow  = 15 * time.Minute
)

// incrWithTTLLua 在同一段脚本里完成「计数 + 首次设置过期」。
//
// 分两次发命令（INCR 再 EXPIRE）有一个危险的窗口：如果 INCR 成功、EXPIRE 失败，
// 这个 key 就永久有效，等于把用户账号永久锁死，而且重启服务也救不回来。
// Lua 在 Redis 里原子执行，不存在只做一半的状态。
const incrWithTTLLua = `
local n = redis.call('INCR', KEYS[1])
if n == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return n
`

// LoginGuard 按用户名统计连续登录失败，达到阈值后在一段时间内拒绝验证。
type LoginGuard struct {
	rdb    *redis.Client
	script *redis.Script
	max    int
	window time.Duration
}

// NewLoginGuard 由装配层创建一次。参数写死成上面两个常量，调用方不必再传。
func NewLoginGuard(rdb *redis.Client) *LoginGuard {
	return &LoginGuard{
		rdb:    rdb,
		script: redis.NewScript(incrWithTTLLua),
		max:    loginMaxAttempts,
		window: loginFailWindow,
	}
}

// Check 判断这个用户名当前是否处于锁定状态，并返回剩余锁定时间。
//
// 返回的剩余时间要原样写进 Retry-After 响应头：前端能告诉用户「还有 8 分钟」，
// 比只说「请求太频繁」有用得多，也减少用户反复重试带来的额外压力。
func (g *LoginGuard) Check(ctx context.Context, username string) (locked bool, retryAfter time.Duration, err error) {
	key := keys.LoginFailKey(normalizeUsername(username))

	count, err := g.rdb.Get(ctx, key).Int()
	if errors.Is(err, redis.Nil) {
		// 没有这个 key 就是没失败过，正常放行。
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("read login failures: %w", err)
	}
	if count < g.max {
		return false, 0, nil
	}

	// 已经达到阈值。剩余时间直接取 key 的 TTL，锁定与计数共用同一个过期时刻：
	// 锁定结束后计数也一起消失，用户立刻又有完整的几次机会。
	ttl, err := g.rdb.TTL(ctx, key).Result()
	if err != nil {
		return false, 0, fmt.Errorf("read login lock ttl: %w", err)
	}
	if ttl <= 0 {
		// TTL 返回 -1（无过期）或 -2（key 刚被删）时按一个完整窗口算，
		// 宁可多锁一会儿，也不要因为读不到 TTL 就放行。
		ttl = g.window
	}
	return true, ttl, nil
}

// Fail 记一次失败，返回这次是否刚好触发锁定。
//
// 用户名不存在时也要计（调用方在 ErrInvalidCredentials 分支里调用，两种情况共用同一个错误）。
// 只给真实用户名计数等于提供了一个「哪些用户名存在」的探测接口。
func (g *LoginGuard) Fail(ctx context.Context, username string) (bool, time.Duration, error) {
	key := keys.LoginFailKey(normalizeUsername(username))
	seconds := int(g.window.Seconds())

	count, err := g.script.Run(ctx, g.rdb, []string{key}, seconds).Int64()
	if err != nil {
		return false, 0, fmt.Errorf("record login failure: %w", err)
	}
	if count < int64(g.max) {
		return false, 0, nil
	}
	// 第 max 次失败起进入锁定。每次超限的失败都会续不上 TTL（EXPIRE 只在 n==1 时设），
	// 所以锁定是一个固定窗口，不会被持续尝试无限延长。
	return true, g.window, nil
}

// Reset 在登录成功后清掉计数。
// 不清的话，一个人白天输错四次、晚上再错一次就可能被锁，而中间他明明成功登录过。
func (g *LoginGuard) Reset(ctx context.Context, username string) error {
	if err := g.rdb.Del(ctx, keys.LoginFailKey(normalizeUsername(username))).Err(); err != nil {
		return fmt.Errorf("clear login failures: %w", err)
	}
	return nil
}

// normalizeUsername 把用户名归一成 key 的安全形态。
//
// 转小写是必须的：登录查询用的是 MySQL 的默认排序规则（不区分大小写），
// 也就是说 "Alice" 和 "alice" 是同一个账号。如果计数按原样拼 key，
// 攻击者大小写轮换着试就能把 5 次上限变成 2^n 次。
func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}
