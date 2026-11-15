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
//
// 为什么要有这张表：点赞、评论这些动作要在同一个 MySQL 事务里既改业务数据、
// 又留下「该发通知了」这条意图。如果直接在这个事务里去 XADD Redis，
// 事务回滚了就发了个假通知，Redis 挂了又丢了真通知。
// 所以先落库（同一事务，必定成功或必定回滚），再由 relay 后台线程搬到 Redis，
// 搬成功才把状态改成 published。
type OutboxEvent struct {
	ID            uint   `gorm:"primaryKey" json:"id"`
	EventID       string `gorm:"size:36;not null;uniqueIndex:uk_outbox_event" json:"event_id"` // 幂等键，重复插入会被唯一索引挡掉
	EventType     string `gorm:"size:32;not null" json:"event_type"`
	AggregateType string `gorm:"size:32;not null" json:"aggregate_type"` // 事件来源的聚合类型，如 article / like
	AggregateID   uint   `gorm:"not null" json:"aggregate_id"`
	Payload       string `gorm:"type:text;not null" json:"payload"` // 序列化后的事件体，消费端反序列化使用
	// relay 后台会不断扫描 WHERE status='pending'，所以这列必须有索引，
	// 否则每轮扫描都是全表扫。
	Status    string    `gorm:"size:16;not null;index" json:"status"`
	Attempts  int       `gorm:"not null;default:0" json:"attempts"` // 投递失败次数，超过上限置 failed
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
