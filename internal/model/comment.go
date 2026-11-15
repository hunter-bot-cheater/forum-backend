// Package model 定义 GORM 数据模型。
package model

import "time"

// Comment 评论表。
//
// Article 关联只用于让 AutoMigrate 建外键，约束是 OnDelete:RESTRICT，
// 即还有评论的文章不允许被删，避免出现查不到文章的孤儿评论。
// json:"-" 是因为评论接口只返回作者信息，不把整篇文章嵌进响应里。
type Comment struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ArticleID uint      `gorm:"index;not null" json:"article_id"` // 按文章拉评论列表，必须走索引
	Article   Article   `gorm:"constraint:OnUpdate:NO ACTION,OnDelete:RESTRICT;" json:"-"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	User      User      `json:"user,omitempty"` // 同上，Preload 后才有值
	Content   string    `gorm:"type:text;not null" json:"content"`
	CreatedAt time.Time `json:"created_at"`
}
