package article

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	ViewCount int64 `json:"view_count"`
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	author := testutil.SeedUser(t, db, "articleowner")
	other := testutil.SeedUser(t, db, "articleintruder")
	post := testutil.SeedArticle(t, db, author.ID, "别人改不动的文章")

	h := NewArticleHandler(svc, &fakeLiked{})
	w := doUpdate(t, h, post.ID, other.ID, `{"title":"篡改","content":"篡改"}`, true)

	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404，body=%s", w.Code, w.Body.String())
	}
}

// listData 是列表响应里本文件关心的字段。
type listData struct {
	Total int `json:"total"`
	List  []struct {
		Title string `json:"title"`
	} `json:"list"`
}

// doList 发一次列表请求，返回状态码与解出的 data。
// query 是完整的查询串（不带 ?），中文等字符由调用方用 url.QueryEscape 编码。
func doList(t *testing.T, h *ArticleHandler, query string) (int, listData) {
	t.Helper()

	r := gin.New()
	r.GET("/api/articles", h.List)

	path := "/api/articles"
	if query != "" {
		path += "?" + query
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

	var env struct {
		Data listData `json:"data"`
	}
	// 400 时 data 是空的，解析失败不算问题，只看状态码。
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w.Code, env.Data
}

// TestListSearchFiltersByKeyword 验证关键词从 query 传到 service 并真的筛掉不匹配的文章。
//
// 这里不复刻 service 的全部搜索规则（正文命中、元字符转义在 service_test 里测），
// 只确认 handler 没有把 keyword 丢掉、也没有把 total 与列表搞成两套口径。
func TestListSearchFiltersByKeyword(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	author := testutil.SeedUser(t, db, "searchuser")
	testutil.SeedArticle(t, db, author.ID, "gin 的路由分组")
	testutil.SeedArticle(t, db, author.ID, "gorm 的预加载")
	h := NewArticleHandler(svc, &fakeLiked{})

	code, data := doList(t, h, "keyword="+url.QueryEscape("gin"))
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", code)
	}
	if data.Total != 1 || len(data.List) != 1 {
		t.Fatalf("total=%d len=%d，期望 1/1（只有标题含 gin 的那篇）", data.Total, len(data.List))
	}
	if data.List[0].Title != "gin 的路由分组" {
		t.Errorf("命中 %q，期望「gin 的路由分组」", data.List[0].Title)
	}

	// 不带 keyword 时是全站，用来确认上面那 1 篇确实是被筛出来的，
	// 而不是「handler 一直只返回第一篇」这类假象。
	code, data = doList(t, h, "")
	if code != http.StatusOK {
		t.Fatalf("全站列表状态码 = %d，期望 200", code)
	}
	if data.Total != 2 {
		t.Errorf("全站 total=%d，期望 2", data.Total)
	}
}

// TestListSearchRejectsLongKeyword 验证超长关键词在 handler 边界被挡成 400。
//
// 不截断也不忽略：截断会返回与输入不符的结果，忽略等于把一次全表扫
// 伪装成「没搜」，用户无从判断是没结果还是参数错了。
func TestListSearchRejectsLongKeyword(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	h := NewArticleHandler(svc, &fakeLiked{})

	// 51 个汉字：上限 50 是按字符算的，用 len() 判断时这里会被误判成 153。
	long := strings.Repeat("字", maxKeywordRunes+1)
	code, _ := doList(t, h, "keyword="+url.QueryEscape(long))
	if code != http.StatusBadRequest {
		t.Errorf("超长关键词状态码 = %d，期望 400", code)
	}

	// 正好 50 个字要放行，避免上限写成了 < 而不是 <=。
	ok := strings.Repeat("字", maxKeywordRunes)
	if code, _ := doList(t, h, "keyword="+url.QueryEscape(ok)); code != http.StatusOK {
		t.Errorf("刚好到上限的关键词状态码 = %d，期望 200", code)
	}
}

// TestListBlankKeywordIsIgnored 验证只有空格的关键词等同于没搜。
//
// 拼 URL 时很容易带出一个 "keyword=" 或 "keyword=%20"，把它当成真实条件
// 会让用户看到一个莫名其妙被筛过的列表。
func TestListBlankKeywordIsIgnored(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	author := testutil.SeedUser(t, db, "blankkwuser")
	testutil.SeedArticle(t, db, author.ID, "第一篇")
	testutil.SeedArticle(t, db, author.ID, "第二篇")
	h := NewArticleHandler(svc, &fakeLiked{})

	code, data := doList(t, h, "keyword=%20%20")
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", code)
	}
	if data.Total != 2 {
		t.Errorf("空白关键词 total=%d，期望 2（等同于不筛选）", data.Total)
	}
}

// TestDetailCountsViewOncePerViewer 验证详情接口计浏览数：同一个人只算一次，
// 且响应里的数字在重复访问时不会变小。
//
// 后半条是最容易做错的：浏览数存在详情缓存里时，第一次回源读到 0 再补 1，
// 第二次命中缓存又会读出 0。用户刷新一下看到数字从 1 变 0，只会认为这功能坏了。
// 现在的实现每次都用库里的最新值覆盖响应，所以两次都必须是 1。
func TestDetailCountsViewOncePerViewer(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	author := testutil.SeedUser(t, db, "author")
	post := testutil.SeedArticle(t, db, author.ID, "浏览数会被累加的文章")

	h := NewArticleHandler(svc, &fakeLiked{})

	// 第一次访问：文章由 cache miss 回源，然后计入一次浏览。
	if data := doDetail(t, h, post.ID, true); data.ViewCount != 1 {
		t.Fatalf("首次访问 view_count = %d，期望 1", data.ViewCount)
	}

	// 第二次同一个身份：去重生效，不计数；同时详情缓存命中，
	// 响应仍要显示库里的 1，不能回退成缓存快照里的 0。
	if data := doDetail(t, h, post.ID, true); data.ViewCount != 1 {
		t.Errorf("同一访问者第二次 view_count = %d，期望仍是 1（不能变小）", data.ViewCount)
	}

	// 游客是另一个访问者（按 IP 去重），浏览数应当变成 2。
	if data := doDetail(t, h, post.ID, false); data.ViewCount != 2 {
		t.Errorf("换一个访问者后 view_count = %d，期望 2", data.ViewCount)
	}
}
