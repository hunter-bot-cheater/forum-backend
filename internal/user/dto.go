package user

import (
	"time"

	"forum-backend/internal/model"
)

// 本文件集中存放用户模块对外响应的结构体，以及从 model 到响应结构体的转换函数。
// 单独成文件是为了让「客户端能看见哪些字段」在目录树里一眼可查，
// 也避免把数据库模型直接交给 response.OK 序列化。

// userResp 是注册与登录接口对外返回的用户信息。
// 字段逐个列出，model.User 以后加字段不会自动出现在响应里。
// 这里刻意不写 Password：model.User 的 Password 带 json:"-"，
// 但 DTO 从源头就不给这个字段留位置，少一层依赖运气。
type userResp struct {
	ID        uint      `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// loginResp 是登录成功后 data 的形状。
// Token 与 User 平级，客户端解析一次就能拿到两份数据。
// User 用值类型，登录成功时一定有用户，不需要指针和 omitempty。
type loginResp struct {
	Token string   `json:"token"`
	User  userResp `json:"user"`
}

// toUserResp 把数据库模型转换成对外结构体。
// 转换只发生在 handler 层：service 继续只认识 model.User，
// 不需要 import handler 包，依赖方向保持单向。
func toUserResp(u *model.User) userResp {
	return userResp{
		ID:        u.ID,
		Username:  u.Username,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}
