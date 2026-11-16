// 本文件是文章详情缓存的配置与工具部分：key 怎么拼、TTL 取多少、空标记是什么、计数器怎么记。
// 纯常量与纯函数放这里，和业务逻辑分开，改缓存策略时不用翻 service.go。
package article

import (
	"strconv"
	"time"

	"forum-backend/internal/cache"
	"forum-backend/internal/metrics"
)

// ===== 缓存 key =====

// articleDetailKeyPrefix 是详情缓存 key 的前缀。
// 抽成常量是为了让「读缓存」「删缓存」「redis-cli 排查」三处用同一个名字，
// 手写字符串迟早会出现少一个冒号、拼错单词之类的低级错误。
const articleDetailKeyPrefix = "article:detail:"

// articleDetailCacheVersion 是缓存值的格式版本。
// 以后给 model.Article 加字段、改 JSON tag 时，把 v1 改成 v2：
// 新代码只读 v2 的 key，老 key 因为没人再读会自然过期，
// 不需要半夜跑脚本批量清理，也不会把旧格式的数据解析成新结构体。
//
// 已经升过两次：v1 → v2 对应 model.Article 新增 view_count，v2 → v3 对应新增 tags。
// 不升的话，旧缓存里没有这个字段，反序列化出来是空值，
// 详情页会先显示「没有标签」/「0 次浏览」，等缓存过期才跳到真值。
const articleDetailCacheVersion = "v3"

// articleDetailKey 拼出一篇详情的缓存 key，形如 article:detail:v1:42。
// 用 strconv.FormatUint 而不是 fmt.Sprintf：这里只做十进制转换，
// strconv 不解析格式串，意图更明确也更快。
func articleDetailKey(id uint) string {
	return articleDetailKeyPrefix + articleDetailCacheVersion + ":" + strconv.FormatUint(uint64(id), 10)
}

// ===== 缓存 TTL =====

// 详情缓存的基础存活时间。
// 它决定「数据库改了但删缓存失败」时，旧数据最多还能被读多久。
const articleDetailTTLBase = 10 * time.Minute

// articleDetailTTLJitter 是叠加在基础 TTL 上的随机抖动上限。
// 最终 TTL 落在 [10m, 10m+2m)，把一批同时写入的 key 的过期时刻摊到 2 分钟窗口里，
// 避免它们在同一个瞬间集体失效、把流量一起打到数据库（缓存雪崩）。
const articleDetailTTLJitter = 2 * time.Minute

// articleEmptyTTL 是「查无此文」空标记的存活时间。
// 空标记挡的是反复查询不存在 id 的请求（缓存穿透），所以不能没有；
// 但也留不久：留太久会让这条记录后来真被写入时，仍有一段时间查不到。
const articleEmptyTTL = 60 * time.Second

// viewDedupTTL 是同一个访问者重复看同一篇文章的去重窗口。
//
// 30 分钟是「一次阅读会话」的量级：读者点进来、刷新几次、又返回来看，
// 都算同一次浏览；过半小时再回来才算新的。窗口太短等于浏览数随刷新次数涨，
// 太长则一篇文章在一个人眼里永远只加一，热榜看不出热度变化。
// 这个值只影响去重标记的存活时间，不影响 view_count 本身。
const viewDedupTTL = 30 * time.Minute

// articleDetailTTL 返回本次回填应该用的 TTL。
// 每次调用都重新取随机数，所以同一批写入的 key 不会撞在同一秒过期。
// 抖动逻辑在 cache 包，关注数缓存用的是同一个函数，两处不会慢慢分叉。
func articleDetailTTL() time.Duration {
	return cache.JitterTTL(articleDetailTTLBase, articleDetailTTLJitter)
}

// ===== 空标记 =====

// articleEmptySentinel 是「数据库里确实没有这篇文章」的缓存值。
// 合法的详情缓存一定是 json.Marshal(model.Article) 的结果，以 '{' 开头；
// 这个字符串以下划线开头，永远不可能和真实文章 JSON 混淆。
// 绝对不能用 "null"：json.Unmarshal([]byte("null"), &a) 不报错，
// 会把 a 留成全零结构体，客户端会收到一篇 id=0 的假文章。
const articleEmptySentinel = "__ARTICLE_NOT_FOUND__"

// ===== 计数器 =====

// 缓存事件统一记到 metrics.ArticleCacheEvents 上，标签 event 的取值见下面这组常量。
//
// 为什么不再各自维护一份原子计数器：这些数字需要同时出现在两处——
// /metrics 给监控抓取，CacheStats() 给本地验收。两套计数会慢慢对不上，
// 所以只记一份，验收时用 metrics.CounterValue 从指标里读回来。
//
// 为什么打标签而不是七个独立计数器：算命中率要 hit 与 miss 相除，
// 带 event 标签后一条 PromQL 就能算（sum by (event)），也不用给每个事件起名字。
const (
	cacheEventHit      = "hit"       // 命中真实文章 JSON
	cacheEventEmptyHit = "empty_hit" // 命中空标记，直接判定不存在
	cacheEventMiss     = "miss"      // 未命中（含 Redis 故障降级）
	cacheEventLoad     = "load"      // 真正回源 MySQL，验证击穿时看这个
	cacheEventShare    = "share"     // 被 singleflight 合并掉的请求数
	cacheEventDegrade  = "degrade"   // Redis 读写出错、走降级的次数
	cacheEventWrite    = "write"     // 成功回填缓存的次数
)

// CacheStats 是缓存计数器的只读快照，供本地验收和调试接口读取。
//
// 注意口径：计数器是全进程共享的（/metrics 只有一份），不按 ArticleService 实例区分。
// 生产上没有区别——一个进程就一个实例；测试里实例会更多，读到的是累计值。
// json tag 写成小写下划线，是因为调试接口会直接把它序列化出去。
type CacheStats struct {
	Hits      int64 `json:"hits"`
	EmptyHits int64 `json:"empty_hits"`
	Misses    int64 `json:"misses"`
	Loads     int64 `json:"loads"`
	Shares    int64 `json:"shares"`
	Degrades  int64 `json:"degrades"`
	Writes    int64 `json:"writes"`
}

// snapshot 从指标里读出当前计数。
// 每个值单独读，严格说不是同一瞬间的快照；对看趋势、做验收断言够用。
func snapshot() CacheStats {
	return CacheStats{
		Hits:      int64(metrics.CounterValue(metrics.ArticleCacheEvents, cacheEventHit)),
		EmptyHits: int64(metrics.CounterValue(metrics.ArticleCacheEvents, cacheEventEmptyHit)),
		Misses:    int64(metrics.CounterValue(metrics.ArticleCacheEvents, cacheEventMiss)),
		Loads:     int64(metrics.CounterValue(metrics.ArticleCacheEvents, cacheEventLoad)),
		Shares:    int64(metrics.CounterValue(metrics.ArticleCacheEvents, cacheEventShare)),
		Degrades:  int64(metrics.CounterValue(metrics.ArticleCacheEvents, cacheEventDegrade)),
		Writes:    int64(metrics.CounterValue(metrics.ArticleCacheEvents, cacheEventWrite)),
	}
}
