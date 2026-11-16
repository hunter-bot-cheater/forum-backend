// 本文件是关注数缓存的配置与读写：key 怎么拼、TTL 多久、命中与失效怎么记。
// 和 service.go 分开，是因为这里没有业务规则，只有「怎么存取」。
package follow

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"forum-backend/internal/cache"
	"forum-backend/internal/metrics"
)

// 关注数缓存的 key 前缀与版本号。
// 带 v1 是为了以后结构变了能直接换版本：新代码只读新 key，
// 旧 key 没人再读会自然过期，不用写脚本去清线上残留。
const followStatsKeyPrefix = "user:follow:stats:v1:"

// followStatsTTLBase 是关注数在缓存里的基础存活时间。
//
// 它只是兜底，不是主要手段：正常情况下关注/取关会把 key 删掉，
// 下一次读就是重新数出来的值。之所以还留 TTL，是因为删除可能失败
// （Redis 抖动、进程在写库和删缓存之间挂掉），没有 TTL 就会错一次永久错。
//
// 取 5 分钟的理由：主页是被反复刷的页面，5 分钟足够把重复的 COUNT 挡掉；
// 而万一失效漏了，粉丝数最多旧五分钟，对一个论坛来说可以接受。
const followStatsTTLBase = 5 * time.Minute

// followStatsTTLJitter 是叠加在基础 TTL 上的随机抖动上限。
//
// 不加抖动会出现「每 5 分钟一次的全表回源脉冲」：
// 关注数的 key 是「有人访问才回填」的，Redis 重启或清库之后，
// 第一波访问会把一批 key 在几秒内集中写进去，这批 key 的过期时刻因此几乎相同；
// 5 分钟后它们同时消失，第二波访问同时回源，两个 COUNT 一起压到 MySQL。
// 之后每 5 分钟重复一次，形成周期性的尖峰，而不是均匀的请求。
//
// 抖动把过期时刻摊到一个 1 分钟宽度的窗口里，把尖峰摊平。
// 和 articleDetailTTL 用的是同一套理由，取值不同只是因为这里的 TTL 更短。
const followStatsTTLJitter = time.Minute

// followStatsTTL 返回本次回填应该用的 TTL。
// 每次调用都重新取随机数，所以同一批写入的 key 不会撞在同一秒过期。
// 抖动逻辑放在 cache 包，因为后面私信、通知、用户简讯的缓存都要用同一套，
// 各写一份迟早会出现某个模块漏了抖动。
func followStatsTTL() time.Duration {
	return cache.JitterTTL(followStatsTTLBase, followStatsTTLJitter)
}

// 缓存事件名，对应 metrics.FollowStatsCacheEvents 的 event 标签。
const (
	cacheEventHit        = "hit"
	cacheEventMiss       = "miss"
	cacheEventDegrade    = "degrade"
	cacheEventInvalidate = "invalidate"
	// cacheEventShare 记「被 singleflight 合并掉的请求数」。
	// 这个数字不为零说明单飞真的在起作用：那些请求本来会各自打一遍两个 COUNT。
	cacheEventShare = "share"
)

// statsSnapshot 是缓存的 JSON 形状。
//
// 两个计数放同一个 key 而不是两个 key：它们总是一起被读、一起失效，
// 拆开会让读变成两次往返，还会出现「关注数已更新、粉丝数还是旧值」的中间态。
type statsSnapshot struct {
	Following int64 `json:"following"`
	Followers int64 `json:"followers"`
}

// followStatsKey 拼出某个用户的关注数缓存 key，形如 user:follow:stats:v1:42。
func followStatsKey(userID uint) string {
	return followStatsKeyPrefix + strconv.FormatUint(uint64(userID), 10)
}

// loadStats 读缓存，返回快照、是否命中、以及 Redis 是否出了故障。
//
// 第三个返回值单独给出，是为了让调用方在 Redis 已经挂掉时不要再尝试回填：
// 否则一次主页请求会先记一条读失败、再记一条写失败，故障期间日志翻倍。
//
// 值损坏不算「Redis 故障」：Redis 是通的，只是数据格式不对，
// 回源之后必须回填把坏值覆盖掉，否则它会在 TTL 到期前一直挡着缓存。
func (s *Service) loadStats(ctx context.Context, userID uint) (snap statsSnapshot, hit bool, cacheBroken bool) {
	found, err := cache.LoadJSON(ctx, s.rdb, followStatsKey(userID), &snap)
	if err != nil {
		metrics.FollowStatsCacheEvents.WithLabelValues(cacheEventDegrade).Inc()
		slog.WarnContext(ctx, "read follow stats cache", "user_id", userID, "error", err)
		return statsSnapshot{}, false, !errors.Is(err, cache.ErrBadValue)
	}
	if found {
		metrics.FollowStatsCacheEvents.WithLabelValues(cacheEventHit).Inc()
		return snap, true, false
	}
	metrics.FollowStatsCacheEvents.WithLabelValues(cacheEventMiss).Inc()
	return statsSnapshot{}, false, false
}

// storeStats 回源之后回填缓存。
// 写失败只记日志：缓存没写进去不影响本次结果，下次读会再回源一次。
func (s *Service) storeStats(ctx context.Context, userID uint, snap statsSnapshot) {
	// 每次回填都重新算 TTL，抖动在这里生效，不能在包级算一次就复用。
	if err := cache.StoreJSON(ctx, s.rdb, followStatsKey(userID), snap, followStatsTTL()); err != nil {
		metrics.FollowStatsCacheEvents.WithLabelValues(cacheEventDegrade).Inc()
		slog.WarnContext(ctx, "write follow stats cache", "user_id", userID, "error", err)
	}
}

// invalidateStats 在关系变化后删掉相关用户的计数缓存。
//
// 一次关注会同时改变两个人：关注者的「关注数」和被关注者的「粉丝数」，
// 所以两边的 key 都要删。少删一个的表现是「刚关注完，主页上的粉丝数没变」。
//
// 一次 DEL 传所有 key：Redis 单线程执行，多 key 版本只有一次往返，
// 也不会出现删了一半的中间态。
func (s *Service) invalidateStats(ctx context.Context, userIDs ...uint) {
	keys := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		keys = append(keys, followStatsKey(id))
	}
	if err := cache.Delete(ctx, s.rdb, keys...); err != nil {
		// 删不掉只剩 TTL 兜底，此时计数可能旧五分钟，必须留下痕迹。
		metrics.FollowStatsCacheEvents.WithLabelValues(cacheEventDegrade).Inc()
		slog.WarnContext(ctx, "invalidate follow stats cache", "user_ids", userIDs, "error", err)
		return
	}
	metrics.FollowStatsCacheEvents.WithLabelValues(cacheEventInvalidate).Inc()
}
