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
