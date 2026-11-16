package comment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain 统一把 gin 切到测试模式。
// 不切的话每次请求都会打调试日志，把真正的失败信息淹掉。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// envelope 是本项目统一响应格式的测试侧视图。
// Data 用 json.RawMessage 接住，方便各用例再按自己的类型解一层。
type envelope struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	ErrorCode string          `json:"error_code"`
}

// newCommentRouter 搭一个只挂评论三条路由的最小 gin 引擎。
//
// 刻意不套 JWTAuth：这里要测的是 handler 自己的分支（参数解析、错误映射、响应形状），
// 「token 无效怎么办」属于中间件与路由的职责，留给路由级集成测试。
// 也因为不套真实中间件，asUser 直接用 c.Set 模拟它写进 context 的那个键。
func newCommentRouter(t *testing.T, f *commentFixture) *gin.Engine {
	t.Helper()

	handler := NewCommentHandler(f.svc)
	r := gin.New()
	r.GET("/api/articles/:id/comments", handler.List)
	r.POST("/api/articles/:id/comments", asUser(f.commenter.ID), handler.Create)
	// 匿名路由只用来验证「取不到用户时必须 401」，与上面的登录路由并存。
	r.POST("/api/anonymous/articles/:id/comments", handler.Create)
	r.DELETE("/api/comments/:id", asUser(f.commenter.ID), handler.Delete)
	return r
}

// asUser 模拟 JWTAuth 的产物：把用户 id 写进 gin.Context 的 userID 键。
func asUser(id uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("userID", id)
		c.Next()
	}
}

// doJSON 发一个请求并把响应解成 envelope。body 为空字符串时不带请求体。
func doJSON(t *testing.T, r *gin.Engine, method, path, body string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法 JSON（%d）: %s", w.Code, w.Body.String())
	}
	return w, env
}

// TestHandlerListReturnsPage 验证列表接口把 service 的结果包成约定形状：
// code 0、list 里带作者、total 与分页回显都正确。
func TestHandlerListReturnsPage(t *testing.T) {
	f := newCommentFixture(t)
	r := newCommentRouter(t, f)

	for _, content := range []string{"第一条", "第二条"} {
		if _, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, 0, content); err != nil {
			t.Fatalf("准备评论失败: %v", err)
		}
	}

	path := fmt.Sprintf("/api/articles/%d/comments?page=1&page_size=10", f.article.ID)
	w, env := doJSON(t, r, http.MethodGet, path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", w.Code)
	}
	if env.Code != 0 || env.Message != "ok" {
		t.Errorf("响应壳 = code %d / message %q", env.Code, env.Message)
	}

	var data commentListResp
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("解析 list 失败: %v", err)
	}
	if data.Total != 2 || len(data.List) != 2 {
		t.Fatalf("total = %d, len = %d，期望都是 2", data.Total, len(data.List))
	}
	if data.Page != 1 || data.PageSize != 10 {
		t.Errorf("分页回显 = %d/%d", data.Page, data.PageSize)
	}
	// 作者必须带出来，否则前端只能显示空白昵称。
	if data.List[0].Author == nil || data.List[0].Author.Username != "commenter" {
		t.Errorf("作者信息缺失: %+v", data.List[0].Author)
	}
}

// TestHandlerListEmptyIsArray 验证没有评论时 list 是 [] 而不是 null。
// 前端直接 .map 渲染列表，null 会直接抛错。
func TestHandlerListEmptyIsArray(t *testing.T) {
	f := newCommentFixture(t)
	r := newCommentRouter(t, f)

	path := fmt.Sprintf("/api/articles/%d/comments", f.article.ID)
	w, env := doJSON(t, r, http.MethodGet, path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", w.Code)
	}
	if !strings.Contains(string(env.Data), `"list":[]`) {
		t.Errorf("空列表应当输出 []，实际 %s", env.Data)
	}
}

// TestHandlerListRejectsBadID 验证路径参数不是数字时返回 400，不会把 "abc" 带进查询。
func TestHandlerListRejectsBadID(t *testing.T) {
	f := newCommentFixture(t)
	r := newCommentRouter(t, f)

	w, env := doJSON(t, r, http.MethodGet, "/api/articles/abc/comments", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", w.Code)
	}
	if env.Code != 1 {
		t.Errorf("失败响应 code 应当是 1，实际 %d", env.Code)
	}
}

