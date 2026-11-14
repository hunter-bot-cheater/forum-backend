package notification

import (
	"forum-backend/internal/httputil"
	"net/http"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/response"
)

// NotificationHandler 站内通知的两个 HTTP 入口。
type NotificationHandler struct {
	svc *NotificationService
}

func NewNotificationHandler(svc *NotificationService) *NotificationHandler {
	return &NotificationHandler{svc: svc}
}

// List godoc
// @Summary      站内通知列表
// @Tags         notifications
// @Produce      json
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=notification.notificationListResp}
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /notifications [get]
// List 处理 GET /api/notifications。
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

	list, total, unread, err := h.svc.List(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		httputil.MapServiceError(c, "list notifications", err)
		return
	}

	items := make([]notificationResp, 0, len(list))
	for _, n := range list {
		items = append(items, toNotificationResp(n))
	}
	response.OK(c, notificationListResp{
		List:        items,
		Total:       total,
		UnreadCount: unread,
		Page:        page,
		PageSize:    pageSize,
	})
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
