package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// gatherFamily 抓一次指标，取出指定名字的指标族；找不到直接失败。
// 断言基于 Gather 的结果而不是解析文本，测的是「注册进去的指标对不对」，
// 文本格式本身由官方库保证，不需要我们再验一遍。
func gatherFamily(t *testing.T, reg *prometheus.Registry, name string) *dto.MetricFamily {
	t.Helper()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("抓取结果里没有指标 %s", name)
	return nil
}

// hasLabels 判断一条样本是否带有给定的全部标签。
// 只比给定的那些：官方库会自动补 __name__ 之外的字段，逐字段对等太脆。
func hasLabels(m *dto.Metric, want map[string]string) bool {
	if len(m.GetLabel()) != len(want) {
		return false
	}
	for _, pair := range m.GetLabel() {
		if want[pair.GetName()] != pair.GetValue() {
			return false
		}
	}
	return true
}

// TestBuiltinCollectorsRegistered 验证 init 里登记的运行时采集器真的产出了指标。
// 这类指标是被导入的，一旦漏注册，看板上会整片空白，所以要守住。
func TestBuiltinCollectorsRegistered(t *testing.T) {
	for _, name := range []string{"go_goroutines", "go_memstats_alloc_bytes", "process_uptime_seconds"} {
		if family := gatherFamily(t, registry, name); family.GetType() != dto.MetricType_GAUGE {
			t.Errorf("%s 应当是 gauge，实际 %v", name, family.GetType())
		}
	}
}

// TestHTTPRequestsLabels 验证请求计数按 method/route/status 三个标签分组，
// 且相同的标签组合累加到同一条样本上。
func TestHTTPRequestsLabels(t *testing.T) {
	// 用测试专属的 route 值，避免和别的用例累计到同一条样本上。
	route := "/test/metrics-labels"
	HTTPRequests.WithLabelValues("GET", route, "200").Inc()
	HTTPRequests.WithLabelValues("GET", route, "200").Inc()
	HTTPRequests.WithLabelValues("POST", route, "500").Inc()

	family := gatherFamily(t, registry, "http_requests_total")
	if family.GetType() != dto.MetricType_COUNTER {
		t.Fatalf("类型应当是 counter，实际 %v", family.GetType())
	}

	got := map[string]float64{}
	for _, m := range family.GetMetric() {
		for _, pair := range m.GetLabel() {
			if pair.GetName() != "status" || !hasLabels(m, map[string]string{
				"method": "GET", "route": route, "status": pair.GetValue(),
			}) {
				continue
			}
			got[pair.GetValue()] = m.GetCounter().GetValue()
		}
	}
	// 同一标签组合 Inc 两次必须合并成一条 2。
	if _, ok := got["200"]; !ok || got["200"] < 2 {
		t.Errorf("GET %s 200 的计数不对: %v", route, got)
	}
}

// TestHTTPDurationBuckets 验证桶是累积的（le 语义），_sum 与 _count 对得上。
// 桶填错的表现是 histogram_quantile 算出离谱的 p99，比报错更难发现。
func TestHTTPDurationBuckets(t *testing.T) {
	route := "/test/metrics-buckets"
	HTTPDuration.WithLabelValues("GET", route).Observe(0.05) // 落 le=0.05
	HTTPDuration.WithLabelValues("GET", route).Observe(0.3)  // 落 le=0.5
	HTTPDuration.WithLabelValues("GET", route).Observe(2.0)  // 超出 1，只算进 2.5 与 +Inf

	family := gatherFamily(t, registry, "http_request_duration_seconds")
	if family.GetType() != dto.MetricType_HISTOGRAM {
		t.Fatalf("类型应当是 histogram，实际 %v", family.GetType())
	}

	var target *dto.Histogram
	for _, m := range family.GetMetric() {
		if hasLabels(m, map[string]string{"method": "GET", "route": route}) {
			target = m.GetHistogram()
		}
	}
	if target == nil {
		t.Fatalf("找不到 GET %s 的直方图", route)
	}

	// 预期每个上界的累积计数：0.05 各 1 次，0.5 累积 2 次，2.5 与 +Inf 累积 3 次。
	want := map[float64]uint64{0.05: 1, 0.5: 2, 1: 2, 2.5: 3}
	for _, bucket := range target.GetBucket() {
		upper := bucket.GetUpperBound()
		if expected, ok := want[upper]; ok && bucket.GetCumulativeCount() != expected {
			t.Errorf("le=%v 的累积计数 = %d，期望 %d", upper, bucket.GetCumulativeCount(), expected)
		}
	}
	if target.GetSampleCount() != 3 {
		t.Errorf("_count = %d，期望 3", target.GetSampleCount())
	}
	if diff := target.GetSampleSum() - 2.35; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("_sum = %v，期望 2.35", target.GetSampleSum())
	}
}

