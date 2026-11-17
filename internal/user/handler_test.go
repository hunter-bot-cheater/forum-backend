package user

import (
	"context"
	"encoding/json"
	"errors"
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

// stubFollowStats 是关注统计的测试替身。
// 真实现要查 follows 表，而这里只验证响应形状与错误分支，用固定值更快也更可控。
type stubFollowStats struct {
	following   int64
	followers   int64
	isFollowing bool
	err         error
}

func (s stubFollowStats) Stats(ctx context.Context, userID, viewerID uint) (int64, int64, bool, error) {
	return s.following, s.followers, s.isFollowing, s.err
}

// newUserRouter 搭一个只挂用户模块三条路由的最小引擎。
// 不套真实 JWTAuth：这里测的是 handler 自己的分支，
// 登录态直接用 asUser 中间件模拟（和中间件写进 context 的键一致）。
func newUserRouter(svc *UserService, loginAs uint) *gin.Engine {
	return newUserRouterWithStats(svc, loginAs, stubFollowStats{})
}

// newUserRouterWithStats 与 newUserRouter 相同，但允许指定关注统计的返回值。
func newUserRouterWithStats(svc *UserService, loginAs uint, stats FollowStats) *gin.Engine {
	// 默认按「不是管理员」装配：绝大多数用例不关心这个身份，
	// 只有专门测 is_admin 的用例才用下面那个能指定白名单的版本。
	return newUserRouterWithAdmin(svc, loginAs, stats, func(uint) bool { return false })
}

// newUserRouterWithAdmin 允许指定管理员判定，用来测 /users/me 的 is_admin。
func newUserRouterWithAdmin(svc *UserService, loginAs uint, stats FollowStats, isAdmin func(uint) bool) *gin.Engine {
	h := NewUserHandler(svc, nil, nil, stats, isAdmin)
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

// TestDetailIncludesFollowStats 验证主页响应带上关注数、粉丝数与 is_following。
// 统计值由替身提供，这里只关心它们是否被正确序列化到顶层。
func TestDetailIncludesFollowStats(t *testing.T) {
	db := testutil.NewDB(t)
	u := testutil.SeedUser(t, db, "erin")
	stats := stubFollowStats{following: 3, followers: 5, isFollowing: true}
	r := newUserRouterWithStats(NewUserService(db), u.ID, stats)

	w, env := do(t, r, http.MethodGet, "/api/users/"+strconv.FormatUint(uint64(u.ID), 10), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200", w.Code)
	}

	// 用匿名结构体接：内嵌的 userResp 字段会被平铺，统计字段也在同一层。
	var got struct {
		ID             uint  `json:"id"`
		FollowingCount int64 `json:"following_count"`
		FollowerCount  int64 `json:"follower_count"`
		IsFollowing    bool  `json:"is_following"`
	}
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("id = %d，期望 %d", got.ID, u.ID)
	}
	if got.FollowingCount != 3 || got.FollowerCount != 5 {
		t.Errorf("统计 = %d/%d，期望 3/5", got.FollowingCount, got.FollowerCount)
	}
	if !got.IsFollowing {
		t.Error("is_following 应当为 true")
	}
}

// TestDetailStatsErrorMapsTo500 验证统计查询失败时返回 500 而不是空统计。
// 静默返回 0 会让前端显示「0 粉丝」，比报错更难排查。
func TestDetailStatsErrorMapsTo500(t *testing.T) {
	db := testutil.NewDB(t)
	u := testutil.SeedUser(t, db, "frank")
	r := newUserRouterWithStats(NewUserService(db), u.ID, stubFollowStats{err: errors.New("boom")})

	w, env := do(t, r, http.MethodGet, "/api/users/"+strconv.FormatUint(uint64(u.ID), 10), "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d，期望 500", w.Code)
	}
	if env.ErrorCode != "internal_error" {
		t.Errorf("error_code = %q，期望 internal_error", env.ErrorCode)
	}
}

