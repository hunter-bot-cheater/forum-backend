package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// newHealthEngine 只装配探活路由，注入假检查，不碰真实 DB / Redis。
func newHealthEngine(t *testing.T, checks []healthCheck) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerHealth(r, checks)
	return r
}

// probe 发一次 GET 并解析 JSON 响应。
func probe(t *testing.T, r *gin.Engine, path string) (*httptest.ResponseRecorder, healthResp) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)

	var body healthResp
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s 返回的不是 JSON: %v, raw=%q", path, err, w.Body.String())
	}
	return w, body
}

// 存活检查不关心任何外部依赖，数据库挂了也必须返回 200。
func TestLivenessIgnoresDependencies(t *testing.T) {
	r := newHealthEngine(t, []healthCheck{
		{name: "mysql", check: func(context.Context) error { return errors.New("db down") }},
	})

	w, body := probe(t, r, "/healthz")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if body.Status != "ok" {
		t.Errorf("status 字段 = %q, want ok", body.Status)
	}
	if len(body.Checks) != 0 {
		t.Errorf("存活检查不该探测依赖，得到 checks=%v", body.Checks)
	}
}

// 依赖都正常时就绪检查返回 200，并把每一项标成 ok。
func TestReadyzAllHealthy(t *testing.T) {
	r := newHealthEngine(t, []healthCheck{
		{name: "mysql", check: func(context.Context) error { return nil }},
		{name: "redis", check: func(context.Context) error { return nil }},
	})

	w, body := probe(t, r, "/readyz")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if body.Status != "ok" {
		t.Errorf("status 字段 = %q, want ok", body.Status)
	}
	if body.Checks["mysql"] != "ok" || body.Checks["redis"] != "ok" {
		t.Errorf("checks = %v, want 全 ok", body.Checks)
	}
}

// 任一依赖失败就返回 503，且不把具体错误信息泄露给调用方。
func TestReadyzFailsOnDependencyError(t *testing.T) {
	r := newHealthEngine(t, []healthCheck{
		{name: "mysql", check: func(context.Context) error { return errors.New("dial tcp 127.0.0.1:3306: refused") }},
		{name: "redis", check: func(context.Context) error { return nil }},
	})

	w, body := probe(t, r, "/readyz")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if body.Status != "fail" {
		t.Errorf("status 字段 = %q, want fail", body.Status)
	}
	if body.Checks["mysql"] != "fail" || body.Checks["redis"] != "ok" {
		t.Errorf("checks = %v, want mysql=fail redis=ok", body.Checks)
	}
	if got := w.Body.String(); got != `{"status":"fail","checks":{"mysql":"fail","redis":"ok"}}` {
		t.Errorf("响应体泄露了依赖错误细节: %s", got)
	}
}

// 检查函数拿到的是带超时的 ctx，探活不会被卡住的依赖无限拖住。
func TestReadyzPassesDeadlineToChecks(t *testing.T) {
	r := newHealthEngine(t, []healthCheck{
		{name: "mysql", check: func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("检查函数拿到的 ctx 没有 deadline")
			}
			return nil
		}},
	})

	if w, _ := probe(t, r, "/readyz"); w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}
