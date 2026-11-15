package notification

import (
	"time"

	"forum-backend/internal/model"
)

// 本文件集中存放站内通知模块对外响应的结构体。

// notificationResp 是一条站内通知对外的形状。
// target_type 加 target_id 表达跳转目标，新增行为不用加字段。
// HTTP 列表与 WebSocket 推送形状一致，前端按 target_type 分支。
type notificationResp struct {
	ID         uint      `json:"id"`
	EventID    string    `json:"event_id"`
	ActorID    uint      `json:"actor_id"`
	Type       string    `json:"type"`
	ArticleID  uint      `json:"article_id"`
	TargetType string    `json:"target_type"`
	TargetID   uint      `json:"target_id"`
	IsRead     bool      `json:"is_read"`
	CreatedAt  time.Time `json:"created_at"`
}

// toNotificationResp 把模型转成对外结构体。
func toNotificationResp(n *model.Notification) notificationResp {
	return notificationResp{
		ID:         n.ID,
		EventID:    n.EventID,
		ActorID:    n.ActorID,
		Type:       n.Type,
		ArticleID:  n.ArticleID,
		TargetType: n.TargetType,
		TargetID:   n.TargetID,
		IsRead:     n.IsRead,
		CreatedAt:  n.CreatedAt,
	}
}

// notificationListResp 是列表接口的 data 部分。
// List 初始化为空切片，保证空结果输出 [] 而不是 null，前端不用判空。
// UnreadCount 单独返回，前端不必自己遍历统计未读。
type notificationListResp struct {
	List        []notificationResp `json:"list"`
	Total       int64              `json:"total"`
	UnreadCount int64              `json:"unread_count"`
	Page        int                `json:"page"`
	PageSize    int                `json:"page_size"`
}
