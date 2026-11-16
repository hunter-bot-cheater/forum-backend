package article

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/httputil"
	"forum-backend/pkg/response"
)

// LikedChecker 回答「这个用户点过这篇文章吗」。
//
// 用接口而不是直接 import like 包：like 已经依赖 article（点赞后要调 InvalidateDetailCache），
// article 反过来再依赖 like 就成了循环依赖。实现由 router 在装配期注入 like.LikeService。
type LikedChecker interface {
	HasLiked(ctx context.Context, articleID, userID uint) (bool, error)
}

// ArticleHandler 是文章模块的 HTTP 入口，只依赖同包的 service 与一个点赞查询接口，
// 不认识 GORM，也不认识 like 包的类型。
// 每个方法都走同一个套路：解析参数 → 调 service → 成功交 DTO / 失败交 MapServiceError。
type ArticleHandler struct {
	svc *ArticleService
	// liked 只服务详情页的「我点过没」。游客访问时用不到。
	liked LikedChecker
}

// NewArticleHandler 注入文章 service 与点赞查询，由 router 在装配期调用。
func NewArticleHandler(svc *ArticleService, liked LikedChecker) *ArticleHandler {
	return &ArticleHandler{svc: svc, liked: liked}
}

// List godoc
// @Summary      文章分页列表
// @Tags         articles
// @Produce      json
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=articleListResp}
// @Failure      400  {string}  string    "分页参数越界"
// @Router       /articles [get]
// List 处理 GET /api/articles，返回当前页数据与总数。
func (h *ArticleHandler) List(c *gin.Context) {
	// 分页参数统一由 httputil.ParsePage 解析：缺省值、上下界、溢出检查都在那一处，
	// 各接口不重复写，口径才不会漂。出错时交给 MapServiceError 统一映射。
	page, pageSize, err := httputil.ParsePage(c)
	if err != nil {
		httputil.MapServiceError(c, "list articles", err)
		return
	}

	articles, total, err := h.svc.List(c.Request.Context(), page, pageSize)
	if err != nil {
		httputil.MapServiceError(c, "list articles", err)
		return
	}

	// 这里必须从「空切片」开始，而不是声明 var list []articleListItem：
	// nil 切片会被序列化成 null，前端拿到的 list 就不是数组了。
	// 容量先按 len(articles) 申请，避免 append 过程中反复扩容。
	// 列表项不含正文，只带服务端截好的摘要，响应体不会随正文长度膨胀。
	list := make([]articleListItem, 0, len(articles))
	for _, a := range articles {
		list = append(list, toArticleListItem(a))
	}

	resp := articleListResp{
		List:     list,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	response.OK(c, resp)
}

// Detail godoc
// @Summary      文章详情
// @Tags         articles
// @Produce      json
// @Param        id   path  int  true  "文章 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=articleDetailResp}
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      404  {string}  string    "文章不存在"
// @Router       /articles/{id} [get]
// Detail 处理 GET /api/articles/:id，缓存命中时不会碰数据库。
func (h *ArticleHandler) Detail(c *gin.Context) {
	// 路径参数是字符串，先转成数字再交给 service。
	// ParseUint 的第二个参数 10 表示十进制，第三个 64 表示位宽；
	// 转换失败（传了字母、负数、超大数）直接在边界挡掉，不会把脏值带进 SQL 条件。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	a, err := h.svc.Detail(c.Request.Context(), uint(id))
	if err != nil {
		// service 已经把「查不到」统一成 domain.ErrNotFound，这里不需要认识 GORM 的错误。
		httputil.MapServiceError(c, "article detail", err)
		return
	}
	// 详情比列表多一个 like_count 与 liked，所以用另一个 DTO 包一层。
	resp := toArticleDetailResp(a)

	// liked 不能跟着文章进缓存：缓存里存的是文章本身（含 like_count），
	// 「我点过没」每个用户都不同，只能按当前访问者实时查。
	// 路由挂了 OptionalJWTAuth，没登录或 token 过期时 CurrentUserID 返回 false，
	// 此时保持 false——游客没有可判断的身份。
	if userID, ok := httputil.CurrentUserID(c); ok && h.liked != nil {
		liked, err := h.liked.HasLiked(c.Request.Context(), uint(id), userID)
		if err != nil {
			// 点赞状态查不出来不该让整篇文章打不开：正文已经拿到了。
			// 这里降级成「未点赞」，用户真点了会走 Like 接口，那边返回的才是真实状态。
			slog.WarnContext(c.Request.Context(), "check liked for article detail",
				"article_id", id, "user_id", userID, "error", err)
		} else {
			resp.Liked = liked
		}
	}

	response.OK(c, resp)
}

// Create godoc
// @Summary      发布文章
// @Tags         articles
// @Accept       json
// @Produce      json
// @Param        body body createArticleReq true "标题与正文"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=articleResp}
// @Failure      400  {string}  string    "请求体格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /articles [post]
// Create 处理 POST /api/articles，作者取自 token，不接受客户端指定。
func (h *ArticleHandler) Create(c *gin.Context) {
	// 从 Gin context 取身份。GetUint 在类型不对或没值时返回 0，
	// httputil.CurrentUserID 把 0 判成失败，避免把 0 号用户写进数据库。
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 只往两个字段的结构体里绑。请求体里额外的 user_id 字段
	// 会被 encoding/json 直接忽略，伪造作者身份这条路走不通。
	var req createArticleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		// JSON 语法错、字段类型错、body 超过 limitBody 的 1 MiB，都落在这里。
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}

	// 三个入参里，只有后两个来自客户端；userID 来自中间件校验过的 token。
	a, err := h.svc.Create(c.Request.Context(), userID, req.Title, req.Content)
	if err != nil {
		// 标题正文为空、超长都会走到这里，由 MapServiceError 映射成 400。
		httputil.MapServiceError(c, "create article", err)
		return
	}

	// 只把 DTO 交给响应包，数据库模型不会直接出现在响应里。
	response.OK(c, toArticleResp(a))
}

// Delete 处理 DELETE /api/articles/:id，仅作者本人可删。
func (h *ArticleHandler) Delete(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 路径参数先转成数字。非法 id 在 handler 边界挡掉，
	// 不把原始字符串拼进 SQL 条件。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	// 「是不是作者」的判断放在 service 的 WHERE 里由数据库完成，handler 不查库，
	// 避免多一次往返，也避免查完再删之间的竞态。
	if err := h.svc.Delete(c.Request.Context(), uint(id), userID); err != nil {
		httputil.MapServiceError(c, "delete article", err)
		return
	}

	// 当前 response 包只有 OK(200)，所以删除成功返回 200 且 data 为 null。
	// 00 章契约里的 204 属于目标状态，要等响应包加 NoContent 之后一起改。
	response.OK(c, nil)
}
