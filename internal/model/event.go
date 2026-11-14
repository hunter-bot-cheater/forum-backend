// Package model 定义 GORM 数据模型。
package model

import "time"

// outbox 事件的三种状态。
const (
	OutboxPending   = "pending"   // 还没投进 Stream，等 relay 扫到
	OutboxPublished = "published" // 已经 XADD 成功
	OutboxFailed    = "failed"    // 重试到上限，留着给人工排查
)

// OutboxEvent 是事务发件箱里的一行。
// 业务写入和这一行由同一个 MySQL 事务提交，事件因此不会凭空消失。
type OutboxEvent struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	EventID       string    `gorm:"size:36;not null;uniqueIndex:uk_outbox_event" json:"event_id"`
	EventType     string    `gorm:"size:32;not null" json:"event_type"`
	AggregateType string    `gorm:"size:32;not null" json:"aggregate_type"`
	AggregateID   uint      `gorm:"not null" json:"aggregate_id"`
	Payload       string    `gorm:"type:text;not null" json:"payload"`
	Status        string    `gorm:"size:16;not null;index" json:"status"` //relay后台会不断扫描 where status='pending' 所以要加索引
	Attempts      int       `gorm:"not null;default:0" json:"attempts"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

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
