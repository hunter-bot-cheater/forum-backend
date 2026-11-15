package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/metrics"
)

// AccessLog 替换 gin 自带的 Logger，用 slog 输出结构化的访问日志。
//
// 为什么不用 gin.Logger：它打的是带颜色和方括号的文本，字段位置固定，
// 想按 status、latency 筛就得写正则；换成 JSON 后每个字段都能直接查。
//
// 同时把两个指标记下来：请求数（按方法、路由模板、状态码分组）与耗时直方图。
// 日志用于看单次请求，指标用于看趋势和分位数，两者不能互相替代。
//
// 必须装在 RequestID 之后（才能带上 request_id）、Recovery 之前
// （这样 panic 被兜成 500 之后，这里记录到的状态码和耗时都是真实的）。
//
// skipPaths 里的路径不记日志也不记指标：探活与指标抓取每隔几秒一次，
// 一天几万条 ok 会把真正的业务日志冲掉，也会污染耗时分位数。
func AccessLog(skipPaths ...string) gin.HandlerFunc {
	skip := make(map[string]bool, len(skipPaths))
	for _, path := range skipPaths {
		skip[path] = true
	}

	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		// 探测接口只看状态码，不需要留痕；放在 c.Next() 之后判断，
		// 是为了仍然走完整个链路（否则探活就成了只看中间件、不进 handler）。
		if skip[c.Request.URL.Path] {
			return
		}

		status := c.Writer.Status()
		// 耗时用毫秒：和 GORM 慢查询日志的 elapsed_ms 统一单位，对照时不用换算。
		latency := float64(time.Since(start).Microseconds()) / 1000

		// route 是注册时的路由模板（/api/articles/:id），path 是真实请求路径。
		// 按 route 聚合能看出哪个接口慢，按 path 能看到具体是谁打的；
		// 404 时 route 为空，指标里统一记成 unmatched，否则时序数量会随乱打路径增长。
		route := metricRoute(c.FullPath())
		metrics.HTTPRequests.WithLabelValues(c.Request.Method, route, strconv.Itoa(status)).Inc()
		// 直方图的单位是秒，这里顺手换算；日志里仍用毫秒，人读更直观。
		metrics.HTTPDuration.WithLabelValues(c.Request.Method, route).Observe(latency / 1000)

		attrs := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"latency_ms", latency,
			"size", writtenSize(c),
			"client_ip", c.ClientIP(),
		}
		if route != "" {
			attrs = append(attrs, "route", route)
		}

		// 用请求自带的 ctx 打日志，contextHandler 会自动补上 request_id。
		ctx := c.Request.Context()
		switch {
		case status >= http.StatusInternalServerError:
			slog.ErrorContext(ctx, "http request", attrs...)
		case status >= http.StatusBadRequest:
			slog.WarnContext(ctx, "http request", attrs...)
		default:
			slog.InfoContext(ctx, "http request", attrs...)
		}
	}
}

// writtenSize 返回响应体字节数。
// gin 在没写任何内容时返回 -1，直接打到日志里会让统计出现负数，这里归零。
func writtenSize(c *gin.Context) int {
	size := c.Writer.Size()
	if size < 0 {
		return 0
	}
	return size
}

// metricRoute 把「没匹配到路由」的请求归到一个固定标签值上。
// 直接用空字符串会让输出变成 route=""，多数看板与告警规则会漏掉这一类，
// 而它恰好是「有人在扫接口」的早期信号。
func metricRoute(route string) string {
	if route == "" {
		return "unmatched"
	}
	return route
}
