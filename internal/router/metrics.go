package router

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/config"
	"forum-backend/internal/metrics"
)

// registerMetrics 挂上 GET /metrics。
//
// 放在根路径而不是 /api 下，有两个原因：
//   - 抓取方（Prometheus、各种看板）默认就按 /metrics 找；
//   - /api 组上有限流与 body 限制，抓取不该占用业务接口的令牌桶。
//
// 关闭时直接不注册路由，请求会走统一的 404，而不是返回空指标——
// 后者会让「没开启采集」和「采集到了 0」两种情况混在一起。
func registerMetrics(r *gin.Engine, cfg config.MetricsConfig) {
	if !cfg.Enabled {
		return
	}

	// promhttp 的 handler 只创建一次：它内部持有抓取用的缓冲池，
	// 每次请求新建会白白丢掉复用。
	// 这里也不手动设置 Content-Type——promhttp 会按 Accept 头协商格式
	// （文本 0.0.4 或 OpenMetrics），自己写反而会覆盖掉协商结果。
	handler := metrics.Handler()

	r.GET("/metrics", func(c *gin.Context) {
		// 先在写响应头之前校验 token：一旦 promhttp 写出 200，就不能再改成 401 了。
		if !metricsTokenOK(c, cfg.Token) {
			c.Header("WWW-Authenticate", `Bearer realm="metrics"`)
			c.String(http.StatusUnauthorized, "unauthorized")
			return
		}

		handler.ServeHTTP(c.Writer, c.Request)
	})
}

// metricsTokenOK 校验抓取方带来的 Bearer token。
// token 为空表示不校验（内网抓取，或由 Nginx 的 allow/deny 限制来源）。
//
// 比较用 subtle.ConstantTimeCompare 而不是 ==：
// 字符串比较在第一个不同字节就返回，攻击者能通过响应时间逐字节猜出 token。
func metricsTokenOK(c *gin.Context, token string) bool {
	if token == "" {
		return true
	}

	header := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := []byte(strings.TrimPrefix(header, prefix))
	return subtle.ConstantTimeCompare(got, []byte(token)) == 1
}
