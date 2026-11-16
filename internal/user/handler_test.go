package user

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/testutil"
)

// TestMain 把 gin 切到测试模式，避免每个请求都打调试日志。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// envelope 是本项目统一响应格式的测试侧视图。
// Data 用 json.RawMessage 接住，用例再按自己的类型解一层。
type envelope struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	ErrorCode string          `json:"error_code"`
}

// newUserRouter 搭一个只挂用户模块三条路由的最小引擎。
// 不套真实 JWTAuth：这里测的是 handler 自己的分支，
// 登录态直接用 asUser 中间件模拟（和中间件写进 context 的键一致）。
func newUserRouter(svc *UserService, loginAs uint) *gin.Engine {
	h := NewUserHandler(svc, nil)
	r := gin.New()
	r.GET("/api/users/:id", h.Detail)
	r.GET("/api/users/me", asUser(loginAs), h.Me)
	r.PUT("/api/users/me", asUser(loginAs), h.UpdateMe)
	return r
}

// asUser 模拟 JWTAuth：把用户 id 写进 gin.Context 的 userID 键。
func asUser(id uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("userID", id)
		c.Next()
	}
}

// do 发一个请求并把响应解成 envelope。body 非空时按 JSON 发送。
func do(t *testing.T, r *gin.Engine, method, path, body string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s %s 返回的不是统一响应格式: %v, raw=%q", method, path, err, w.Body.String())
	}
	return w, env
}

// TestDetailReturnsFallbackNickname 验证公开主页返回 200，且昵称回退成用户名。
func TestDetailReturnsFallbackNickname(t *testing.T) {
	db := testutil.NewDB(t)
	u := testutil.SeedUser(t, db, "alice")
	r := newUserRouter(NewUserService(db), u.ID)

	w, env := do(t, r, http.MethodGet, "/api/users/"+strconv.FormatUint(uint64(u.ID), 10), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200", w.Code)
	}

	var got userResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if got.Nickname != "alice" {
		t.Errorf("nickname = %q，期望回退成 alice", got.Nickname)
	}
}

// TestDetailBadIDAndMissing 验证非法 id 是 400、不存在的 id 是 404。
func TestDetailBadIDAndMissing(t *testing.T) {
	db := testutil.NewDB(t)
	r := newUserRouter(NewUserService(db), 1)

	if w, _ := do(t, r, http.MethodGet, "/api/users/abc", ""); w.Code != http.StatusBadRequest {
		t.Errorf("非法 id status = %d，期望 400", w.Code)
	}
	if w, _ := do(t, r, http.MethodGet, "/api/users/999999", ""); w.Code != http.StatusNotFound {
		t.Errorf("不存在 id status = %d，期望 404", w.Code)
	}
}

// TestMeRequiresLogin 验证取不到登录态时返回 401。
func TestMeRequiresLogin(t *testing.T) {
	db := testutil.NewDB(t)
	// loginAs=0 等价于中间件没写 userID。
	r := newUserRouter(NewUserService(db), 0)

	w, env := do(t, r, http.MethodGet, "/api/users/me", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d，期望 401", w.Code)
	}
	if env.ErrorCode != "unauthorized" {
		t.Errorf("error_code = %q，期望 unauthorized", env.ErrorCode)
	}
}

// TestUpdateMeThenReadBack 验证修改资料后再读回能拿到新值。
func TestUpdateMeThenReadBack(t *testing.T) {
	db := testutil.NewDB(t)
	u := testutil.SeedUser(t, db, "bob")
	r := newUserRouter(NewUserService(db), u.ID)

	w, env := do(t, r, http.MethodPut, "/api/users/me", `{"nickname":"波波","bio":"你好"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200, body=%s", w.Code, w.Body.String())
	}

	var updated userResp
	if err := json.Unmarshal(env.Data, &updated); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if updated.Nickname != "波波" || updated.Bio != "你好" {
		t.Errorf("更新结果 = %+v", updated)
	}

	// 独立再发一次 GET，确认落库而不只是回显请求体。
	_, env = do(t, r, http.MethodGet, "/api/users/me", "")
	var read userResp
	if err := json.Unmarshal(env.Data, &read); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if read.Nickname != "波波" {
		t.Errorf("读回的 nickname = %q，期望 波波", read.Nickname)
	}
}

// TestUpdateMeInvalidBody 验证请求体格式错误返回 400 而不是 500。
func TestUpdateMeInvalidBody(t *testing.T) {
	db := testutil.NewDB(t)
	u := testutil.SeedUser(t, db, "carol")
	r := newUserRouter(NewUserService(db), u.ID)

	// nickname 传数字：JSON 能解析，但类型不匹配，ShouldBindJSON 会失败。
	w, env := do(t, r, http.MethodPut, "/api/users/me", `{"nickname":123}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d，期望 400", w.Code)
	}
	if env.ErrorCode != "bad_request" {
		t.Errorf("error_code = %q，期望 bad_request", env.ErrorCode)
	}
}

// TestUpdateMeValidationError 验证 service 的校验错误被映射成 400 + invalid_input。
func TestUpdateMeValidationError(t *testing.T) {
	db := testutil.NewDB(t)
	u := testutil.SeedUser(t, db, "dave")
	r := newUserRouter(NewUserService(db), u.ID)

	w, env := do(t, r, http.MethodPut, "/api/users/me", `{"avatar_url":"javascript:alert(1)"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d，期望 400", w.Code)
	}
	if env.ErrorCode != "invalid_input" {
		t.Errorf("error_code = %q，期望 invalid_input", env.ErrorCode)
	}
}
