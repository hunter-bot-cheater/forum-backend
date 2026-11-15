// Package model 定义 GORM 数据模型。
package model

import "time"

// ArticleLike 一条点赞关系：谁给哪篇文章点了赞。
//
// 用自增 ID 做主键，另加联合唯一索引 uk_article_user(article_id, user_id)。
// 唯一索引负责去重，重复点赞会被数据库直接拒掉，业务层不用先查再插。
// 联合索引的顺序是 article_id 在前，靠最左前缀法则，
// 只按 article_id 统计点赞数（WHERE article_id = ?）也能走上这个索引。
type ArticleLike struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ArticleID uint      `gorm:"not null;uniqueIndex:uk_article_user,priority:1" json:"article_id"`
	UserID    uint      `gorm:"not null;uniqueIndex:uk_article_user,priority:2" json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	// Article 只用来让 AutoMigrate 建外键，不参与 JSON 序列化。
	Article Article `gorm:"constraint:OnUpdate:NO ACTION,OnDelete:RESTRICT;" json:"-"`
}
