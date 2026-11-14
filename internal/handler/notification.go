package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/service"
	"forum-backend/pkg/response"
)

// NotificationHandler 站内通知的两个 HTTP 入口。
type NotificationHandler struct {
	svc *service.NotificationService
}

func NewNotificationHandler(svc *service.NotificationService) *NotificationHandler {
	return &NotificationHandler{svc: svc}
}

// List godoc
// @Summary      站内通知列表
// @Tags         notifications
// @Produce      json
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.notificationListResp}
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /notifications [get]
// List 处理 GET /api/notifications。
func (h *NotificationHandler) List(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}
	page, pageSize, err := parsePage(c)
	if err != nil {
		mapServiceError(c, "list notifications", err)
		return
	}

	list, total, unread, err := h.svc.List(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		mapServiceError(c, "list notifications", err)
		return
	}

	items := make([]notificationResp, 0, len(list))
	for _, n := range list {
		items = append(items, notificationResp{
			ID:         n.ID,
			EventID:    n.EventID,
			ActorID:    n.ActorID,
			Type:       n.Type,
			ArticleID:  n.ArticleID,
			TargetType: n.TargetType,
			TargetID:   n.TargetID,
			IsRead:     n.IsRead,
			CreatedAt:  n.CreatedAt,
		})
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
// @Success      200  {object}  handler.SwaggerEnvelope
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /notifications/read-all [put]
// ReadAll 处理 PUT /api/notifications/read-all。
func (h *NotificationHandler) ReadAll(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}
	affected, err := h.svc.MarkAllRead(c.Request.Context(), userID)
	if err != nil {
		mapServiceError(c, "mark notifications read", err)
		return
	}
	response.OK(c, gin.H{"affected": affected})
}
