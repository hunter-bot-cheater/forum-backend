// Package model 定义 GORM 数据模型。
package model

import "time"

// Follow 是一条关注关系：FollowerID 关注了 FolloweeID。
//
// 唯一索引建在 (follower_id, followee_id) 上，重复关注由数据库挡住，
// service 用 ON DUPLICATE KEY DO NOTHING 插入，请求重放不会产生两行。
// 这个复合索引的最左列就是 follower_id，「我关注了谁」的查询可以直接走它。
//
// 另建 idx_follow_followee 给「谁关注了我」用：
// 复合索引的右列不能单独当索引用，粉丝列表按 followee_id 查会全表扫。
//
// 不加外键：用户注销流程还没定义，外键会让「删用户」变成一堆约束问题；
// 关注目标是否存在由 service 先查一次给出准确的 404，其余脏数据由列表查询跳过。
type Follow struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	FollowerID uint      `gorm:"not null;uniqueIndex:uk_follow_pair,priority:1" json:"follower_id"`
	FolloweeID uint      `gorm:"not null;uniqueIndex:uk_follow_pair,priority:2;index:idx_follow_followee" json:"followee_id"`
	CreatedAt  time.Time `json:"created_at"`
}
