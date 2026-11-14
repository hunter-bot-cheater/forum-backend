// Package model 定义 GORM 数据模型。
package model

import "time"

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
