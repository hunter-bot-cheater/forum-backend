// Package service 的文章详情缓存配置与工具函数。
// 本文件只放常量、纯函数和计数器。
package service

import (
	"math/rand/v2"
	"strconv"
	"sync/atomic"
	"time"
)

//一、key

// articleDetailKeyPrefix 是详情缓存 key 的前缀。
// 抽成常量是为了让「读缓存」「删缓存」「redis-cli 排查」三处用同一个名字，
const articleDetailKeyPrefix = "article:detail:"

// articleDetailCacheVersion 是缓存值的格式版本。
// 以后给 model.Article 加字段、改 JSON tag 时，把 v1 改成 v2：
// 新代码只读 v2 的 key，老 key 因为没人再读会自然过期，
// 不需要半夜跑脚本批量清理，也不会把旧格式的数据解析成新结构体。
const articleDetailCacheVersion = "v1"

// articleDetailKey 拼出一篇详情的缓存 key，形如 article:detail:v1:42。
// 用 strconv.FormatUint 而不是 fmt.Sprintf：这里只需要十进制转换，
// strconv 不做格式解析，意图更明确。
func articleDetailKey(id uint) string {
	return articleDetailKeyPrefix + articleDetailCacheVersion + ":" + strconv.FormatUint(uint64(id), 10)
}

// 二、TTL

// 详情缓存的基础存活时间。
// 决定「数据库改了但删缓存失败」时，旧数据最多能被读多久。
const articleDetailTTLBase = 10 * time.Minute

// articleDetailTTLJitter 是叠加在基础 TTL 上的随机抖动上限。
// 最终 TTL 落在 [10m, 10m+2m)，把一批写入的过期时刻摊到 2 分钟窗口。
const articleDetailTTLJitter = 2 * time.Minute

// articleEmptyTTL 是「查无此文」空标记的存活时间。
// 空标记挡的是不存在的 id，
// 留太久会让后来真写入这条记录时短暂查不到。
const articleEmptyTTL = 60 * time.Second

// articleDetailTTL 返回本次回填应该用的 TTL。
// 每次调用都重新取随机数，所以同一批写入的 key 不会撞在同一秒过期。
func articleDetailTTL() time.Duration {
	// Int64N 返回 [0, n) 区间里的整数，转成 time.Duration 后加到基础值上。
	jitter := rand.Int64N(int64(articleDetailTTLJitter))
	return articleDetailTTLBase + time.Duration(jitter)
}

// 三、空标记
// articleEmptySentinel 是「数据库里确实没有这篇文章」的缓存值。
// 合法的详情缓存一定是 json.Marshal(model.Article) 的结果，以 '{' 开头；
// 这个字符串以下划线开头，永远不可能和真实文章 JSON 混淆。
// 绝对不能用 "null"：json.Unmarshal([]byte("null"), &a) 不报错，
// 会把 a 留成全零结构体，客户端会收到一篇 id=0 的假文章。
const articleEmptySentinel = "__ARTICLE_NOT_FOUND__"

//四、计数器

// cacheMetrics 用原子计数器记录缓存路径上发生了什么。
// 字段必须用 atomic.Int64：Detail 会被多个请求并发调用，
// 普通 int64 的 ++ 不是原子操作，go test -race 会直接报数据竞争。
type cacheMetrics struct {
	Hits      atomic.Int64 // 命中真实文章 JSON 的次数
	EmptyHits atomic.Int64 // 命中空标记、直接判定不存在的次数
	Misses    atomic.Int64 // 未命中（含 Redis 故障降级）的次数
	Loads     atomic.Int64 // 真正回源 MySQL 的次数，验证击穿时看这个
	Shares    atomic.Int64 // 被 singleflight 合并掉的请求数
	Degrades  atomic.Int64 // Redis 读写出错、走降级的次数
	Writes    atomic.Int64 // 成功回填缓存的次数
}

// CacheStats 是缓存计数器的只读快照，供调试接口和测试读取。
// 故意用普通 int64：调用方拿到的是一份拷贝，读的时候不会看到半成品，
// 也不用知道 atomic 怎么用。
// json tag 写成小写下划线，是因为 6.7 的可选调试接口会直接把它序列化出去，
// Go 默认导出的字段名是 Hits、EmptyHits 这种大写形式，不适合当 API 字段。
type CacheStats struct {
	Hits      int64 `json:"hits"`
	EmptyHits int64 `json:"empty_hits"`
	Misses    int64 `json:"misses"`
	Loads     int64 `json:"loads"`
	Shares    int64 `json:"shares"`
	Degrades  int64 `json:"degrades"`
	Writes    int64 `json:"writes"`
}

// snapshot 把原子计数器读成一份普通结构体。
// 每个字段单独 Load，严格说这不是同一个瞬间的快照；
// 对学习项目里看趋势、做验收断言够用，要精确一致得另加锁。
func (m *cacheMetrics) snapshot() CacheStats {
	var s CacheStats
	s.Hits = m.Hits.Load()
	s.EmptyHits = m.EmptyHits.Load()
	s.Misses = m.Misses.Load()
	s.Loads = m.Loads.Load()
	s.Shares = m.Shares.Load()
	s.Degrades = m.Degrades.Load()
	s.Writes = m.Writes.Load()
	return s
}
