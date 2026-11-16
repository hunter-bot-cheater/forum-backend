package article

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

// TestMain 统一把 gin 切到测试模式，否则每个请求都会打调试日志。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// fakeLiked 是可编程的 LikedChecker。
// 用它而不是真的 like.Service：这条用例要验证的是「详情按访问者回填 liked」
// 以及「查询失败时详情不跟着失败」，与点赞表的读写无关。
type fakeLiked struct {
	liked bool
	err   error
	// calls 记录被调用次数，用来确认游客路径根本没有去查。
	calls int
}

func (f *fakeLiked) HasLiked(ctx context.Context, articleID, userID uint) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.liked, nil
}

// asUser 模拟 OptionalJWTAuth 解析成功后的产物：把用户 id 写进 gin.Context。
func asUser(id uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("userID", id)
		c.Next()
	}
}

// detailData 是详情响应里本文件关心的字段。
type detailData struct {
	ID        uint  `json:"id"`
	LikeCount int64 `json:"like_count"`
	Liked     bool  `json:"liked"`
}

// doDetail 发一次详情请求并解出 data。
// loggedIn 为 false 时不挂 asUser，模拟游客。
func doDetail(t *testing.T, h *ArticleHandler, articleID uint, loggedIn bool) detailData {
	t.Helper()

	r := gin.New()
	if loggedIn {
		r.GET("/api/articles/:id", asUser(1), h.Detail)
	} else {
		r.GET("/api/articles/:id", h.Detail)
	}

	path := "/api/articles/" + strconv.FormatUint(uint64(articleID), 10)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法 JSON: %v，body=%s", err, w.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("请求失败: code=%d body=%s", env.Code, w.Body.String())
	}

	var data detailData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data 解析失败: %v", err)
	}
	return data
}

// TestDetailFillsLikedForLoggedInUser 验证登录用户的详情会带出真实的点赞状态。
//
// 详情页的点赞按钮靠这个字段决定要不要点亮。没有它，用户点过赞之后刷新页面
// 按钮还是灰的，再点一次会被后端判成「重复点赞」——看起来像点了没反应。
func TestDetailFillsLikedForLoggedInUser(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "author")
	post := testutil.SeedArticle(t, db, author.ID, "点过赞的文章")

	liked := &fakeLiked{liked: true}
	h := NewArticleHandler(svc, liked)

	data := doDetail(t, h, post.ID, true)
	if !data.Liked {
		t.Error("登录用户点过赞，liked 应当是 true")
	}
	if liked.calls != 1 {
		t.Errorf("likedChecker 被调用 %d 次，期望 1 次", liked.calls)
	}
}

// TestDetailGuestSkipsLikedCheck 验证游客路径完全不去查点赞集合。
//
// 没登录就没有身份可查，去查只会拿 userID=0 打一次 Redis + 一次 MySQL，
// 而结果必然是 false。这条用例顺带守住「游客能看详情」——公开内容不能因为
// 没有登录态就打不开。
func TestDetailGuestSkipsLikedCheck(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "author")
	post := testutil.SeedArticle(t, db, author.ID, "游客也能看的文章")

	liked := &fakeLiked{liked: true}
	h := NewArticleHandler(svc, liked)

	data := doDetail(t, h, post.ID, false)
	if data.Liked {
		t.Error("游客没有身份，liked 必须是 false")
	}
	if liked.calls != 0 {
		t.Errorf("游客不该触发点赞查询，实际被调用 %d 次", liked.calls)
	}
	if data.ID != post.ID {
		t.Errorf("正文 id = %d，期望 %d", data.ID, post.ID)
	}
}

// TestDetailSurvivesLikedCheckFailure 验证点赞状态查不出来时详情照常返回。
//
// 判据是 HTTP 200 且正文完整、liked 降级为 false。
// 如果这里跟着报错，一次 Redis 抖动就会让整篇文章打不开，代价与收益完全不对等。
func TestDetailSurvivesLikedCheckFailure(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "author")
	post := testutil.SeedArticle(t, db, author.ID, "点赞查询会失败的文章")

	h := NewArticleHandler(svc, &fakeLiked{err: errors.New("redis is down")})

	data := doDetail(t, h, post.ID, true)
	if data.Liked {
		t.Error("查询失败时应当降级成未点赞")
	}
	if data.ID != post.ID {
		t.Errorf("详情仍然要正常返回，实际 id = %d", data.ID)
	}
}