// TestHandlerCreateRequiresLogin 验证没取到用户 id 时直接 401，
// 并且按 RFC 7235 带上 WWW-Authenticate。
func TestHandlerCreateRequiresLogin(t *testing.T) {
	f := newCommentFixture(t)
	r := newCommentRouter(t, f)

	w, env := doJSON(t, r, http.MethodPost, "/api/anonymous/articles/1/comments", `{"content":"未登录"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", w.Code)
	}
	if env.ErrorCode == "" {
		t.Error("401 响应应当带 error_code")
	}
	if got := w.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q，应包含 Bearer", got)
	}
}

// TestHandlerCreateRejectsBadJSON 验证请求体不是合法 JSON 时返回 400。
func TestHandlerCreateRejectsBadJSON(t *testing.T) {
	f := newCommentFixture(t)
	r := newCommentRouter(t, f)

	w, _ := doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/articles/%d/comments", f.article.ID), `{"content":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", w.Code)
	}
}

// TestHandlerCreateReturnsComment 验证发表成功的响应形状：
// 回显内容，但作者字段要省略（新建时没有 Preload，前端自己知道是谁发的）。
func TestHandlerCreateReturnsComment(t *testing.T) {
	f := newCommentFixture(t)
	r := newCommentRouter(t, f)

	path := fmt.Sprintf("/api/articles/%d/comments", f.article.ID)
	w, env := doJSON(t, r, http.MethodPost, path, `{"content":"  你好  "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应 %s", w.Code, w.Body.String())
	}

	var data commentResp
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("解析 comment 失败: %v", err)
	}
	if data.Content != "你好" {
		t.Errorf("内容 = %q，期望去掉前后空白后的「你好」", data.Content)
	}
	if data.Author != nil {
		t.Errorf("新建评论不应带作者字段，实际 %+v", data.Author)
	}
}

// TestHandlerDeleteOthersComment 验证删别人的评论对外是 404（不是 403），
// 避免通过状态码探测某条评论属于谁。
func TestHandlerDeleteOthersComment(t *testing.T) {
	f := newCommentFixture(t)
	handler := NewCommentHandler(f.svc)

	created, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, 0, "别人删不掉")
	if err != nil {
		t.Fatalf("准备评论失败: %v", err)
	}

	// 换一个「当前用户」重建路由：这次登录的是文章作者，不是评论作者。
	r := gin.New()
	r.DELETE("/api/comments/:id", asUser(f.author.ID), handler.Delete)

	w, env := doJSON(t, r, http.MethodDelete, fmt.Sprintf("/api/comments/%d", created.ID), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404，响应 %s", w.Code, w.Body.String())
	}
	if env.ErrorCode != "not_found" {
		t.Errorf("error_code = %q，期望 not_found", env.ErrorCode)
	}
}

// TestHandlerDeleteOwnComment 验证作者删除成功：200 且 data 为 null。
func TestHandlerDeleteOwnComment(t *testing.T) {
	f := newCommentFixture(t)
	r := newCommentRouter(t, f)

	created, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, 0, "会删掉我")
	if err != nil {
		t.Fatalf("准备评论失败: %v", err)
	}

	w, env := doJSON(t, r, http.MethodDelete, fmt.Sprintf("/api/comments/%d", created.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应 %s", w.Code, w.Body.String())
	}
	if string(env.Data) != "null" {
		t.Errorf("删除成功的 data 应为 null，实际 %s", env.Data)
	}
}

// TestHandlerCreateReply 验证带 parent_id 的请求被当成回复处理，响应里带回层级字段。
func TestHandlerCreateReply(t *testing.T) {
	f := newCommentFixture(t)
	r := newCommentRouter(t, f)

	root, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, 0, "根评论")
	if err != nil {
		t.Fatalf("准备根评论失败: %v", err)
	}

	body := fmt.Sprintf(`{"content":"回复内容","parent_id":%d}`, root.ID)
	w, env := doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/articles/%d/comments", f.article.ID), body)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应 %s", w.Code, w.Body.String())
	}

	var got commentResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if got.ParentID != root.ID || got.RootID != root.ID {
		t.Fatalf("回复的 parent/root 期望 %d/%d，实际 %d/%d", root.ID, root.ID, got.ParentID, got.RootID)
	}
}

// TestHandlerReplyToMissingParent 验证父评论不存在时返回 404，而不是 500。
func TestHandlerReplyToMissingParent(t *testing.T) {
	f := newCommentFixture(t)
	r := newCommentRouter(t, f)

	body := `{"content":"回复不存在的评论","parent_id":999999}`
	w, _ := doJSON(t, r, http.MethodPost, fmt.Sprintf("/api/articles/%d/comments", f.article.ID), body)
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404，响应 %s", w.Code, w.Body.String())
	}
}
