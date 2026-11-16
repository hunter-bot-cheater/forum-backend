package follow

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain 把 gin 切到测试模式，避免请求日志淹没断言输出。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// envelope 是统一响应格式的测试侧视图。
type envelope struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	ErrorCode string          `json:"error_code"`
}

// newFollowRouter 搭一个最小引擎：关注/取关走模拟登录，列表接口不挂鉴权。
// 真实路由里列表接口挂的是 OptionalJWTAuth，这里直接不挂，
// 因为 handler 对「没有 userID」的处理和游客请求完全一致。
func newFollowRouter(svc *Service, loginAs uint) *gin.Engine {
	h := NewHandler(svc)
	r := gin.New()
	r.PUT("/api/users/:id/follow", asUser(loginAs), h.Follow)
	r.DELETE("/api/users/:id/follow", asUser(loginAs), h.Unfollow)
	// 匿名路由只用来验证「取不到登录态必须 401」。
	r.PUT("/api/anonymous/users/:id/follow", h.Follow)
	r.GET("/api/users/:id/following", h.Following)
	r.GET("/api/users/:id/followers", h.Followers)
	return r
}

// asUser 模拟 JWTAuth：把用户 id 写进 gin.Context 的 userID 键。
func asUser(id uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("userID", id)
		c.Next()
	}
}

// do 发一次请求并解出统一响应。body 非空时按 JSON 发送。
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

// userPath 拼出 /api/users/<id>... 形式的路径。
func userPath(id uint, suffix string) string {
	return "/api/users/" + strconv.FormatUint(uint64(id), 10) + suffix
}

// TestFollowRequiresLogin 验证未登录关注返回 401。
func TestFollowRequiresLogin(t *testing.T) {
	f := newFollowFixture(t)
	r := newFollowRouter(f.svc, 0)

	w, env := do(t, r, http.MethodPut, userPath(f.bob.ID, "/follow"), "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d，期望 401", w.Code)
	}
	if env.ErrorCode != "unauthorized" {
		t.Errorf("error_code = %q，期望 unauthorized", env.ErrorCode)
	}
}

// TestFollowSelfReturns400 验证自关注被 service 挡下并映射成 400。
func TestFollowSelfReturns400(t *testing.T) {
	f := newFollowFixture(t)
	r := newFollowRouter(f.svc, f.alice.ID)

	w, env := do(t, r, http.MethodPut, userPath(f.alice.ID, "/follow"), "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d，期望 400", w.Code)
	}
	if env.ErrorCode != "invalid_input" {
		t.Errorf("error_code = %q，期望 invalid_input", env.ErrorCode)
	}
}

// TestFollowUnknownUserReturns404 验证关注不存在的用户返回 404。
func TestFollowUnknownUserReturns404(t *testing.T) {
	f := newFollowFixture(t)
	r := newFollowRouter(f.svc, f.alice.ID)

	w, _ := do(t, r, http.MethodPut, userPath(999999, "/follow"), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d，期望 404", w.Code)
	}
}

// TestFollowThenUnfollow 验证一次完整的关注到取关，响应里的状态与粉丝数正确。
func TestFollowThenUnfollow(t *testing.T) {
	f := newFollowFixture(t)
	r := newFollowRouter(f.svc, f.alice.ID)

	w, env := do(t, r, http.MethodPut, userPath(f.bob.ID, "/follow"), "")
	if w.Code != http.StatusOK {
		t.Fatalf("关注 status = %d, body=%s", w.Code, w.Body.String())
	}
	var state struct {
		Following     bool  `json:"following"`
		FollowerCount int64 `json:"follower_count"`
		Changed       bool  `json:"changed"`
	}
	if err := json.Unmarshal(env.Data, &state); err != nil {
		t.Fatalf("解析关注响应失败: %v", err)
	}
	if !state.Following || state.FollowerCount != 1 || !state.Changed {
		t.Errorf("关注响应 = %+v，期望 following=true count=1 changed=true", state)
	}

	w, env = do(t, r, http.MethodDelete, userPath(f.bob.ID, "/follow"), "")
	if w.Code != http.StatusOK {
		t.Fatalf("取关 status = %d, body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(env.Data, &state); err != nil {
		t.Fatalf("解析取关响应失败: %v", err)
	}
	if state.Following || state.FollowerCount != 0 || !state.Changed {
		t.Errorf("取关响应 = %+v，期望 following=false count=0 changed=true", state)
	}
}

// TestFollowingListShape 验证列表响应的形状：list 是数组、total 与分页参数回显正确。
func TestFollowingListShape(t *testing.T) {
	f := newFollowFixture(t)
	r := newFollowRouter(f.svc, 0)

	// 先让 alice 关注 bob，列表才有内容。
	if _, err := f.svc.Follow(t.Context(), f.alice.ID, f.bob.ID); err != nil {
		t.Fatalf("准备关注失败: %v", err)
	}

	w, env := do(t, r, http.MethodGet, userPath(f.alice.ID, "/following?page=1&page_size=5"), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		List     []map[string]any `json:"list"`
		Total    int64            `json:"total"`
		Page     int              `json:"page"`
		PageSize int              `json:"page_size"`
	}
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if resp.Total != 1 || len(resp.List) != 1 {
		t.Fatalf("total=%d len=%d，期望都是 1", resp.Total, len(resp.List))
	}
	if resp.Page != 1 || resp.PageSize != 5 {
		t.Errorf("分页回显 = %d/%d，期望 1/5", resp.Page, resp.PageSize)
	}
	if got := resp.List[0]["username"]; got != "bob" {
		t.Errorf("列表里的 username = %v，期望 bob", got)
	}
}

// TestFollowingEmptyListIsArray 验证空列表输出 []，不是 null。
// 前端直接对 list 做 map 时，null 会让页面白屏。
func TestFollowingEmptyListIsArray(t *testing.T) {
	f := newFollowFixture(t)
	r := newFollowRouter(f.svc, 0)

	w, env := do(t, r, http.MethodGet, userPath(f.carol.ID, "/followers"), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if resp.List == nil {
		t.Error("空列表应当是 []，实际是 null")
	}
	if len(resp.List) != 0 {
		t.Errorf("空列表条数 = %d，期望 0", len(resp.List))
	}
}

// TestListBadPathID 验证路径 id 非数字返回 400。
func TestListBadPathID(t *testing.T) {
	f := newFollowFixture(t)
	r := newFollowRouter(f.svc, 0)

	w, env := do(t, r, http.MethodGet, "/api/users/abc/followers", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d，期望 400", w.Code)
	}
	if env.ErrorCode != "bad_request" {
		t.Errorf("error_code = %q，期望 bad_request", env.ErrorCode)
	}
}
