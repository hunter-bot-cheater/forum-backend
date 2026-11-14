// Package model 定义 GORM 数据模型。
package model

import "time"

// outbox 事件的三种状态。
const (
	OutboxPending   = "pending"   // 还没投进 Stream，等 relay 扫到
	OutboxPublished = "published" // 已经 XADD 成功
	OutboxFailed    = "failed"    // 重试上限，留着给人工排查
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
