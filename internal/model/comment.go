// Package model 定义 GORM 数据模型。
package model

import "time"

// Comment 评论表
type Comment struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ArticleID uint      `gorm:"index;not null" json:"article_id"`
	Article   Article   `gorm:"constraint:OnUpdate:NO ACTION,OnDelete:RESTRICT;" json:"-"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	User      User      `json:"user,omitempty"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	CreatedAt time.Time `json:"created_at"`
}
