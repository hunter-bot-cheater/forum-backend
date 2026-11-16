package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/response"
)

// setUser 模拟 JWTAuth 已经把登录用户 id 写进 context。
// RequireAdmin 只读这个值，不负责解析 token，所以直接塞进去就够了。
func setUser(id uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(userIDKey, id)
		c.Next()
	}
}

// adminProbe 挂上「先塞登录用户，再判管理员」的完整链路。
// 顺序与真实路由一致：RequireAdmin 必须排在认证之后。
func adminProbe(adminIDs []uint, loginAs uint, calls *int) *gin.Engine {
	r := gin.New()
	r.POST("/admin", setUser(loginAs), RequireAdmin(adminIDs), func(c *gin.Context) {
		*calls = *calls + 1
		c.String(http.StatusOK, "ok")
	})
	return r
}

// TestRequireAdminAllowsListedUser 验证白名单里的用户能通过。
func TestRequireAdminAllowsListedUser(t *testing.T) {
	calls := 0
	r := adminProbe([]uint{7, 9}, 9, &calls)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200（body=%s）", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Errorf("handler 被调用 %d 次，期望 1 次", calls)
	}
}

// TestRequireAdminRejectsOthers 验证不在名单里的用户被 403 挡住，且 handler 不执行。
//
// 只断言状态码不够：如果响应写了 403 但忘了 Abort，
// handler 还是会跑完并产生副作用（比如真的建了一个活动）。
func TestRequireAdminRejectsOthers(t *testing.T) {
	calls := 0
	r := adminProbe([]uint{7}, 8, &calls)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d，期望 403", w.Code)
	}
	if calls != 0 {
		t.Errorf("handler 不该被调用，实际调用了 %d 次（漏了 c.Abort）", calls)
	}

	// 响应要是统一的 JSON 错误信封，前端靠 error_code 判断权限问题。
	var body struct {
		Code      int    `json:"code"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v（body=%s）", err, w.Body.String())
	}
	if body.Code != 1 || body.ErrorCode != response.ErrCodeForbidden {
		t.Errorf("响应 = %+v，期望 code=1 error_code=%s", body, response.ErrCodeForbidden)
	}
}

// TestRequireAdminEmptyListRejectsAll 验证空名单等于「谁都不能调」。
//
// 这是安全默认的核心：配置漏写时接口必须是关闭的，
// 而不是因为「没有任何人匹配」的逻辑写反而对所有人放行。
func TestRequireAdminEmptyListRejectsAll(t *testing.T) {
	for _, loginAs := range []uint{1, 7, 999} {
		calls := 0
		r := adminProbe(nil, loginAs, &calls)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin", nil))

		if w.Code != http.StatusForbidden {
			t.Errorf("user=%d：status = %d，期望 403", loginAs, w.Code)
		}
		if calls != 0 {
			t.Errorf("user=%d：handler 不该被调用", loginAs)
		}
	}
}

// TestRequireAdminIgnoresZeroInList 验证名单里写了 0 也不会放宽未登录请求。
//
// 0 是「取不到 userID」的零值。如果白名单里有 0，
// 任何一个认证失败却仍往下走的请求都会被当成管理员，这是最坏的一种漏洞。
func TestRequireAdminIgnoresZeroInList(t *testing.T) {
	calls := 0
	r := gin.New()
	// 不挂 setUser，模拟「没取到登录用户」：context 里没有 userIDKey。
	r.POST("/admin", RequireAdmin([]uint{0, 7}), func(c *gin.Context) {
		calls = calls + 1
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d，期望 403（0 不该被当成管理员）", w.Code)
	}
	if calls != 0 {
		t.Errorf("handler 不该被调用")
	}
}
