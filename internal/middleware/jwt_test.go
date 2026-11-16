package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/jwt"
)

// TestMain 把 gin 切到测试模式，避免请求日志干扰断言输出。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// newJWTManager 造一个测试用的签发器。密钥长度满足配置校验即可。
func newJWTManager(t *testing.T) *jwt.Manager {
	t.Helper()

	m, err := jwt.NewManager("test-secret-at-least-32-characters-long", 15*time.Minute, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("创建 JWT 管理器失败: %v", err)
	}
	return m
}

// probeHandler 把「中间件是否写入了 userID」回报成响应内容。
// 200 + userID 表示解析成功，200 + 空表示放行但按游客处理。
func probeHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.String(http.StatusOK, "%d", c.GetUint(userIDKey))
	}
}

// TestOptionalJWTAuthWithoutToken 验证没带 token 时放行，且不写 userID。
func TestOptionalJWTAuthWithoutToken(t *testing.T) {
	r := gin.New()
	r.GET("/probe", OptionalJWTAuth(newJWTManager(t)), probeHandler())

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200", w.Code)
	}
	if w.Body.String() != "0" {
		t.Errorf("userID = %q，期望 0（未登录）", w.Body.String())
	}
}

// TestOptionalJWTAuthWithValidToken 验证有效 token 被解析并写入 userID。
func TestOptionalJWTAuthWithValidToken(t *testing.T) {
	m := newJWTManager(t)
	token, err := m.SignAccess(42)
	if err != nil {
		t.Fatalf("签发 token 失败: %v", err)
	}

	r := gin.New()
	r.GET("/probe", OptionalJWTAuth(m), probeHandler())

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200", w.Code)
	}
	if w.Body.String() != "42" {
		t.Errorf("userID = %q，期望 42", w.Body.String())
	}
}

// TestOptionalJWTAuthWithBadToken 验证坏 token 静默降级为游客。
// 这是它与 JWTAuth 的关键差别：公开接口不该因为过期 token 变成 401。
func TestOptionalJWTAuthWithBadToken(t *testing.T) {
	r := gin.New()
	r.GET("/probe", OptionalJWTAuth(newJWTManager(t)), probeHandler())

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200", w.Code)
	}
	if w.Body.String() != "0" {
		t.Errorf("userID = %q，期望 0", w.Body.String())
	}
}

// TestJWTAuthRejectsMissingHeader 验证必选鉴权仍然拒绝匿名请求。
func TestJWTAuthRejectsMissingHeader(t *testing.T) {
	r := gin.New()
	r.GET("/probe", JWTAuth(newJWTManager(t)), probeHandler())

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d，期望 401", w.Code)
	}
}