// TestBindGaugeEvaluatedAtScrape 验证 gauge 的值是在抓取时现算的，不是注册时算一次。
func TestBindGaugeEvaluatedAtScrape(t *testing.T) {
	reg := prometheus.NewRegistry()
	value := 1.0
	bindGauge(reg, "demo_gauge", "示例 gauge", func() float64 { return value })

	if got := gaugeValue(t, reg, "demo_gauge"); got != 1 {
		t.Fatalf("第一次抓取 = %v，期望 1", got)
	}

	// 不重新注册，只改被闭包捕获的变量，第二次抓取应当是新值。
	value = 42
	if got := gaugeValue(t, reg, "demo_gauge"); got != 42 {
		t.Fatalf("第二次抓取 = %v，期望 42", got)
	}
}

// TestBindGaugeReplacesFunction 验证同名重复绑定不 panic、只换函数。
// router.New 在测试里会被调多次，这条是它的前提。
func TestBindGaugeReplacesFunction(t *testing.T) {
	reg := prometheus.NewRegistry()
	bindGauge(reg, "swap_gauge", "示例 gauge", func() float64 { return 1 })
	bindGauge(reg, "swap_gauge", "示例 gauge", func() float64 { return 2 })

	if got := gaugeValue(t, reg, "swap_gauge"); got != 2 {
		t.Fatalf("应当使用后绑定的函数，实际 %v", got)
	}
	// 重复绑定不能变成两条样本，否则 Prometheus 会判定重复序列。
	family := gatherFamily(t, reg, "swap_gauge")
	if len(family.GetMetric()) != 1 {
		t.Errorf("同名 gauge 只应有 1 条样本，实际 %d", len(family.GetMetric()))
	}
}

// TestBindGaugeTypeConflict 验证同名指标已被别的类型占用时 panic。
// 同一个指标名有两种类型会让整次抓取失败，必须在装配期就炸。
func TestBindGaugeTypeConflict(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{
		Name: "conflict_total",
		Help: "占位",
	}))

	defer func() {
		if recover() == nil {
			t.Fatal("类型冲突应当 panic")
		}
	}()
	bindGauge(reg, "conflict_total", "示例", func() float64 { return 1 })
}

// TestCounterValue 验证按标签读回计数器（CacheStats 的调试快照靠它）。
func TestCounterValue(t *testing.T) {
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "demo_events_total",
		Help: "示例事件",
	}, []string{"event"})

	vec.WithLabelValues("hit").Inc()
	vec.WithLabelValues("hit").Inc()
	vec.WithLabelValues("miss").Inc()

	if got := CounterValue(vec, "hit"); got != 2 {
		t.Errorf("hit = %v，期望 2", got)
	}
	// 没出现过的标签组合按 0 返回，不能报错让调用方崩。
	if got := CounterValue(vec, "never"); got != 0 {
		t.Errorf("未出现的标签应当返回 0，实际 %v", got)
	}
}

// gaugeValue 抓取指定名字的 gauge 并返回数值。
func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()

	family := gatherFamily(t, reg, name)
	metrics := family.GetMetric()
	if len(metrics) == 0 {
		t.Fatalf("%s 没有任何样本", name)
	}
	return metrics[0].GetGauge().GetValue()
}
