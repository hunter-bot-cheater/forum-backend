// Package metrics 用 prometheus/client_golang 采集指标，并暴露抓取用的 handler。
//
// 用法：指标在 builtin.go 里统一定义并注册，业务代码 import 本包后
// 调 WithLabelValues(...).Inc() / .Observe() 打点，不要自己 new 指标。
//
// 为什么用官方库而不是自己拼文本格式：版本协商（0.0.4 与 OpenMetrics）、
// 标签值转义、gzip 压缩、+Inf 桶、Content-Type 里的 escaping 参数，
// 这些都是规范细节，自己实现很容易在某一条上出错，而错了的表现是
// 「抓取端静默丢样本」——最难查的一类问题。
package metrics

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

// registry 是本进程唯一的指标注册表。
//
// 不用 prometheus.DefaultRegisterer：它是全局变量，测试之间会互相污染
// （同一个指标注册第二次直接 panic），显式持有才能让测试各自拿一份干净的。
var registry = prometheus.NewRegistry()

// Handler 返回 /metrics 的抓取 handler。
//
// 交给 promhttp 而不是自己拼响应头和正文：它负责 gzip（抓取方默认带
// Accept-Encoding: gzip）、格式协商，以及并发抓取时的加锁。
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		// 默认行为：采集器出错时返回 500。不要改成 ContinueOnError——
		// 半截指标看起来像正常数据，缺的序列会沿用「上一次抓到的值」，
		// 反而掩盖故障；500 能让 Prometheus 直接把这次抓取标记为失败。
		ErrorHandling: promhttp.HTTPErrorOnError,
		// promhttp 的错误出口是它自己的 Logger 接口，默认写标准库 log
		// （纯文本、无字段）。转给 slog，日志才会和其他模块保持同一种 JSON 格式。
		ErrorLog: slogErrorPrinter{},
	})
}

// slogErrorPrinter 把 promhttp 的错误输出转成一条 slog 记录。
type slogErrorPrinter struct{}

// Println 实现 promhttp.Logger。库只认这一个方法，参数是 fmt.Println 那种形式。
func (slogErrorPrinter) Println(v ...any) {
	slog.Error("metrics scrape failed", "detail", fmt.Sprint(v...))
}

// BindGauge 登记一个「抓取时求值」的 gauge，同名重复调用只替换取值函数。
//
// 典型用途：在线连接数（Hub 里现成的计数）、进程已运行时长。
// 不做成每次变化 Set，是因为这些值由别的模块持有，在热路径上埋钩子
// 得不偿失，而抓取频率通常只有十几秒一次。
func BindGauge(name, help string, fn func() float64) {
	bindGauge(registry, name, help, fn)
}

// bindGauge 是 BindGauge 的实现，注册表作为参数传进来，方便测试隔离。
func bindGauge(reg prometheus.Registerer, name, help string, fn func() float64) {
	gauge := &swapGauge{desc: prometheus.NewDesc(name, help, nil, nil), fn: fn}

	if err := reg.Register(gauge); err != nil {
		var already prometheus.AlreadyRegisteredError
		// 同名已注册是正常情况：router.New 在测试里会被调多次，每次都要绑定
		// 一批 gauge。只要原来那个也是 swapGauge，就只换函数。
		if errors.As(err, &already) {
			existing, ok := already.ExistingCollector.(*swapGauge)
			if !ok {
				panic("metrics: 指标 " + name + " 已被别的类型占用，不能再当 gauge 绑定")
			}
			existing.setFn(fn)
			return
		}
		panic("metrics: 注册 gauge " + name + " 失败: " + err.Error())
	}
}

// swapGauge 是取值函数可替换的 gauge。
//
// 为什么不用 prometheus.NewGaugeFunc：GaugeFunc 一旦注册，函数就固定了，
// 而装配逻辑（router.New）会被重复执行，固定的函数意味着第二次只能 panic。
type swapGauge struct {
	desc *prometheus.Desc

	mu sync.RWMutex
	fn func() float64
}

// Describe 声明自己产出的描述符，注册表靠它检测重名与类型冲突。
func (g *swapGauge) Describe(ch chan<- *prometheus.Desc) {
	ch <- g.desc
}

// Collect 在抓取时现算一次值。
// 锁只保护函数指针，取值本身在锁外执行，避免某个慢函数把其他抓取请求堵住。
func (g *swapGauge) Collect(ch chan<- prometheus.Metric) {
	g.mu.RLock()
	fn := g.fn
	g.mu.RUnlock()

	ch <- prometheus.MustNewConstMetric(g.desc, prometheus.GaugeValue, fn())
}

// setFn 替换取值函数，供重复绑定使用。
func (g *swapGauge) setFn(fn func() float64) {
	g.mu.Lock()
	g.fn = fn
	g.mu.Unlock()
}

// CounterValue 读取带标签计数器的当前值，给调试快照和测试断言用。
//
// CounterVec 没有提供「按标签读数值」的方法，只能先取到子指标，
// 再让它把自己序列化成 dto.Metric。这条路径有两次分配，热路径上不要调用。
func CounterValue(vec *prometheus.CounterVec, labelValues ...string) float64 {
	counter, err := vec.GetMetricWithLabelValues(labelValues...)
	// 标签个数不对属于编码错误，运行时不该走到；真走到了返回 0，
	// 让调试快照少一个数字，而不是让整个接口失败。
	if err != nil {
		return 0
	}

	m := &dto.Metric{}
	if err := counter.Write(m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

// init 登记库自带的两个采集器。
//
// 它们覆盖了原本要手写的 runtime.ReadMemStats 那一堆：goroutine 数、堆内存、
// GC 次数、CPU 时间、文件描述符数、进程启动时刻等。指标名和社区看板对齐，
// 导入现成面板就能用，不用自己编名字再写一遍查询。
func init() {
	registry.MustRegister(collectors.NewGoCollector())
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// 进程采集器只给启动时刻（精确到秒），uptime 需要自己减一次。
	startedAt := time.Now()
	BindGauge("process_uptime_seconds", "进程已运行秒数", func() float64 {
		return time.Since(startedAt).Seconds()
	})
}
