package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/service"
	"forum-backend/pkg/response"
)

// LikeHandler 点赞与热榜的 HTTP 入口，只依赖 service。
type LikeHandler struct {
	likeSvc *service.LikeService
	hotSvc  *service.HotService
}

func NewLikeHandler(likeSvc *service.LikeService, hotSvc *service.HotService) *LikeHandler {
	return &LikeHandler{likeSvc: likeSvc, hotSvc: hotSvc}
}

// toHotArticleResp 把 service 的结果转成对外结构体，转换留在 handler 层。
func toHotArticleResp(item service.HotArticle) hotArticleResp {
	return hotArticleResp{
		ArticleID: item.ArticleID,
		Score:     item.Score,
		Title:     item.Title,
		LikeCount: item.LikeCount,
	}
}

// toLikeResp 把 service 的结论转成对外结构体，转换留在 handler 层。
func toLikeResp(r service.LikeResult) likeResp {
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
// @Param        id   path  int  true  "文章 ID"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.likeResp}
// @Failure      401  {string}  string    "未登录"
// @Failure      404  {string}  string    "文章不存在"
// @Security     Bearer
// @Router       /articles/{id}/like [put]
// Like 处理 PUT /api/articles/:id/like。
func (h *LikeHandler) Like(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	result, err := h.likeSvc.Like(c.Request.Context(), uint(id), userID)
	if err != nil {
		mapServiceError(c, "like article", err)
		return
	}
	response.OK(c, toLikeResp(result))
}

// Unlike godoc
// @Summary      取消点赞
// @Tags         likes
// @Produce      json
// @Param        id   path  int  true  "文章 ID"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.likeResp}
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /articles/{id}/like [delete]
// Unlike 处理 DELETE /api/articles/:id/like。
func (h *LikeHandler) Unlike(c *gin.Context) {
	userID, ok := currentUserID(c)
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
		mapServiceError(c, "unlike article", err)
		return
	}
	response.OK(c, toLikeResp(result))
}

// Hot godoc
// @Summary      热门文章 Top N
// @Tags         likes
// @Produce      json
// @Param        limit  query  int  false  "返回条数，默认 10，最大 100"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.hotListResp}
// @Failure      400  {string}  string    "limit 非法"
// @Router       /articles/hot [get]
// Hot 处理 GET /api/articles/hot。
func (h *LikeHandler) Hot(c *gin.Context) {
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
		mapServiceError(c, "hot articles", err)
		return
	}

	resp := make([]hotArticleResp, 0, len(list))
	for _, item := range list {
		resp = append(resp, toHotArticleResp(item))
	}
	response.OK(c, hotListResp{List: resp})
}
