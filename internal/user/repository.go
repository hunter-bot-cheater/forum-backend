package user

import "gorm.io/gorm"

// UserRepo 是数据访问层的占位实现。
//
// 现状：注册与登录各只有一条 SQL，UserService 直接持有 *gorm.DB 就够了，
// 这个结构体目前没有任何调用点。留着是因为模块拆分时统一了四件套的形状，
// 等出现「同一查询被多个 service 复用」或需要替换数据源时再接进来。
// 如果长期不用，删掉它比留着更清楚。
type UserRepo struct {
	db *gorm.DB
}

// NewUserRepo 构造数据访问对象，当前无调用点。
func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}
