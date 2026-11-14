// Package domain 存放跨模块复用的领域错误哨兵。
// 放在独立包里，是为了让 handler（错误映射）和各个业务 service 引用同一份定义，
// 避免「service.ErrNotFound」和「article.ErrNotFound」是两个不同变量导致 errors.Is 失配。
package domain

import "errors"

var (
	// ErrInvalidInput 参数不合法（校验失败、越界等）。
	ErrInvalidInput = errors.New("invalid input")
	// ErrNotFound 资源不存在。
	ErrNotFound = errors.New("not found")
	// ErrUserNameTaken 用户名已被占用。
	ErrUserNameTaken = errors.New("username already taken")
	// ErrInvalidCredentials 用户名或密码错误。
	ErrInvalidCredentials = errors.New("invalid username or password")
	// ErrFloorNotActive 抢楼活动未开始或已结束，或参数不合法。
	ErrFloorNotActive = errors.New("floor event is not active")
	// ErrFloorTaken 这一楼的名额已经被领走了（条件 UPDATE 的 RowsAffected 为 0）。
	ErrFloorTaken = errors.New("floor already taken")
)