// TestUpdateMeReturnsProfileShape 验证 PUT /users/me 与 GET /users/me 返回同一形状。
//
// 前端保存资料后通常直接拿返回值替换本地状态。如果 PUT 只回资料本体、
// 不带 following_count / follower_count，替换之后主页上的关注数会变成 0，
// 用户看到的是「刚改完昵称，粉丝没了」。
func TestUpdateMeReturnsProfileShape(t *testing.T) {
	db := testutil.NewDB(t)
	u := testutil.SeedUser(t, db, "grace")
	stats := stubFollowStats{following: 3, followers: 5}
	r := newUserRouterWithStats(NewUserService(db), u.ID, stats)

	w, env := do(t, r, http.MethodPut, "/api/users/me", `{"bio":"改个简介"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200, body=%s", w.Code, w.Body.String())
	}

	var got struct {
		Bio            string `json:"bio"`
		FollowingCount int64  `json:"following_count"`
		FollowerCount  int64  `json:"follower_count"`
	}
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if got.Bio != "改个简介" {
		t.Errorf("bio = %q，期望 改个简介", got.Bio)
	}
	if got.FollowingCount != 3 || got.FollowerCount != 5 {
		t.Errorf("统计 = %d/%d，期望 3/5", got.FollowingCount, got.FollowerCount)
	}
}

// TestMeReportsAdminFlag 验证 /users/me 的 is_admin 跟白名单判定一致。
//
// 前端用户菜单靠这个字段决定要不要显示「管理后台」入口：判错了，
// 普通用户点进去只会看到 403 的空页，而真正的管理员根本找不到入口。
func TestMeReportsAdminFlag(t *testing.T) {
	db := testutil.NewDB(t)
	u := testutil.SeedUser(t, db, "admin_flag")
	svc := NewUserService(db)

	// 同一个人、同一个接口，只换判定函数：分数来自白名单，与用户自身数据无关。
	asAdmin := newUserRouterWithAdmin(svc, u.ID, stubFollowStats{}, func(id uint) bool { return id == u.ID })
	w, env := do(t, asAdmin, http.MethodGet, "/api/users/me", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200, body=%s", w.Code, w.Body.String())
	}
	var admin struct {
		IsAdmin bool `json:"is_admin"`
	}
	if err := json.Unmarshal(env.Data, &admin); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if !admin.IsAdmin {
		t.Error("is_admin = false，期望 true")
	}

	// 不在白名单里：同一个用户必须拿到 false，
	// 而不是因为「资料里刚好有个字段」被带出来。
	asUserRouter := newUserRouter(svc, u.ID)
	_, env = do(t, asUserRouter, http.MethodGet, "/api/users/me", "")
	var normal struct {
		IsAdmin bool `json:"is_admin"`
	}
	if err := json.Unmarshal(env.Data, &normal); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if normal.IsAdmin {
		t.Error("is_admin = true，期望 false")
	}
}

// TestPublicProfileHasNoAdminFlag 验证公开主页不暴露 is_admin。
//
// 这个字段的主语是「查看者」，不是「主页上这个人」。如果它跟着 profileResp
// 一起出现，看别人的主页时会被读成「这个人是管理员」——语义完全错了。
func TestPublicProfileHasNoAdminFlag(t *testing.T) {
	db := testutil.NewDB(t)
	me := testutil.SeedUser(t, db, "viewer")
	target := testutil.SeedUser(t, db, "someone")
	// 把自己设成管理员，确保「字段没被带出来」不是因为刚好没人有权限。
	r := newUserRouterWithAdmin(NewUserService(db), me.ID, stubFollowStats{},
		func(id uint) bool { return id == me.ID })

	w, env := do(t, r, http.MethodGet, "/api/users/"+strconv.FormatUint(uint64(target.ID), 10), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200, body=%s", w.Code, w.Body.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if _, ok := got["is_admin"]; ok {
		t.Error("公开主页出现了 is_admin，这个字段只属于 /users/me")
	}
}

// TestUpdateMeClearNicknameRoundTrip 验证「清空昵称」这条链路是自洽的。// 传空串后：库里存空串，响应 nickname 回退成用户名，同时 has_custom_nickname 变 false。
// 少了最后这个布尔值，前端编辑表单拿 nickname 回填会显示成用户名，
// 用户没法表达「我要清空昵称」，也分不清自己到底设过没有。
func TestUpdateMeClearNicknameRoundTrip(t *testing.T) {
	db := testutil.NewDB(t)
	u := testutil.SeedUser(t, db, "heidi")
	r := newUserRouter(NewUserService(db), u.ID)

	// 先设一个昵称，确认 has_custom_nickname 变成 true。
	_, env := do(t, r, http.MethodPut, "/api/users/me", `{"nickname":"海蒂"}`)
	var set struct {
		Nickname          string `json:"nickname"`
		HasCustomNickname bool   `json:"has_custom_nickname"`
	}
	if err := json.Unmarshal(env.Data, &set); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if set.Nickname != "海蒂" || !set.HasCustomNickname {
		t.Fatalf("设置后 = %+v，期望 nickname=海蒂 且 has_custom_nickname=true", set)
	}

	// 再传空串清空：展示名回退成用户名，但「设过没有」必须回到 false。
	_, env = do(t, r, http.MethodPut, "/api/users/me", `{"nickname":""}`)
	var cleared struct {
		Nickname          string `json:"nickname"`
		HasCustomNickname bool   `json:"has_custom_nickname"`
	}
	if err := json.Unmarshal(env.Data, &cleared); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if cleared.Nickname != "heidi" {
		t.Errorf("清空后 nickname = %q，期望回退成 heidi", cleared.Nickname)
	}
	if cleared.HasCustomNickname {
		t.Error("清空后 has_custom_nickname 应为 false")
	}
}
