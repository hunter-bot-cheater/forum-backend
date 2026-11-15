// Package model 定义 GORM 数据模型。
package model

import "time"

// User 用户表。
// Username 是登录名，唯一索引兜住并发注册同名；Password 存 bcrypt 哈希，
// json:"-" 保证任何直接序列化 model 的路径都不会把哈希漏出去。
type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"size:64;uniqueIndex;not null" json:"username"`
	Password  string    `gorm:"size:128;not null" json:"-"` // bcrypt 哈希，绝不返回明文
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
