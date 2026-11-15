package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/config"
)

// newMetricsEngine 只装 /metrics 端点，不构造完整路由：
// 完整路由要连 DB 和 Redis，测一个抓取端点没必要拖那些依赖进来。
func newMetricsEngine(t *testing.T, cfg config.MetricsConfig) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	r := gin.New()
	registerMetrics(r, cfg)
	return r
}

// TestMetricsEndpoint 验证默认配置下能抓到文本格式，且带对 Content-Type。
func TestMetricsEndpoint(t *testing.T) {
	r := newMetricsEngine(t, config.MetricsConfig{Enabled: true})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	// 只校验前缀：库会在后面追加 escaping=underscores 之类的协商参数，
	// 写死完整字符串会在升级库时无谓地失败。
	// 版本 0.0.4 是抓取端认的文本格式，写错会整次采集失败。
	contentType := w.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type = %q", contentType)
	}
	body := w.Body.String()
	// 运行时采集器在 init 里注册，任何进程都一定有，用它验证「注册表内容真的写出来了」。
	if !strings.Contains(body, "# HELP go_goroutines") {
		t.Errorf("缺少 go_goroutines 的 HELP 行")
	}
	if !strings.Contains(body, "# TYPE go_goroutines gauge") {
		t.Errorf("缺少 go_goroutines 的 TYPE 行")
	}
	if !strings.Contains(body, "process_uptime_seconds ") {
		t.Errorf("缺少 process_uptime_seconds 样本行")
	}
}

// TestMetricsDisabled 验证关掉后路由不存在，请求落到 404。
// 不注册而是返回空内容会让「没开」和「采集失败」两种情况分不清。
func TestMetricsDisabled(t *testing.T) {
	r := newMetricsEngine(t, config.MetricsConfig{Enabled: false})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("关闭后应当 404，实际 %d", w.Code)
	}
}

// TestMetricsToken 覆盖三种情况：不带 token、带错 token、带对 token。
func TestMetricsToken(t *testing.T) {
	const token = "s3cret-metrics-token"
	r := newMetricsEngine(t, config.MetricsConfig{Enabled: true, Token: token})

	cases := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{name: "不带 token", authHeader: "", wantStatus: http.StatusUnauthorized},
		{name: "Bearer 前缀缺失", authHeader: token, wantStatus: http.StatusUnauthorized},
		{name: "token 不对", authHeader: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "token 正确", authHeader: "Bearer " + token, wantStatus: http.StatusOK},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if c.authHeader != "" {
				req.Header.Set("Authorization", c.authHeader)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != c.wantStatus {
				t.Fatalf("status = %d，期望 %d", w.Code, c.wantStatus)
			}
			// 401 时不该漏出任何指标内容。
			if c.wantStatus == http.StatusUnauthorized && strings.Contains(w.Body.String(), "go_goroutines") {
				t.Error("401 响应里泄露了指标内容")
			}
		})
	}
}
