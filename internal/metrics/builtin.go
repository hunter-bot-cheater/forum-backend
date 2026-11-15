package metrics

import "github.com/prometheus/client_golang/prometheus"

// 本文件集中定义项目内置指标，并在 init 里一次性注册。
//
// 放在包级变量而不是「用的时候再建」：指标名在进程内必须唯一，
// 而 router.New 在测试里会被调用多次，就地创建第二次就会 panic。
// 业务代码只引用这里的变量，不要自己 new。

var (
	// HTTPRequests 记录请求数。route 用路由模板（/api/articles/:id），
	// 不是真实路径：用模板才能把 /api/articles/1、/2、/3 聚合成一条时间线，
	// 用真实路径会让时序数量随文章数无限增长（Prometheus 最怕的标签发散）。
	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP 请求数，按方法、路由模板与状态码分组",
	}, []string{"method", "route", "status"})

	// HTTPDuration 记录请求耗时（秒）。桶按本项目实测分布选：
	// 5ms~50ms 覆盖缓存命中的正常请求，250ms 以上基本就是慢查询或外部抖动。
	// 分位数在 Prometheus 侧用 histogram_quantile() 从桶里算，客户端只负责把桶填对。
	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP 请求耗时（秒），按方法、路由模板分组",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"method", "route"})

	// HTTPPanics 是 panic 兜底触发的次数。它不是 0 就说明有代码路径在崩，
	// 该去日志里找同 request_id 的 stack。
	HTTPPanics = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "http_panics_total",
		Help: "被 Recovery 兜住的 panic 次数",
	})

	// HTTPRateLimited 是被限流拒绝的请求数（429）。
	// 它突然变大意味着要么有人在刷，要么容量配小了。
	HTTPRateLimited = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "http_rate_limited_total",
		Help: "被限流拒绝的请求数",
	})

	// RateLimitErrors 是限流器自身故障的次数。
	// 限流是 fail-open 的（Redis 挂了就放行），所以它变大时接口仍然能跑，
	// 但保护已经失效，需要尽快修。
	RateLimitErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "rate_limit_errors_total",
		Help: "限流器自身故障（Redis 不可用）的次数",
	})

	// GormSlowQueries 与 GormErrors 是 SQL 层的两个信号。
	// 前者的阈值来自 mysql.slow_query_ms；后者不含 ErrRecordNotFound（那是业务空结果）。
	GormSlowQueries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gorm_slow_queries_total",
		Help: "执行时间超过慢查询阈值的 SQL 次数",
	})
	GormErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gorm_query_errors_total",
		Help: "执行失败的 SQL 次数（不含记录不存在）",
	})

	// ArticleCacheEvents 把缓存路径的每一步拆成事件：
	// hit / empty_hit / miss / load / share / degrade / write。
	// 有了它就能算命中率 = hit / (hit + miss)，也能单独看降级次数。
	ArticleCacheEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "article_cache_events_total",
		Help: "文章缓存事件计数，event 取 hit/empty_hit/miss/load/share/degrade/write",
	}, []string{"event"})
)

// init 注册全部内置指标。
// 用 MustRegister：重名或类型冲突属于编码错误，启动时就该炸，
// 而不是等到抓取时输出重复序列、让 Prometheus 拒绝整次采集。
func init() {
	registry.MustRegister(
		HTTPRequests,
		HTTPDuration,
		HTTPPanics,
		HTTPRateLimited,
		RateLimitErrors,
		GormSlowQueries,
		GormErrors,
		ArticleCacheEvents,
	)
}
