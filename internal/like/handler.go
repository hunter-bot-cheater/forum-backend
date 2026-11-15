package like

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/hot"
	"forum-backend/internal/httputil"
	"forum-backend/pkg/response"
)

// LikeHandler 点赞与热榜的 HTTP 入口。
// 同时持有 likeSvc 与 hotSvc：热榜接口是只读的，放在点赞模块里是因为
// 榜单的分数就来自点赞数，两个接口共享同一套「点赞数」的口径。
type LikeHandler struct {
	likeSvc *LikeService
	hotSvc  *hot.HotService
}

// NewLikeHandler 注入点赞与热榜两个 service，由 router 在装配期调用。
func NewLikeHandler(likeSvc *LikeService, hotSvc *hot.HotService) *LikeHandler {
	return &LikeHandler{likeSvc: likeSvc, hotSvc: hotSvc}
}

// toLikeResp 把 service 的结论转成对外结构体。
// 转换留在 module 内，service 不需要 import 自己的 handler 层。
func toLikeResp(r LikeResult) likeResp {
	return likeResp{
		ArticleID: r.ArticleID,
		Count:     r.Count,
		Changed:   r.Changed,
		Liked:     r.Liked,
	}
}

// Like godoc
// @Summary      点赞文章
// @Tags         likes
// @Produce      json
// @Param        id   path  int  true   "文章 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=like.likeResp}
// @Failure      401  {string}  string    "未登录"
// @Failure      404  {string}  string    "文章不存在"
// @Security     Bearer
// @Router       /articles/{id}/like [put]
// Like 处理 PUT /api/articles/:id/like，重复点赞返回 200 且 changed=false。
func (h *LikeHandler) Like(c *gin.Context) {
	// 用户身份只来自中间件校验过的 token，不接受请求体或查询参数指定。
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 路径参数是字符串，先转成数字再交给 service，非法 id 不进 SQL 条件。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	result, err := h.likeSvc.Like(c.Request.Context(), uint(id), userID)
	if err != nil {
		// 文章不存在、数据库故障都在这里映射成统一的错误响应。
		httputil.MapServiceError(c, "like article", err)
		return
	}
	response.OK(c, toLikeResp(result))
}

// Unlike godoc
// @Summary      取消点赞
// @Tags         likes
// @Produce      json
// @Param        id   path  int  true   "文章 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=like.likeResp}
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /articles/{id}/like [delete]
// Unlike 处理 DELETE /api/articles/:id/like，取消一个没点过的赞也返回 200。
func (h *LikeHandler) Unlike(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	result, err := h.likeSvc.Unlike(c.Request.Context(), uint(id), userID)
	if err != nil {
		httputil.MapServiceError(c, "unlike article", err)
		return
	}
	response.OK(c, toLikeResp(result))
}

// Hot godoc
// @Summary      热门文章 Top N
// @Tags         likes
// @Produce      json
// @Param        limit  query  int  false  "返回条数，默认 10，最大 100"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=hot.HotListResp}
// @Failure      400  {string}  string    "limit 非法"
// @Router       /articles/hot [get]
// Hot 处理 GET /api/articles/hot，默认 10 条，上限由 service 的 checkTopN 把关。
func (h *LikeHandler) Hot(c *gin.Context) {
	// 不传 limit 就用默认值；传了必须先转成整数，
	// 否则下游拿到 0 会触发 checkTopN 的越界错误，报错文案也对不上。
	limit := 10
	raw := c.Query("limit")
	if raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			response.Error(c, http.StatusBadRequest, "limit 必须是整数")
			return
		}
		limit = v
	}

	list, err := h.hotSvc.Top(c.Request.Context(), limit)
	if err != nil {
		httputil.MapServiceError(c, "hot articles", err)
		return
	}

	// 从空切片开始，空榜时序列化成 [] 而不是 null，前端可以直接遍历。
	resp := make([]hot.HotArticleResp, 0, len(list))
	for _, item := range list {
		resp = append(resp, hot.ToHotArticleResp(item))
	}
	response.OK(c, hot.HotListResp{List: resp})
}
