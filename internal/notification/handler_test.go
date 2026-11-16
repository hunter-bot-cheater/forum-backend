package notification

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/model"
)

// TestMain 把 gin 切到测试模式，避免每条请求的调试日志淹没断言输出。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// envelope 是统一响应格式的测试侧视图，只取断言会用到的字段。
type envelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

// notifyItem 是列表响应里一条通知的影子结构，只声明本文件要检查的字段。
// Actor 用 map 而不是结构体：要区分「字段不存在」和「字段是空对象」，
// 结构体解出来两者都是零值，会把 omitempty 失效的 bug 放过去。
type notifyItem struct {
	ID      uint                   `json:"id"`
	ActorID uint                   `json:"actor_id"`
	Actor   map[string]interface{} `json:"actor"`
}

// notifyListData 是 data 部分的影子结构。
type notifyListData struct {
	List        []notifyItem `json:"list"`
	Total       int64        `json:"total"`
	UnreadCount int64        `json:"unread_count"`
	Page        int          `json:"page"`
	PageSize    int          `json:"page_size"`
	Kind        string       `json:"kind"`
}

// newNotifyRouter 搭一个最小引擎：一条匿名路由验证 401，两条带登录态的路由走真实 handler。
// 不套 JWTAuth 是因为鉴权中间件自己有测试，这里只想测 handler 的分支。
func newNotifyRouter(svc *NotificationService, loginAs uint) *gin.Engine {
	h := NewNotificationHandler(svc)
	r := gin.New()

	r.GET("/api/anonymous/notifications", h.List)

	auth := r.Group("/api")
	auth.Use(func(c *gin.Context) {
		c.Set("userID", loginAs)
		c.Next()
	})
	auth.GET("/notifications", h.List)
	auth.GET("/notifications/summary", h.Summary)
	auth.PUT("/notifications/read-all", h.ReadAll)
	// 这条和上面的静态段 read-all 同方法同层，注册时不 panic 本身就是一条断言：
	// gin 的静态优先规则必须能把 /notifications/read-all 与 /notifications/:id/read 分开。
	auth.PUT("/notifications/:id/read", h.MarkRead)
	return r
}

// do 发一次请求并解出统一响应。
func do(t *testing.T, r *gin.Engine, method, path string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var env envelope
	if body := rec.Body.Bytes(); len(body) > 0 {
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("响应不是合法 JSON: %v，原始内容 %s", err, body)
		}
	}
	return rec, env
}

// TestListIncludesActor 验证响应里带上了可渲染的昵称，前端不必再按 id 查一次。
func TestListIncludesActor(t *testing.T) {
	f := newNotifyFixture(t)

	if err := f.db.Model(f.bob).Update("nickname", "鲍勃").Error; err != nil {
		t.Fatalf("更新 bob 昵称失败: %v", err)
	}
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-actor", "like.created", 9, false)

	r := newNotifyRouter(f.svc, f.owner.ID)
	rec, env := do(t, r, http.MethodGet, "/api/notifications")

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码期望 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	var data notifyListData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if len(data.List) != 1 {
		t.Fatalf("列表长度期望 1，实际 %d", len(data.List))
	}

	actor := data.List[0].Actor
	if actor == nil {
		t.Fatal("响应缺少 actor 字段，前端无法显示「谁赞了你」")
	}
	if actor["nickname"] != "鲍勃" {
		t.Fatalf("actor.nickname 期望 鲍勃，实际 %v", actor["nickname"])
	}
	if actor["username"] != "bob" {
		t.Fatalf("actor.username 期望 bob，实际 %v", actor["username"])
	}
}

// TestListOmitsMissingActor 验证触发者账号不存在时整个 actor 字段被省掉。
// 回一个 {id:0, nickname:""} 的空对象会让前端显示成空白名字，比没有更难排查。
func TestListOmitsMissingActor(t *testing.T) {
	f := newNotifyFixture(t)

	seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-no-actor", "like.created", 9, false)
	if err := f.db.Delete(&model.User{}, f.alice.ID).Error; err != nil {
		t.Fatalf("删除 alice 失败: %v", err)
	}

	r := newNotifyRouter(f.svc, f.owner.ID)
	rec, env := do(t, r, http.MethodGet, "/api/notifications")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码期望 200，实际 %d", rec.Code)
	}

	var data notifyListData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if len(data.List) != 1 {
		t.Fatalf("列表长度期望 1，实际 %d", len(data.List))
	}
	if data.List[0].Actor != nil {
		t.Fatalf("actor 缺失时不该输出该字段，实际 %v", data.List[0].Actor)
	}
	// actor_id 要保留：它是稳定标识，前端跳主页时用得上。
	if data.List[0].ActorID != f.alice.ID {
		t.Fatalf("actor_id 期望 %d，实际 %d", f.alice.ID, data.List[0].ActorID)
	}
}

// TestListRequiresLogin 验证匿名请求被挡住。
func TestListRequiresLogin(t *testing.T) {
	f := newNotifyFixture(t)
	r := newNotifyRouter(f.svc, 0)

	rec, _ := do(t, r, http.MethodGet, "/api/anonymous/notifications")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("匿名请求状态码期望 401，实际 %d", rec.Code)
	}
}

