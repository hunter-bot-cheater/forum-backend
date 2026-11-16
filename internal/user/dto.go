package user

import (
	"time"

	"forum-backend/internal/model"
)

// 本文件集中存放用户模块对外响应的结构体，以及从 model 到响应结构体的转换函数。
// 单独成文件是为了让「客户端能看见哪些字段」在目录树里一眼可查，
// 也避免把数据库模型直接交给 response.OK 序列化。

// userResp 是用户模块对外返回的用户信息，注册、登录、个人主页共用一份形状。
// 字段逐个列出，model.User 以后加字段不会自动出现在响应里。
// 这里刻意不写 Password：model.User 的 Password 带 json:"-"，
// 但 DTO 从源头就不给这个字段留位置，少一层依赖运气。
//
// Nickname 在输出前已经做过回退，空昵称会填成 Username，
// 所以客户端不需要自己判空，直接用 nickname 渲染即可。
type userResp struct {
	ID        uint      `json:"id"`
	Username  string    `json:"username"`
	Nickname  string    `json:"nickname"`
	AvatarURL string    `json:"avatar_url"`
	Bio       string    `json:"bio"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// updateProfileReq 是 PUT /users/me 的请求体。
// 三个字段都是指针：没传（nil）= 不改这一项，传空字符串 = 清空这一项。
// 用值类型会把「没传」和「清空」混成同一个零值，前端只想改头像时会把昵称抹掉。
type updateProfileReq struct {
	Nickname  *string `json:"nickname"`
	AvatarURL *string `json:"avatar_url"`
	Bio       *string `json:"bio"`
}

// toProfilePatch 把请求体转成 service 的入参。
// 请求结构与业务入参分开，handler 之外的地方（比如以后的批量导入）不改 DTO 也能复用 service。
func toProfilePatch(req updateProfileReq) ProfilePatch {
	return ProfilePatch{
		Nickname:  req.Nickname,
		AvatarURL: req.AvatarURL,
		Bio:       req.Bio,
	}
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
	// 昵称为空时用用户名兜底，保证响应里的 nickname 永远有可显示的内容。
	nickname := u.Nickname
	if nickname == "" {
		nickname = u.Username
	}
	return userResp{
		ID:        u.ID,
		Username:  u.Username,
		Nickname:  nickname,
		AvatarURL: u.AvatarURL,
		Bio:       u.Bio,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}
