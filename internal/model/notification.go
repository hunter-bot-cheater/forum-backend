// Package model 定义 GORM 数据模型。
package model

import "time"

// Notification 是一条站内通知。
// EventID 上的唯一索引是消费端幂等的最后一道保证。
// TargetType 加 TargetID 表达这条通知指向什么，新增行为不用再加列。
// ArticleID 是列表展示用的文章上下文，非文章域事件存 0。
// 这些关联列都不加外键：文章或评论被删后，通知仍应保留，点进去 404 即可。
type Notification struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"not null;index:idx_notify_user_read,priority:1" json:"user_id"`
	EventID    string    `gorm:"size:36;not null;uniqueIndex:uk_notify_event" json:"event_id"`
	ActorID    uint      `gorm:"not null" json:"actor_id"`
	Type       string    `gorm:"size:32;not null" json:"type"`
	ArticleID  uint      `gorm:"not null;default:0" json:"article_id"`
	TargetType string    `gorm:"size:32;not null;default:''" json:"target_type"`
	TargetID   uint      `gorm:"not null;default:0" json:"target_id"`
	IsRead     bool      `gorm:"not null;default:false;index:idx_notify_user_read,priority:2" json:"is_read"`
	CreatedAt  time.Time `json:"created_at"`
}