// TestListInvalidPage 验证非法分页参数走统一错误映射，返回 400 而不是 500。
func TestListInvalidPage(t *testing.T) {
	f := newNotifyFixture(t)
	r := newNotifyRouter(f.svc, f.owner.ID)

	rec, _ := do(t, r, http.MethodGet, "/api/notifications?page_size=999")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码期望 400，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
}

// TestReadAllReturnsAffected 验证全部已读返回受影响行数，前端用它决定要不要提示。
func TestReadAllReturnsAffected(t *testing.T) {
	f := newNotifyFixture(t)

	seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-ra-1", "like.created", 1, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-ra-2", "comment.created", 2, false)

	r := newNotifyRouter(f.svc, f.owner.ID)
	rec, env := do(t, r, http.MethodPut, "/api/notifications/read-all")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码期望 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	var data struct {
		Affected int64 `json:"affected"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if data.Affected != 2 {
		t.Fatalf("affected 期望 2，实际 %d", data.Affected)
	}
}

// TestListPassesKindThrough 验证 kind 参数真的透传到查询，并且响应里回显分类。
// 回显很关键：前端连续切 tab 时靠它判断「这一页属于哪个 tab」。
func TestListPassesKindThrough(t *testing.T) {
	f := newNotifyFixture(t)

	seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-k-like", "like.created", 1, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-k-comment", "comment.created", 2, false)

	r := newNotifyRouter(f.svc, f.owner.ID)
	rec, env := do(t, r, http.MethodGet, "/api/notifications?kind=comment")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码期望 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	var data notifyListData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if data.Kind != "comment" {
		t.Fatalf("响应应回显 kind=comment，实际 %q", data.Kind)
	}
	if len(data.List) != 1 || data.Total != 1 {
		t.Fatalf("comment 分类期望 1 条，实际 %d 条 / total %d", len(data.List), data.Total)
	}
}

// TestListUnknownKindBadRequest 验证未知分类返回 400，不是静默返回全部。
func TestListUnknownKindBadRequest(t *testing.T) {
	f := newNotifyFixture(t)
	r := newNotifyRouter(f.svc, f.owner.ID)

	rec, _ := do(t, r, http.MethodGet, "/api/notifications?kind=wat")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知分类期望 400，实际 %d", rec.Code)
	}
}

// TestSummaryByKind 验证分类未读接口的形状：by_kind 三类齐全。
func TestSummaryByKind(t *testing.T) {
	f := newNotifyFixture(t)

	seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-s-like", "like.created", 1, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-s-comment", "comment.created", 2, false)

	r := newNotifyRouter(f.svc, f.owner.ID)
	rec, env := do(t, r, http.MethodGet, "/api/notifications/summary")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码期望 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	var data struct {
		Total  int64            `json:"total"`
		ByKind map[string]int64 `json:"by_kind"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if data.Total != 2 {
		t.Fatalf("total 期望 2，实际 %d", data.Total)
	}
	if data.ByKind["like"] != 1 || data.ByKind["comment"] != 1 {
		t.Fatalf("分类计数不对: %v", data.ByKind)
	}
	if _, ok := data.ByKind["follow"]; !ok {
		t.Fatalf("by_kind 应包含全部三类，实际 %v", data.ByKind)
	}
}

// TestHandlerMarkRead 验证单条已读接口：属于自己就 200，并且只动这一条。
func TestHandlerMarkRead(t *testing.T) {
	f := newNotifyFixture(t)

	target := seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-h-read-1", "like.created", 1, false)
	other := seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-h-read-2", "like.created", 1, false)

	r := newNotifyRouter(f.svc, f.owner.ID)
	rec, env := do(t, r, http.MethodPut, fmt.Sprintf("/api/notifications/%d/read", target.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码期望 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	var data struct {
		Affected int64 `json:"affected"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if data.Affected != 1 {
		t.Fatalf("affected 期望 1，实际 %d", data.Affected)
	}

	// 同一用户另一条通知必须还是未读：只清被点开的那一条。
	var rest model.Notification
	if err := f.db.First(&rest, other.ID).Error; err != nil {
		t.Fatalf("查另一条通知失败: %v", err)
	}
	if rest.IsRead {
		t.Fatal("只点了第一条，第二条不该被一起标成已读")
	}
}

// TestHandlerMarkReadNotFound 验证别人的通知返回 404，而不是越权改掉。
func TestHandlerMarkReadNotFound(t *testing.T) {
	f := newNotifyFixture(t)

	foreign := seedNotification(t, f.db, f.bob.ID, f.alice.ID, "evt-h-foreign", "like.created", 1, false)

	r := newNotifyRouter(f.svc, f.owner.ID)
	rec, _ := do(t, r, http.MethodPut, fmt.Sprintf("/api/notifications/%d/read", foreign.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("别人的通知期望 404，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	// 幂等性也要守住：一次失败的越权尝试不能把对方的数据改掉。
	var got model.Notification
	if err := f.db.First(&got, foreign.ID).Error; err != nil {
		t.Fatalf("查对方的通知失败: %v", err)
	}
	if got.IsRead {
		t.Fatal("越权请求不该改到别人的通知")
	}
}

// TestHandlerMarkReadBadID 验证 id 不是数字时返回 400，不落到 500。
func TestHandlerMarkReadBadID(t *testing.T) {
	f := newNotifyFixture(t)

	r := newNotifyRouter(f.svc, f.owner.ID)
	rec, _ := do(t, r, http.MethodPut, "/api/notifications/abc/read")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 id 期望 400，实际 %d", rec.Code)
	}
}
