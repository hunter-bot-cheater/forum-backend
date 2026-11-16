// Package model 定义 GORM 数据模型。
package model

import "time"

// Tag 标签表。名字全局唯一：同一个标签只有一行，多篇文章共用。
//
// 名字用 varchar(32) 而不是 text：标签是给人扫一眼的短词，
// 允许长句会让「后端」「Go 并发模型」两个量级的东西出现在同一列里。
// 唯一索引让「先查再插」变成「插了就知道有没有」，重复标签由数据库挡住。
type Tag struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:32;not null;uniqueIndex:uk_tag_name" json:"name"`
	// 标签没有「作者」也没有「描述」，只记创建时间，用于排查异常标签是怎么冒出来的。
	CreatedAt time.Time `json:"created_at"`
}

// ArticleTag 文章与标签的多对多关联。
//
// 用复合主键而不是自增 id + 唯一索引：这张表除了「有没有这条关联」没有别的信息，
// 主键本身就是去重约束，多一个自增列只会让每行多 8 字节。
// 复合主键的顺序是 (article_id, tag_id)：按文章查标签走最左前缀，
// 这也正是列表与详情的主要查询方向。
type ArticleTag struct {
	ArticleID uint `gorm:"primaryKey" json:"article_id"`
	TagID     uint `gorm:"primaryKey" json:"tag_id"`

	// 两个关联字段只用于让 AutoMigrate 建外键，不参与 JSON 序列化。
	// 都用 RESTRICT：删文章时由 article.Delete 先清关联，删标签时也不允许留下悬空行。
	Article Article `gorm:"constraint:OnUpdate:NO ACTION,OnDelete:RESTRICT;" json:"-"`
	Tag     Tag     `gorm:"constraint:OnUpdate:NO ACTION,OnDelete:RESTRICT;" json:"-"`
}
