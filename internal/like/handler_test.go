package like

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/article"
	"forum-backend/internal/testutil"
)

// TestMain 统一把 gin 切到测试模式，否则每个请求都会打调试日志。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// asUser 模拟 JWTAuth 解析成功后的产物：把用户 id 写进 gin.Context。
func asUser(id uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("userID", id)
		c.Next()
	}
}

// doLike 发一次点赞或取消请求，返回状态码与解好的 data。
// hotSvc 传 nil：点赞与取消根本不碰热榜 service，热榜在 hot 包自己测。
func doLike(t *testing.T, h *LikeHandler, articleID, userID uint, method string, loggedIn bool) (int, likeResp) {
	t.Helper()

	r := gin.New()
	path := "/api/articles/:id/like"
	if loggedIn {
		r.Handle(method, path, asUser(userID), func(c *gin.Context) {
			if method == http.MethodPut {
				h.Like(c)
				return
			}
			h.Unlike(c)
		})
	} else {
		r.Handle(method, path, func(c *gin.Context) {
			if method == http.MethodPut {
				h.Like(c)
				return
			}
			h.Unlike(c)
		})
	}

	url := "/api/articles/" + strconv.FormatUint(uint64(articleID), 10) + "/like"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, url, nil))

	var env struct {
		Code int      `json:"code"`
		Data likeResp `json:"data"`
	}
	// 非 2xx 时 data 是 null，反序列化会失败，这时只关心状态码，交给调用方断言。
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil && w.Code == http.StatusOK {
		t.Fatalf("响应不是合法 JSON: %v，body=%s", err, w.Body.String())
	}
	return w.Code, env.Data
}

// TestLikeEndpointReturnsState 验证点赞接口回的是操作后的真实状态。
//
// 客户端拿 liked 点亮按钮、拿 like_count 改数字，两个字段都不能靠本地推算：
// 并发下本地加减会越差越远。
func TestLikeEndpointReturnsState(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	articleSvc := article.NewArticleService(db, rdb)
	h := NewLikeHandler(NewLikeService(db, rdb, articleSvc), nil)

	author := testutil.SeedUser(t, db, "httplikeauthor")
	reader := testutil.SeedUser(t, db, "httplikereader")
	post := testutil.SeedArticle(t, db, author.ID, "接口点赞的文章")

	code, data := doLike(t, h, post.ID, reader.ID, http.MethodPut, true)
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", code)
	}
	if !data.Changed || !data.Liked {
		t.Errorf("changed=%v liked=%v，期望 true/true", data.Changed, data.Liked)
	}
	if data.Count != 1 {
		t.Errorf("like_count = %d，期望 1", data.Count)
	}
	if data.ArticleID != post.ID {
		t.Errorf("article_id = %d，期望 %d", data.ArticleID, post.ID)
	}

	// 再取消一次，状态要反过来。
	code, data = doLike(t, h, post.ID, reader.ID, http.MethodDelete, true)
	if code != http.StatusOK {
		t.Fatalf("取消：状态码 = %d，期望 200", code)
	}
	if !data.Changed || data.Liked || data.Count != 0 {
		t.Errorf("取消后 changed=%v liked=%v count=%d，期望 true/false/0", data.Changed, data.Liked, data.Count)
	}
}

// TestLikeEndpointRequiresLogin 验证没身份直接 401，不会用 0 号用户写关系。
func TestLikeEndpointRequiresLogin(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	h := NewLikeHandler(NewLikeService(db, rdb, article.NewArticleService(db, rdb)), nil)

	author := testutil.SeedUser(t, db, "anonauthor")
	post := testutil.SeedArticle(t, db, author.ID, "游客点赞的文章")

	code, _ := doLike(t, h, post.ID, 0, http.MethodPut, false)
	if code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", code)
	}
	if got := testutil.CountRows(t, db, "article_likes"); got != 0 {
		t.Errorf("未登录不该写关系，article_likes 有 %d 行", got)
	}
}

// TestLikeEndpointBadID 验证非数字 id 在 handler 边界就被挡掉。
func TestLikeEndpointBadID(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	h := NewLikeHandler(NewLikeService(db, rdb, article.NewArticleService(db, rdb)), nil)

	r := gin.New()
	r.PUT("/api/articles/:id/like", asUser(1), h.Like)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/articles/abc/like", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", w.Code)
	}
}

// TestLikeEndpointMissingArticle 验证点赞不存在的文章回 404 而不是 500。
func TestLikeEndpointMissingArticle(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	h := NewLikeHandler(NewLikeService(db, rdb, article.NewArticleService(db, rdb)), nil)
	reader := testutil.SeedUser(t, db, "missingliker")

	code, _ := doLike(t, h, 999999, reader.ID, http.MethodPut, true)
	if code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", code)
	}
}
