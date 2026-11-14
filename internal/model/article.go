// Package model 定义 GORM 数据模型。
package model

import "time"

// Article 文章表
type Article struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	User      User      `json:"user,omitempty"`
	Title     string    `gorm:"size:128;not null" json:"title"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	LikeCount int64     `gorm:"default:0" json:"like_count"` // 与 Redis 计数最终一致
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
