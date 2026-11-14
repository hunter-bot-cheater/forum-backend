// Package model 定义 GORM 数据模型。
package model

import "time"

// User 用户表
type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"size:64;uniqueIndex;not null" json:"username"`
	Password  string    `gorm:"size:128;not null" json:"-"` // bcrypt 哈希，绝不返回明文
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

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

// ArticleLike 一条点赞关系。
// 用自增 ID 加联合唯一索引。
type ArticleLike struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ArticleID uint      `gorm:"not null;uniqueIndex:uk_article_user,priority:1" json:"article_id"`
	UserID    uint      `gorm:"not null;uniqueIndex:uk_article_user,priority:2" json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	// Article 只用来让 AutoMigrate 建外键，不参与 JSON 序列化。
	Article Article `gorm:"constraint:OnUpdate:NO ACTION,OnDelete:RESTRICT;" json:"-"`
}
//最左前缀法则 articleid在左边 SELECT * FROM article_likes WHERE article_id = 1 会走索引，极快
