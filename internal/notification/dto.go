package notification

import (
	"time"

	"forum-backend/internal/model"
	"forum-backend/internal/user"
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

	// Actor 是触发者的昵称头像，前端靠它显示「谁赞了你」。
	// 用指针加 omitempty：账号注销后查不到资料，此时整个字段省掉，
	// 前端拿 actor_id 也画不出有效内容，直接按「有人」兜底。
	// actor_id 仍然保留：它是稳定标识，点击跳主页时用得上。
	Actor *user.BriefResp `json:"actor,omitempty"`

	// ArticleTitle 是互动发生在哪篇文章的标题，让用户不必点进去才知道是哪篇。
	// 文章已删（或非文章域事件 article_id=0）时为空，omitempty 让字段整个不出现，
	// 前端据此决定要不要渲染标题行。
	ArticleTitle string `json:"article_title,omitempty"`
}

// toNotificationResp 把模型、触发者资料与文章标题转成对外结构体。
// articleTitle 由 service 批量查好后传入，handler 不认识表结构。
func toNotificationResp(notice *model.Notification, actor user.Brief, articleTitle string) notificationResp {
	return notificationResp{
		ID:           notice.ID,
		EventID:      notice.EventID,
		ActorID:      notice.ActorID,
		Type:         notice.Type,
		ArticleID:    notice.ArticleID,
		TargetType:   notice.TargetType,
		TargetID:     notice.TargetID,
		IsRead:       notice.IsRead,
		CreatedAt:    notice.CreatedAt,
		Actor:        user.NewBriefRespPtr(actor),
		ArticleTitle: articleTitle,
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
	// Kind 回显本次查询的分类（空串表示全部）。
	// 前端连续切 tab 时可能有多个请求在飞，靠回显判断「这一页属于哪个 tab」，
	// 否则慢的那次会把当前 tab 的列表覆盖成上一个 tab 的内容。
	Kind string `json:"kind"`
}

// unreadSummaryResp 是分类未读数，对应消息中心顶部 tab 上的红点。
// by_kind 保证包含全部分类（没有未读时是 0），前端不用判 undefined。
type unreadSummaryResp struct {
	Total  int64            `json:"total"`
	ByKind map[string]int64 `json:"by_kind"`
}

// toUnreadSummaryResp 把 service 的统计结果转成对外结构。
func toUnreadSummaryResp(s UnreadSummary) unreadSummaryResp {
	byKind := make(map[string]int64, len(s.ByKind))
	for kind, count := range s.ByKind {
		byKind[kind] = count
	}
	return unreadSummaryResp{Total: s.Total, ByKind: byKind}
}
