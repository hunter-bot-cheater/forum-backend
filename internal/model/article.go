// Package model 定义 GORM 数据模型。
// 这里只放表结构本身，不放查询逻辑；业务包拿到 model 后自己组 where 条件。
package model

import "time"

// Article 文章表。
//
// 表名由 GORM 从类型名复数化得到（article -> articles）。
// UserID 上建了普通索引，因为列表和详情都要按作者反查；
// LikeCount 是 MySQL 里的计数值，和 Redis 的点赞数最终一致（Redis 为准，定时/异步回写）。
type Article struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	UserID    uint   `gorm:"index;not null" json:"user_id"`
	User      User   `json:"user,omitempty"` // 关联作者，查询时 Preload 才会填
	Title     string `gorm:"size:128;not null" json:"title"`
	Content   string `gorm:"type:text;not null" json:"content"`
	LikeCount int64  `gorm:"default:0" json:"like_count"` // 与 Redis 计数最终一致

	// Excerpt 不是数据库列，只是列表查询时用 SQL 截出来的正文开头。
	// gorm:"->" 表示只读：写入时 GORM 跳过它，AutoMigrate 也不会给它建列。
	// json:"-" 是因为 model 不直接对外序列化，且详情缓存会把它整个 marshal 进 Redis，
	// 这个字段在那里没有意义，不该混进缓存值。
	Excerpt   string    `gorm:"->" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
