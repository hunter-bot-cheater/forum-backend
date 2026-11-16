// Package model 定义 GORM 数据模型。
package model

import "time"

// Comment 评论表。
//
// Article 关联只用于让 AutoMigrate 建外键，约束是 OnDelete:RESTRICT，
// 即还有评论的文章不允许被删，避免出现查不到文章的孤儿评论。
// json:"-" 是因为评论接口只返回作者信息，不把整篇文章嵌进响应里。
//
// 回复用两列表达，构成两层的树（和抖音/微博一致：回复的回复会挂回根评论下）：
//   - ParentID 是直接回复的那条评论，顶级评论为 0；
//   - RootID 是所属的根评论，顶级评论自己为 0。
//
// 只存 ParentID 也能查出整棵树，但要递归（或查完再内存里拼），
// 每页 20 条根评论就会变成 N+1 次查询；存了 RootID 之后
// 「取这一页根评论的全部回复」是一条 WHERE root_id IN (...) 就够。
// 两个字段都进 idx_comment_root，按根评论聚合回复时走得上索引。
type Comment struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ArticleID uint      `gorm:"index;not null" json:"article_id"` // 按文章拉评论列表，必须走索引
	Article   Article   `gorm:"constraint:OnUpdate:NO ACTION,OnDelete:RESTRICT;" json:"-"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	User      User      `json:"user,omitempty"` // 同上，Preload 后才有值
	Content   string    `gorm:"type:text;not null" json:"content"`
	ParentID  uint      `gorm:"not null;default:0;index:idx_comment_root,priority:2" json:"parent_id"`
	RootID    uint      `gorm:"not null;default:0;index:idx_comment_root,priority:1" json:"root_id"`
	CreatedAt time.Time `json:"created_at"`
}
