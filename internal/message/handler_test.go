package message

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

// newMsgRouter 搭一个最小引擎，只挂私信的五个接口。
// 登录态用 asUser 模拟，不套真实 JWTAuth：这里测的是 handler 自己的分支，
// 鉴权中间件另有自己的测试。
func newMsgRouter(svc *Service, loginAs uint) *gin.Engine {
	h := NewHandler(svc)
	r := gin.New()

	// 匿名路由用来验证「取不到登录态必须 401」。
	r.GET("/api/anonymous/conversations", h.Conversations)

	auth := r.Group("/api", asUser(loginAs))
	auth.GET("/conversations", h.Conversations)
	auth.GET("/conversations/unread", h.Unread)
	auth.GET("/conversations/with/:user_id/messages", h.Messages)
	auth.POST("/conversations/with/:user_id/messages", h.Send)
	auth.PUT("/conversations/with/:user_id/read", h.MarkRead)
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
		t.Fatalf("响应不是合法 JSON: %v，原文 %s", err, w.Body.String())
	}
	return w, env
}

// TestSendEndpointReturnsMine 验证发送接口回的是消息本体，且 mine=true。
//
// mine 由服务端算：前端拿到就能直接把气泡画到右边，
// 不用再拿自己的 id 比一遍（比错了消息会全部靠左，很难看出来）。
func TestSendEndpointReturnsMine(t *testing.T) {
	f := newMsgFixture(t)
	r := newMsgRouter(f.svc, f.alice.ID)

	w, env := do(t, r, http.MethodPost, "/api/conversations/with/"+itoa(int(f.bob.ID))+"/messages",
		`{"content":"你好 bob"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200, body=%s", w.Code, w.Body.String())
	}

	var got struct {
		ID      uint   `json:"id"`
		Content string `json:"content"`
		Mine    bool   `json:"mine"`
	}
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if got.Content != "你好 bob" || !got.Mine || got.ID == 0 {
		t.Errorf("返回 = %+v，期望内容一致且 mine=true", got)
	}
}

// TestSendEndpointIdempotent 验证 HTTP 层能把 client_msg_id 透到 service 并去重。
//
// 只测 service 不够：字段名写错（比如 client_msg_id 绑到别的 tag）时
// 请求照样 200，只是永远不去重，测试里必须从接口这一层走一遍。
func TestSendEndpointIdempotent(t *testing.T) {
	f := newMsgFixture(t)
	r := newMsgRouter(f.svc, f.alice.ID)
	path := "/api/conversations/with/" + itoa(int(f.bob.ID)) + "/messages"
	body := `{"content":"在吗","client_msg_id":"c-1"}`

	_, first := do(t, r, http.MethodPost, path, body)
	_, second := do(t, r, http.MethodPost, path, body)

	var a, b struct {
		ID          uint   `json:"id"`
		ClientMsgID string `json:"client_msg_id"`
	}
	if err := json.Unmarshal(first.Data, &a); err != nil {
		t.Fatalf("解析第一次响应失败: %v", err)
	}
	if err := json.Unmarshal(second.Data, &b); err != nil {
		t.Fatalf("解析第二次响应失败: %v", err)
	}
	if a.ID == 0 || a.ID != b.ID {
		t.Errorf("两次 id = %d / %d，期望相同且非 0", a.ID, b.ID)
	}
	// 回显幂等键，前端据此把乐观渲染的灰气泡替换成服务端这条。
	if a.ClientMsgID != "c-1" {
		t.Errorf("client_msg_id = %q，期望回显 c-1", a.ClientMsgID)
	}

	// 不带幂等键的请求不输出这个字段，免得前端拿到空串还要判一次。
	_, plain := do(t, r, http.MethodPost, path, `{"content":"另一条"}`)
	if strings.Contains(string(plain.Data), "client_msg_id") {
		t.Errorf("没传幂等键时不该输出 client_msg_id：%s", plain.Data)
	}
}

// TestAnonymousGets401 验证没登录态时统一回 401。
func TestAnonymousGets401(t *testing.T) {
	f := newMsgFixture(t)
	r := newMsgRouter(f.svc, f.alice.ID)

	w, env := do(t, r, http.MethodGet, "/api/anonymous/conversations", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d，期望 401", w.Code)
	}
	if env.ErrorCode == "" {
		t.Error("401 应当带 error_code")
	}
}

// TestBadPeerID 验证路径里的 user_id 不是正整数时回 400。
//
// 不校验的话 0 会被当成合法 id 一路带到 SQL，
// 结果是「查询正常、没有任何会话」，看起来像丢数据。
func TestBadPeerID(t *testing.T) {
	f := newMsgFixture(t)
	r := newMsgRouter(f.svc, f.alice.ID)

	for _, bad := range []string{"abc", "0", "-1", "1.5"} {
		w, _ := do(t, r, http.MethodGet, "/api/conversations/with/"+bad+"/messages", "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("user_id=%q：status = %d，期望 400", bad, w.Code)
		}
	}
}

// TestConversationsEndpoint 验证列表响应带上对方资料与未读数。
func TestConversationsEndpoint(t *testing.T) {
	f := newMsgFixture(t)
	r := newMsgRouter(f.svc, f.alice.ID)

	if _, err := f.svc.Send(t.Context(), f.alice.ID, f.bob.ID, "第一条", ""); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if _, err := f.svc.Send(t.Context(), f.bob.ID, f.alice.ID, "回复", ""); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	w, env := do(t, r, http.MethodGet, "/api/conversations", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200", w.Code)
	}

	var got struct {
		List []struct {
			Peer struct {
				ID       uint   `json:"id"`
				Username string `json:"username"`
				Nickname string `json:"nickname"`
			} `json:"peer"`
			LastMessage *struct {
				Content string `json:"content"`
				Mine    bool   `json:"mine"`
			} `json:"last_message"`
			Unread int `json:"unread"`
		} `json:"list"`
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if got.Total != 1 || len(got.List) != 1 {
		t.Fatalf("total=%d len=%d，期望都是 1", got.Total, len(got.List))
	}

	row := got.List[0]
	if row.Peer.ID != f.bob.ID || row.Peer.Username != "bob" {
		t.Errorf("peer = %+v，期望 bob", row.Peer)
	}
	// 昵称没设过，必须回退成用户名，前端才有东西可画。
	if row.Peer.Nickname != "bob" {
		t.Errorf("peer.nickname = %q，期望回退成 bob", row.Peer.Nickname)
	}
	// bob 回的那条是别人发的，对 alice 来说 mine=false。
	if row.LastMessage == nil || row.LastMessage.Content != "回复" {
		t.Errorf("last_message = %+v，期望「回复」", row.LastMessage)
	} else if row.LastMessage.Mine {
		t.Error("bob 发的消息对 alice 来说 mine 应为 false")
	}
	if row.Unread != 1 {
		t.Errorf("unread = %d，期望 1", row.Unread)
	}
}

// TestUnreadEndpoint 验证未读角标接口的形状与数值。
func TestUnreadEndpoint(t *testing.T) {
	f := newMsgFixture(t)
	r := newMsgRouter(f.svc, f.bob.ID)

	if _, err := f.svc.Send(t.Context(), f.alice.ID, f.bob.ID, "hi", ""); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	w, env := do(t, r, http.MethodGet, "/api/conversations/unread", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200", w.Code)
	}

	var got struct {
		Unread int64 `json:"unread"`
	}
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if got.Unread != 1 {
		t.Errorf("unread = %d，期望 1", got.Unread)
	}
}

// TestMarkReadEndpoint 验证已读接口返回清掉的条数。
func TestMarkReadEndpoint(t *testing.T) {
	f := newMsgFixture(t)
	r := newMsgRouter(f.svc, f.bob.ID)

	for i := 0; i < 3; i++ {
		if _, err := f.svc.Send(t.Context(), f.alice.ID, f.bob.ID, "hi", ""); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}

	w, env := do(t, r, http.MethodPut, "/api/conversations/with/"+itoa(int(f.alice.ID))+"/read", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200, body=%s", w.Code, w.Body.String())
	}

	var got struct {
		Cleared int64 `json:"cleared"`
	}
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if got.Cleared != 3 {
		t.Errorf("cleared = %d，期望 3", got.Cleared)
	}
}

// TestMessagesEndpointCursor 验证消息列表接口的游标翻页与响应形状。
func TestMessagesEndpointCursor(t *testing.T) {
	f := newMsgFixture(t)
	r := newMsgRouter(f.svc, f.alice.ID)
	path := "/api/conversations/with/" + strconv.FormatUint(uint64(f.bob.ID), 10) + "/messages"

	for i := 1; i <= 3; i++ {
		if _, err := f.svc.Send(t.Context(), f.alice.ID, f.bob.ID, "第"+itoa(i)+"条", ""); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}

	w, env := do(t, r, http.MethodGet, path+"?limit=2", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200", w.Code)
	}

	var got struct {
		List []struct {
			ID      uint   `json:"id"`
			Content string `json:"content"`
		} `json:"list"`
		HasMore      bool `json:"has_more"`
		NextBeforeID uint `json:"next_before_id"`
		Peer         struct {
			ID       uint   `json:"id"`
			Username string `json:"username"`
		} `json:"peer"`
	}
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if len(got.List) != 2 || got.List[0].Content != "第2条" {
		t.Errorf("第一页 = %+v，期望最新两条且正序", got.List)
	}
	if !got.HasMore {
		t.Error("还有第 1 条，has_more 应当为 true")
	}
	if got.NextBeforeID != got.List[0].ID {
		t.Errorf("next_before_id = %d，期望等于本页最早一条 %d", got.NextBeforeID, got.List[0].ID)
	}
	// 聊天页顶栏靠这个字段画头像和昵称，缺了前端还得再请求一次用户详情。
	if got.Peer.ID != f.bob.ID || got.Peer.Username != "bob" {
		t.Errorf("peer = %+v，期望 bob", got.Peer)
	}

	// 拿游标继续往上翻，应当拿到第 1 条，并且没有更多了。
	w, env = do(t, r, http.MethodGet, path+"?limit=2&before_id="+itoa(int(got.NextBeforeID)), "")
	if w.Code != http.StatusOK {
		t.Fatalf("第二页 status = %d，期望 200", w.Code)
	}
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if len(got.List) != 1 || got.List[0].Content != "第1条" {
		t.Errorf("第二页 = %+v，期望只剩第1条", got.List)
	}
	if got.HasMore {
		t.Error("已经翻到头，has_more 应当为 false")
	}

	// 非法参数交给 ParseCursor 挡，回 400 而不是 500。
	for _, q := range []string{"?limit=999", "?limit=0", "?before_id=-1", "?before_id=abc"} {
		w, _ = do(t, r, http.MethodGet, path+q, "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s：status = %d，期望 400", q, w.Code)
		}
	}
}

// TestSendUnknownPeer404 验证给不存在的人发消息回 404 而不是 500。
func TestSendUnknownPeer404(t *testing.T) {
	f := newMsgFixture(t)
	r := newMsgRouter(f.svc, f.alice.ID)

	w, env := do(t, r, http.MethodPost, "/api/conversations/with/999999/messages", `{"content":"在吗"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d，期望 404, body=%s", w.Code, w.Body.String())
	}
	if env.ErrorCode != "not_found" {
		t.Errorf("error_code = %q，期望 not_found", env.ErrorCode)
	}
}

// TestEnterEmptyChatWithStranger 验证「从主页点私信、进入和陌生人的聊天页」这条路。
//
// 这正是抖音那种交互：在某人主页点私信，直接进到和他的专属聊天界面，
// 这时两人还没聊过、会话还不存在。接口必须回 200 + 空列表，
// 并且带上对方资料让顶栏能画出来。回 404 或 500 都会让用户看到一个错误页，
// 而他只是想开口说第一句话。
func TestEnterEmptyChatWithStranger(t *testing.T) {
	f := newMsgFixture(t)
	r := newMsgRouter(f.svc, f.alice.ID)

	w, env := do(t, r, http.MethodGet,
		"/api/conversations/with/"+strconv.FormatUint(uint64(f.carol.ID), 10)+"/messages", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200, body=%s", w.Code, w.Body.String())
	}

	var got struct {
		List         []messageResp `json:"list"`
		HasMore      bool          `json:"has_more"`
		NextBeforeID uint          `json:"next_before_id"`
		Peer         struct {
			ID       uint   `json:"id"`
			Username string `json:"username"`
			Nickname string `json:"nickname"`
		} `json:"peer"`
	}
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	if len(got.List) != 0 || got.HasMore || got.NextBeforeID != 0 {
		t.Errorf("len=%d has_more=%v next=%d，期望全是空值",
			len(got.List), got.HasMore, got.NextBeforeID)
	}
	if got.Peer.ID != f.carol.ID || got.Peer.Username != "carol" {
		t.Errorf("peer = %+v，期望 carol（空聊天页也要能显示对方）", got.Peer)
	}
	// 没设昵称时必须回退成用户名，否则顶栏是一片空白。
	if got.Peer.Nickname != "carol" {
		t.Errorf("peer.nickname = %q，期望回退成 carol", got.Peer.Nickname)
	}

	// 空列表要是 [] 而不是 null：前端直接 map 的话 null 会抛错。
	if strings.Contains(string(env.Data), `"list":null`) {
		t.Error("list 应当是空数组而不是 null")
	}
}
