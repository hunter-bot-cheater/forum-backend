package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/domain"
	"forum-backend/internal/httputil"
	"forum-backend/internal/middleware"
	"forum-backend/pkg/response"
)

// newContractEngine 只装配兜底中间件和几个探针路由，不碰 DB/Redis。
func newContractEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	installFallbacks(r)

	r.GET("/ok", func(c *gin.Context) {
		response.OK(c, gin.H{"x": 1})
	})
	r.GET("/only-get", func(c *gin.Context) {
		response.OK(c, nil)
	})
	r.GET("/panic", func(c *gin.Context) {
		panic("contract-probe-panic")
	})
	r.GET("/unauthorized", func(c *gin.Context) {
		response.Error(c, http.StatusUnauthorized, "未登录")
	})
	r.GET("/floor-taken", func(c *gin.Context) {
		httputil.MapServiceError(c, "grab", domain.ErrFloorTaken)
	})
	r.GET("/floor-inactive", func(c *gin.Context) {
		httputil.MapServiceError(c, "grab", domain.ErrFloorNotActive)
	})
	r.GET("/rate-limited", func(c *gin.Context) {
		response.Error(c, http.StatusTooManyRequests, "请求太频繁，请稍后再试")
	})
	return r
}

// call 发一次请求并解析响应体。所有路径都必须是合法 JSON，否则测试直接失败。
func call(t *testing.T, r *gin.Engine, method, path, requestID string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if requestID != "" {
		req.Header.Set(middleware.HeaderRequestID, requestID)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s %s 返回的不是 JSON: %v, raw=%q", method, path, err, w.Body.String())
	}
	return w, body
}

func wantString(t *testing.T, body map[string]any, key, want string) {
	t.Helper()
	got, _ := body[key].(string)
	if got != want {
		t.Errorf("body[%q] = %q, want %q", key, got, want)
	}
}

func TestNoRouteReturnsUnifiedJSON(t *testing.T) {
	r := newContractEngine()
	w, body := call(t, r, http.MethodGet, "/api/nope", "")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want json", ct)
	}
	if body["code"] != float64(1) {
		t.Errorf("code = %v, want 1", body["code"])
	}
	wantString(t, body, "error_code", response.ErrCodeRouteNotFound)
	wantString(t, body, "message", "接口不存在")

	id, _ := body["request_id"].(string)
	if len(id) != 32 {
		t.Errorf("request_id = %q, want 32 位十六进制", id)
	}
	if w.Header().Get(middleware.HeaderRequestID) != id {
		t.Errorf("响应头 X-Request-ID 与响应体不一致")
	}
}

func TestMethodNotAllowedReturns405(t *testing.T) {
	r := newContractEngine()
	w, body := call(t, r, http.MethodDelete, "/only-get", "")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
	wantString(t, body, "error_code", response.ErrCodeMethodNotAllowed)
}

func TestPanicReturnsUnifiedJSON(t *testing.T) {
	r := newContractEngine()
	w, body := call(t, r, http.MethodGet, "/panic", "")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	wantString(t, body, "error_code", response.ErrCodeInternal)
	if !strings.Contains(w.Body.String(), "request_id") {
		t.Errorf("500 响应缺少 request_id")
	}
}

func TestSuccessKeepsOldShape(t *testing.T) {
	r := newContractEngine()
	w, body := call(t, r, http.MethodGet, "/ok", "")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if body["code"] != float64(0) {
		t.Errorf("code = %v, want 0", body["code"])
	}
	if _, ok := body["error_code"]; ok {
		t.Errorf("成功响应不应带 error_code")
	}
	if _, ok := body["request_id"]; ok {
		t.Errorf("成功响应不应带 request_id")
	}
	if w.Header().Get(middleware.HeaderRequestID) == "" {
		t.Errorf("成功响应也要带 X-Request-ID 响应头")
	}
}

func Test401CarriesAuthenticateHeader(t *testing.T) {
	r := newContractEngine()
	w, body := call(t, r, http.MethodGet, "/unauthorized", "")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("401 缺少 WWW-Authenticate: Bearer")
	}
	wantString(t, body, "error_code", response.ErrCodeUnauthorized)
}

func TestSameStatusSplitByErrorCode(t *testing.T) {
	r := newContractEngine()

	_, taken := call(t, r, http.MethodGet, "/floor-taken", "")
	wantString(t, taken, "error_code", response.ErrCodeFloorTaken)

	_, inactive := call(t, r, http.MethodGet, "/floor-inactive", "")
	wantString(t, inactive, "error_code", response.ErrCodeFloorNotActive)
}

func TestErrorCodeFallbackByStatus(t *testing.T) {
	r := newContractEngine()
	w, body := call(t, r, http.MethodGet, "/rate-limited", "")

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
	wantString(t, body, "error_code", response.ErrCodeRateLimited)
}

func TestRequestIDPassthroughAndSanitize(t *testing.T) {
	r := newContractEngine()

	_, accepted := call(t, r, http.MethodGet, "/api/nope", "trace-ABC_123")
	wantString(t, accepted, "request_id", "trace-ABC_123")

	_, rejected := call(t, r, http.MethodGet, "/api/nope", "bad\ninjected")
	id, _ := rejected["request_id"].(string)
	if id == "bad\ninjected" {
		t.Errorf("非法 X-Request-ID 被原样回显，存在日志注入风险")
	}
	if len(id) != 32 {
		t.Errorf("非法 X-Request-ID 应换成 32 位十六进制，得到 %q", id)
	}
}