// doUpdate 发一次编辑请求，返回原始响应，由调用方按状态码分流断言。
// loggedIn 为 false 时不挂 asUser，模拟没带 token 的请求。
func doUpdate(t *testing.T, h *ArticleHandler, articleID, userID uint, body string, loggedIn bool) *httptest.ResponseRecorder {
	t.Helper()

	r := gin.New()
	if loggedIn {
		r.PUT("/api/articles/:id", asUser(userID), h.Update)
	} else {
		r.PUT("/api/articles/:id", h.Update)
	}

	path := "/api/articles/" + strconv.FormatUint(uint64(articleID), 10)
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestUpdateReturnsUpdatedDetail 验证编辑成功后回的是详情形状。
//
// 前端保存完要直接拿它替换页面上的文章，所以 content 必须是最新的，
// like_count 也必须在（少了它就退回成「先 PUT 再 GET」两次请求）。
func TestUpdateReturnsUpdatedDetail(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "updatehandler")
	post := testutil.SeedArticle(t, db, author.ID, "待修改的标题")

	h := NewArticleHandler(svc, &fakeLiked{})
	w := doUpdate(t, h, post.ID, author.ID, `{"title":"改后的标题","content":"改后的正文"}`, true)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			ID        uint   `json:"id"`
			Title     string `json:"title"`
			Content   string `json:"content"`
			LikeCount int64  `json:"like_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if env.Data.Title != "改后的标题" || env.Data.Content != "改后的正文" {
		t.Errorf("返回的内容没更新：title=%q content=%q", env.Data.Title, env.Data.Content)
	}
	if env.Data.ID != post.ID {
		t.Errorf("返回 id = %d，期望 %d", env.Data.ID, post.ID)
	}
}

// TestUpdateWithoutLoginIs401 验证没身份直接拒，不会落到 service 里用 0 号用户去改。
func TestUpdateWithoutLoginIs401(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "nologinuser")
	post := testutil.SeedArticle(t, db, author.ID, "不需要登录也能试的文章")

	h := NewArticleHandler(svc, &fakeLiked{})
	w := doUpdate(t, h, post.ID, 0, `{"title":"标题","content":"正文"}`, false)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401，body=%s", w.Code, w.Body.String())
	}
}

// TestUpdateRejectsBadRequest 覆盖三种 400：id 不是数字、body 不是 JSON、字段类型不对。
// 三条都必须在 handler 边界挡掉，不能带进 service。
func TestUpdateRejectsBadRequest(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "badrequestuser")
	post := testutil.SeedArticle(t, db, author.ID, "参数校验用的文章")
	h := NewArticleHandler(svc, &fakeLiked{})

	r := gin.New()
	r.PUT("/api/articles/:id", asUser(author.ID), h.Update)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/articles/abc", strings.NewReader(`{}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("非数字 id：状态码 = %d，期望 400", w.Code)
	}

	if got := doUpdate(t, h, post.ID, author.ID, `not-json`, true); got.Code != http.StatusBadRequest {
		t.Errorf("坏 JSON：状态码 = %d，期望 400", got.Code)
	}

	if got := doUpdate(t, h, post.ID, author.ID, `{"title":123,"content":"正文"}`, true); got.Code != http.StatusBadRequest {
		t.Errorf("字段类型不对：状态码 = %d，期望 400", got.Code)
	}
}

// TestUpdateOtherUserReturns404 验证改别人的文章回 404 而不是 403。
//
// 403 等于确认「这篇文章存在但不归你」，配合遍历 id 就能摸清站内有哪些文章。
// 与 Delete 的口径保持一致：两者对外都只说找不到。
func TestUpdateOtherUserReturns404(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "articleowner")
	other := testutil.SeedUser(t, db, "articleintruder")
	post := testutil.SeedArticle(t, db, author.ID, "别人改不动的文章")

	h := NewArticleHandler(svc, &fakeLiked{})
	w := doUpdate(t, h, post.ID, other.ID, `{"title":"篡改","content":"篡改"}`, true)

	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404，body=%s", w.Code, w.Body.String())
	}
}
