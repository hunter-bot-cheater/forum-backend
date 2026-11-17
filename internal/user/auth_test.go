package user

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/session"
	"forum-backend/internal/testutil"
	"forum-backend/pkg/jwt"
)

// tokenPair 是登录与刷新响应的测试侧视图。
type tokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	User         struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

// newAuthRouter 搭一个只挂认证四条路由的引擎，依赖用真实测试库与内存 Redis。
// 用真实 service 而不是打桩：这一批测的正是「签发与登记是否同步」这类跨组件行为，
// 打桩会把要验证的东西一起替掉。
func newAuthRouter(t *testing.T) *gin.Engine {
	t.Helper()

	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	manager, err := jwt.NewManager("test-secret-at-least-32-characters", 15*time.Minute, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	// 认证链路用不到管理员判定，给一个恒 false 的函数即可：
	// 这里没有 /users/me 路由，is_admin 不会出现在任何响应里。
	h := NewUserHandler(NewUserService(db), session.NewService(manager, rdb), NewLoginGuard(rdb),
		stubFollowStats{}, func(uint) bool { return false })

	r := gin.New()
	r.POST("/api/register", h.Register)
	r.POST("/api/login", h.Login)
	r.POST("/api/auth/refresh", h.Refresh)
	r.POST("/api/auth/logout", h.Logout)
	return r
}

// registerAndLogin 注册一个用户并登录，返回登录响应里的 token 对。
// 用户名固定为 alice：每个用例的测试库都是清空后的状态，不会互相撞名。
func registerAndLogin(t *testing.T, r *gin.Engine) tokenPair {
	t.Helper()

	if w, _ := do(t, r, http.MethodPost, "/api/register", `{"username":"alice","password":"password123"}`); w.Code != http.StatusOK {
		t.Fatalf("注册失败: status = %d", w.Code)
	}
	w, env := do(t, r, http.MethodPost, "/api/login", `{"username":"alice","password":"password123"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败: status = %d, body = %s", w.Code, w.Body.String())
	}
	var pair tokenPair
	if err := json.Unmarshal(env.Data, &pair); err != nil {
		t.Fatalf("解析登录响应失败: %v", err)
	}
	return pair
}

// TestLoginReturnsTokenPair 验证登录响应形状：两个 token 加过期秒数加用户资料。
// 前端同时依赖三者，少一个都会让拦截器或顶栏缺东西。
func TestLoginReturnsTokenPair(t *testing.T) {
	r := newAuthRouter(t)
	pair := registerAndLogin(t, r)

	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("token 为空: %+v", pair)
	}
	if pair.AccessToken == pair.RefreshToken {
		t.Fatal("access 与 refresh 相同，说明只签了一种")
	}
	if pair.ExpiresIn != 900 {
		t.Fatalf("ExpiresIn = %d, want 900", pair.ExpiresIn)
	}
	if pair.User.Username != "alice" || pair.User.ID == 0 {
		t.Fatalf("用户资料不对: %+v", pair.User)
	}
}

// TestRefreshReturnsNewPair 验证刷新接口返回新的一对，且不返回用户资料（用不到）。
func TestRefreshReturnsNewPair(t *testing.T) {
	r := newAuthRouter(t)
	pair := registerAndLogin(t, r)

	w, env := do(t, r, http.MethodPost, "/api/auth/refresh",
		`{"refresh_token":"`+pair.RefreshToken+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("刷新失败: status = %d, body = %s", w.Code, w.Body.String())
	}
	var next tokenPair
	if err := json.Unmarshal(env.Data, &next); err != nil {
		t.Fatalf("解析刷新响应失败: %v", err)
	}
	if next.AccessToken == "" || next.RefreshToken == "" {
		t.Fatalf("刷新返回的 token 为空: %+v", next)
	}
	if next.RefreshToken == pair.RefreshToken {
		t.Fatal("refresh 没有旋转")
	}
}

// TestRefreshReuseReturns401 验证旧 refresh 被重放时返回 401。
// 这是旋转的安全边界：不返回 401 就等于旧票永远能用。
func TestRefreshReuseReturns401(t *testing.T) {
	r := newAuthRouter(t)
	pair := registerAndLogin(t, r)

	body := `{"refresh_token":"` + pair.RefreshToken + `"}`
	if w, _ := do(t, r, http.MethodPost, "/api/auth/refresh", body); w.Code != http.StatusOK {
		t.Fatalf("第一次刷新失败: status = %d", w.Code)
	}
	if w, _ := do(t, r, http.MethodPost, "/api/auth/refresh", body); w.Code != http.StatusUnauthorized {
		t.Fatalf("重放旧 refresh status = %d, want 401", w.Code)
	}
}

// TestRefreshRejectsAccessToken 验证拿 access 当 refresh 用会被拒。
// 少了这层校验，短有效期的 access 就能被当成 7 天的续期凭证。
func TestRefreshRejectsAccessToken(t *testing.T) {
	r := newAuthRouter(t)
	pair := registerAndLogin(t, r)

	w, _ := do(t, r, http.MethodPost, "/api/auth/refresh",
		`{"refresh_token":"`+pair.AccessToken+`"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("access 当 refresh status = %d, want 401", w.Code)
	}
}

// TestRefreshRejectsMissingBody 验证缺字段时是 400 而不是 500。
// 前端在 bug 状态下可能发出空 body，这时该给参数错误，而不是让人以为服务挂了。
func TestRefreshRejectsMissingBody(t *testing.T) {
	r := newAuthRouter(t)
	if w, _ := do(t, r, http.MethodPost, "/api/auth/refresh", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("空 body status = %d, want 400", w.Code)
	}
}

// TestLogoutRevokesRefresh 验证登出之后同一个 refresh 不能再换 token。
// 这正是「登出能真正踢掉设备」与旧实现的区别：以前只是前端删了 localStorage。
func TestLogoutRevokesRefresh(t *testing.T) {
	r := newAuthRouter(t)
	pair := registerAndLogin(t, r)

	body := `{"refresh_token":"` + pair.RefreshToken + `"}`
	if w, _ := do(t, r, http.MethodPost, "/api/auth/logout", body); w.Code != http.StatusOK {
		t.Fatalf("登出失败: status = %d", w.Code)
	}
	if w, _ := do(t, r, http.MethodPost, "/api/auth/refresh", body); w.Code != http.StatusUnauthorized {
		t.Fatalf("登出后刷新 status = %d, want 401", w.Code)
	}
}

// TestLogoutIsIdempotent 验证重复登出仍然成功。
// 前端可能因为网络重试发两次，第二次不该变成错误弹窗。
func TestLogoutIsIdempotent(t *testing.T) {
	r := newAuthRouter(t)
	pair := registerAndLogin(t, r)

	body := `{"refresh_token":"` + pair.RefreshToken + `"}`
	for i := 0; i < 2; i++ {
		if w, _ := do(t, r, http.MethodPost, "/api/auth/logout", body); w.Code != http.StatusOK {
			t.Fatalf("第 %d 次登出 status = %d, want 200", i+1, w.Code)
		}
	}
}

// TestLoginLocksAfterRepeatedFailures 验证连续输错达到阈值后，即使密码正确也被拒。
//
// 关键在最后一步用正确密码：只验证「错密码返回 429」说明不了锁定生效，
// 因为那也可能是密码错误本身的分支。正确密码被 429 才算真的拦在了验证之前。
func TestLoginLocksAfterRepeatedFailures(t *testing.T) {
	r := newAuthRouter(t)
	// 先注册，保证用户存在；注册接口与登录共用凭证桶，但测试没挂限流中间件。
	if w, _ := do(t, r, http.MethodPost, "/api/register", `{"username":"alice","password":"password123"}`); w.Code != http.StatusOK {
		t.Fatalf("注册失败: status = %d", w.Code)
	}

	for i := 0; i < loginMaxAttempts; i++ {
		w, _ := do(t, r, http.MethodPost, "/api/login", `{"username":"alice","password":"wrong-password"}`)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码 status = %d, want 401", i+1, w.Code)
		}
	}

	w, _ := do(t, r, http.MethodPost, "/api/login", `{"username":"alice","password":"password123"}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定后用正确密码 status = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("429 响应缺少 Retry-After 头，前端无法告诉用户还要等多久")
	}
}

// TestLoginSuccessClearsFailureCount 验证成功登录会把计数清零。
//
// 不清零的表现为：一个人长期偶尔输错，某天早上突然被锁，而他昨晚刚登录成功过。
func TestLoginSuccessClearsFailureCount(t *testing.T) {
	r := newAuthRouter(t)
	if w, _ := do(t, r, http.MethodPost, "/api/register", `{"username":"alice","password":"password123"}`); w.Code != http.StatusOK {
		t.Fatalf("注册失败: status = %d", w.Code)
	}

	// 错到差一次就锁。
	for i := 0; i < loginMaxAttempts-1; i++ {
		if w, _ := do(t, r, http.MethodPost, "/api/login", `{"username":"alice","password":"wrong-password"}`); w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码 status = %d", i+1, w.Code)
		}
	}
	if w, _ := do(t, r, http.MethodPost, "/api/login", `{"username":"alice","password":"password123"}`); w.Code != http.StatusOK {
		t.Fatalf("密码正确时应能登录，status = %d", w.Code)
	}

	// 清零之后又错一次，仍然是 401 而不是 429。
	if w, _ := do(t, r, http.MethodPost, "/api/login", `{"username":"alice","password":"wrong-password"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("清零后再错一次 status = %d, want 401", w.Code)
	}
}
