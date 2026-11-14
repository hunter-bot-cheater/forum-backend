package user

import "gorm.io/gorm"

// UserRepo 用户数据访问
type UserRepo struct {
	db *gorm.DB
}

func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}
