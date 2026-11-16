// Package model 定义 GORM 数据模型。
// 这里只放表结构本身，不放查询逻辑；业务包拿到 model 后自己组 where 条件。
package model

import (
	"time"

	"gorm.io/gorm"
)

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

	// DeletedAt 让删除变成「打标记」而不是真删。
	//
	// 作者点删除通常是手误或情绪化操作，硬删之后评论、点赞、标签关联一起消失，
	// 想恢复只能翻备份。软删只把这一行标成已删，GORM 之后所有查询自动带上
	// deleted_at IS NULL，业务代码不用改一行；子表数据原样留着，恢复时还完整。
	// 代价是表会变大，需要定期归档（见 deploy/backup.sh 的说明）。
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// ViewCount 是浏览数，只在详情接口被访问时自增，由 article.RecordView 维护。
	//
	// 它是「尽力而为」的计数：同一访问者 30 分钟内看同一篇只算一次（Redis SETNX 去重），
	// Redis 故障期间降级成每次都算。所以这个数字适合排序与展示，
	// 不适合当审计依据——要看真实访问得从 access log 里数。
	ViewCount int64 `gorm:"default:0" json:"view_count"`

	// Tags 是这篇文章的标签名，不落在 articles 表里（gorm:"-" 让 GORM 忽略它）。
	// 由 article 包在查询后从 article_tags 关联表填充，随文章一起写进详情缓存，
	// 所以命中缓存的请求也能直接拿到，不必再查一次关联表。
	Tags []string `gorm:"-" json:"tags"`

	// Excerpt 不是数据库列，只是列表查询时用 SQL 截出来的正文开头。
	// gorm:"->" 表示只读：写入时 GORM 跳过它，AutoMigrate 也不会给它建列。
	// json:"-" 是因为 model 不直接对外序列化，且详情缓存会把它整个 marshal 进 Redis，
	// 这个字段在那里没有意义，不该混进缓存值。
	Excerpt   string    `gorm:"->" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
