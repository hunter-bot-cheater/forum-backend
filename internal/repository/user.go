// Package repository 数据访问层：封装 MySQL 与 Redis 的读写。
// 第 4 周会用接口抽象，便于测试时注入 mock。
package repository

import "gorm.io/gorm"

// UserRepo 用户数据访问
type UserRepo struct {
	db *gorm.DB
}

func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}
