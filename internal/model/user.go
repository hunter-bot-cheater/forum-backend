// Package model 定义 GORM 数据模型。
package model

import "time"

// User 用户表。
// Username 是登录名，唯一索引兜住并发注册同名；Password 存 bcrypt 哈希，
// json:"-" 保证任何直接序列化 model 的路径都不会把哈希漏出去。
//
// Nickname / AvatarURL / Bio 是展示资料，与登录凭证解耦：
// 改昵称不影响登录，也不会撞唯一索引。
// 三列都是 not null + default ''，存量数据迁移时不用回填 NULL；
// 昵称为空时展示层回退到用户名，不在 SQL 里写 COALESCE。
type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"size:64;uniqueIndex;not null" json:"username"`
	Nickname  string    `gorm:"size:32;not null;default:''" json:"nickname"`
	AvatarURL string    `gorm:"size:255;not null;default:''" json:"avatar_url"`
	Bio       string    `gorm:"size:200;not null;default:''" json:"bio"`
	Password  string    `gorm:"size:128;not null" json:"-"` // bcrypt 哈希，绝不返回明文
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
