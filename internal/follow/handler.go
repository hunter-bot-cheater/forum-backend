package follow

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/httputil"
	"forum-backend/pkg/response"
)

// Handler 是关注模块的 HTTP 入口。
// 关注数与列表都算业务，handler 只做参数解析和响应组装。
type Handler struct {
	svc *Service
}

// NewHandler 由 router 在装配期调用。
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Follow godoc
// @Summary      关注用户
// @Description  重复关注也返回 200，changed 表示这次是否真的新增了关系。
// @Tags         follow
// @Produce      json
// @Param        id   path  int  true  "被关注者 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=follow.followStateResp}
// @Failure      400  {string}  string    "id 非法或试图关注自己"
// @Failure      401  {string}  string    "未登录"
// @Failure      404  {string}  string    "用户不存在"
// @Security     Bearer
// @Router       /users/{id}/follow [put]
// Follow 处理 PUT /api/users/:id/follow。
func (h *Handler) Follow(c *gin.Context) {
	followerID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}
	followeeID, ok := parseUserID(c)
	if !ok {
		return
	}

	state, err := h.svc.Follow(c.Request.Context(), followerID, followeeID)
	if err != nil {
		httputil.MapServiceError(c, "follow user", err)
		return
	}
	response.OK(c, toFollowStateResp(state))
}

// Unfollow godoc
// @Summary      取消关注
// @Description  本来就没关注也返回 200，便于前端无脑重试。
// @Tags         follow
// @Produce      json
// @Param        id   path  int  true  "被取关者 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=follow.followStateResp}
// @Failure      400  {string}  string    "id 非法"
// @Failure      401  {string}  string    "未登录"
// @Failure      404  {string}  string    "用户不存在"
// @Security     Bearer
// @Router       /users/{id}/follow [delete]
// Unfollow 处理 DELETE /api/users/:id/follow。
func (h *Handler) Unfollow(c *gin.Context) {
	followerID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}
	followeeID, ok := parseUserID(c)
	if !ok {
		return
	}

	state, err := h.svc.Unfollow(c.Request.Context(), followerID, followeeID)
	if err != nil {
		httputil.MapServiceError(c, "unfollow user", err)
		return
	}
	response.OK(c, toFollowStateResp(state))
}

// Following godoc
// @Summary      某人的关注列表
// @Tags         follow
// @Produce      json
// @Param        id         path   int  true   "用户 ID"
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=follow.followListResp}
// @Failure      400  {string}  string    "分页或 id 非法"
// @Failure      404  {string}  string    "用户不存在"
// @Router       /users/{id}/following [get]
// Following 处理 GET /api/users/:id/following。
// 个人中心的「我的关注」就是拿自己的 id 调这个接口，不额外开 /me 版本。
func (h *Handler) Following(c *gin.Context) {
	h.list(c, h.svc.ListFollowing)
}

// Followers godoc
// @Summary      某人的粉丝列表
// @Tags         follow
// @Produce      json
// @Param        id         path   int  true   "用户 ID"
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=follow.followListResp}
// @Failure      400  {string}  string    "分页或 id 非法"
// @Failure      404  {string}  string    "用户不存在"
// @Router       /users/{id}/followers [get]
// Followers 处理 GET /api/users/:id/followers。
func (h *Handler) Followers(c *gin.Context) {
	h.list(c, h.svc.ListFollowers)
}

// listOf 是两条列表接口的共同签名。
// 用函数值把「取关注」与「取粉丝」的差别挡在调用处，list 里只剩参数解析与响应组装。
type listOf func(ctx context.Context, userID, viewerID uint, page, pageSize int) ([]Item, int64, error)

// list 解析分页参数、调对应的 service 方法、拼统一形状的响应。
func (h *Handler) list(c *gin.Context, fetch listOf) {
	userID, ok := parseUserID(c)
	if !ok {
		return
	}
	page, pageSize, err := httputil.ParsePage(c)
	if err != nil {
		httputil.MapServiceError(c, "parse page", err)
		return
	}

	// 未登录时 viewerID 为 0：列表照常返回，只有 followed_by_me 全是 false。
	var viewerID uint
	if current, ok := httputil.CurrentUserID(c); ok {
		viewerID = current
	}

	items, total, err := fetch(c.Request.Context(), userID, viewerID, page, pageSize)
	if err != nil {
		httputil.MapServiceError(c, "list follows", err)
		return
	}
	response.OK(c, toFollowListResp(items, total, page, pageSize))
}

// parseUserID 解析路径里的 :id。
// 解析失败时已经把 400 写回客户端，调用方直接 return 即可。
func parseUserID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeBadRequest, "id 格式不正确")
		return 0, false
	}
	return uint(id), true
}
