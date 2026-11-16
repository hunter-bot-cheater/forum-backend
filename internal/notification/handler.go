package notification

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/httputil"
	"forum-backend/pkg/response"
)

// NotificationHandler 站内通知的两个 HTTP 入口。
type NotificationHandler struct {
	svc *NotificationService
}

// NewNotificationHandler 由装配层调用，注入已构造好的 service。
func NewNotificationHandler(svc *NotificationService) *NotificationHandler {
	return &NotificationHandler{svc: svc}
}

// List godoc
// @Summary      站内通知列表
// @Tags         notifications
// @Produce      json
// @Param        page       query  int     false  "页码，默认 1"
// @Param        page_size  query  int     false  "每页条数，默认 10，最大 50"
// @Param        kind       query  string  false  "分类：like / comment / follow，留空为全部"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=notification.notificationListResp}
// @Failure      400  {string}  string    "分类或分页参数非法"
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /notifications [get]
// List 处理 GET /api/notifications。
// 收件人只从 JWT 里取，不接受 query 传 user_id，否则任何登录用户都能翻别人的通知。
func (h *NotificationHandler) List(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}
	page, pageSize, err := httputil.ParsePage(c)
	if err != nil {
		httputil.MapServiceError(c, "list notifications", err)
		return
	}

	// kind 留空表示「全部」。非法取值由 service 判，避免这一层再维护一份分类白名单。
	kind := c.Query("kind")
	list, total, unread, err := h.svc.List(c.Request.Context(), userID, kind, page, pageSize)
	if err != nil {
		httputil.MapServiceError(c, "list notifications", err)
		return
	}

	items := make([]notificationResp, 0, len(list))
	for _, item := range list {
		items = append(items, toNotificationResp(item.Notice, item.Actor, item.ArticleTitle))
	}
	response.OK(c, notificationListResp{
		List:        items,
		Total:       total,
		UnreadCount: unread,
		Page:        page,
		PageSize:    pageSize,
		Kind:        kind,
	})
}

// Summary godoc
// @Summary      各分类未读数
// @Tags         notifications
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=notification.unreadSummaryResp}
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /notifications/summary [get]
// Summary 处理 GET /api/notifications/summary。
// 只回计数不回列表：侧边栏角标与 tab 红点都只需要数字，
// 为了拿一个数字去拉一页通知是浪费。
func (h *NotificationHandler) Summary(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	summary, err := h.svc.UnreadSummary(c.Request.Context(), userID)
	if err != nil {
		httputil.MapServiceError(c, "summarize notifications", err)
		return
	}
	response.OK(c, toUnreadSummaryResp(summary))
}

// MarkRead godoc
// @Summary      标记单条通知已读
// @Tags         notifications
// @Produce      json
// @Param        id  path  int  true  "通知 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Failure      404  {string}  string    "通知不存在或不属于当前用户"
// @Security     Bearer
// @Router       /notifications/{id}/read [put]
// MarkRead 处理 PUT /api/notifications/:id/read。
//
// 没有它就只剩「全部已读」：用户点开一条消息后红点还在，
// 想消掉只能把没看的也一起清掉。
func (h *NotificationHandler) MarkRead(c *gin.Context) {
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

	// 归属校验在 service 的 WHERE 里，handler 不查库。
	// 已经读过的返回 0 而不是 404：重复点击、多端同时点开都是正常情况。
	affected, err := h.svc.MarkRead(c.Request.Context(), userID, uint(id))
	if err != nil {
		httputil.MapServiceError(c, "mark notification read", err)
		return
	}
	response.OK(c, gin.H{"affected": affected})
}

// ReadAll godoc
// @Summary      标记全部通知已读
// @Tags         notifications
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /notifications/read-all [put]
// ReadAll 处理 PUT /api/notifications/read-all。
// 用 PUT 而不是 POST，因为这个操作幂等：重复调用结果都是「全部已读」。
func (h *NotificationHandler) ReadAll(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}
	affected, err := h.svc.MarkAllRead(c.Request.Context(), userID)
	if err != nil {
		httputil.MapServiceError(c, "mark notifications read", err)
		return
	}
	response.OK(c, gin.H{"affected": affected})
}
