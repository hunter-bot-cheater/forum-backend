package user

import (
	"time"

	"forum-backend/internal/model"
)

// 本文件集中存放用户模块对外响应的结构体，以及从 model 到响应结构体的转换函数。
// 单独成文件是为了让「客户端能看见哪些字段」在目录树里一眼可查，
// 也避免把数据库模型直接交给 response.OK 序列化。
//
// 对外只有两种用户形状，别再加第三种：
//   - userResp     资料本体。注册、登录用，刚拿到身份，还没到主页。
//   - profileResp  资料本体 + 关系计数。个人主页用，看自己看别人都一样。
//
// 不合并成一个：登录时去数关注数是白花两次 COUNT，
// 而主页少了计数前端就得自己再发一次请求。

// userResp 是用户资料本体，注册、登录接口用它。
// 字段逐个列出，model.User 以后加字段不会自动出现在响应里。
// 这里刻意不写 Password：model.User 的 Password 带 json:"-"，
// 但 DTO 从源头就不给这个字段留位置，少一层依赖运气。
//
// 关于 Nickname 与 HasCustomNickname 的关系，见下面 toUserResp 的注释。
type userResp struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatar_url"`
	Bio       string `json:"bio"`
	// HasCustomNickname 表示用户在库里真的设过昵称。
	// Nickname 是展示名（空时已回退成 Username），光看它分不清
	// 「用户设了昵称且刚好等于用户名」和「用户没设昵称」。
	// 个人中心的编辑表单靠这个布尔值决定输入框回填昵称还是回填空串。
	HasCustomNickname bool      `json:"has_custom_nickname"`
	CreatedAt         time.Time `json:"created_at"`
	// 没有 UpdatedAt：它是资料最后修改时间，对用户没有意义，
	// 暴露出去等于把「这个人多久动一次资料」告诉外部。
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
//
// 这里给的是 userResp 而不是 profileResp：登录只负责发身份，
// 前端进个人中心时调 /users/me 拿带计数的版本，不需要在登录流程里多查两次 COUNT。
type loginResp struct {
	Token string   `json:"token"`
	User  userResp `json:"user"`
}

// profileResp 是个人主页的 data：资料本体 + 关注统计。
//
// 用内嵌而不是把字段抄一遍：userResp 以后再加字段（比如等级、签名），
// 这里自动跟着变，不会出现「主页少一个字段」的漏改。
// 序列化时内嵌结构体的字段会平铺到同一层，前端看到的仍是一个扁平对象。
type profileResp struct {
	userResp
	FollowingCount int64 `json:"following_count"`
	FollowerCount  int64 `json:"follower_count"`
	// IsFollowing 表示当前登录用户是否关注了这个人，未登录时固定 false。
	IsFollowing bool `json:"is_following"`
}

// toProfileResp 在 toUserResp 的基础上补上关注统计。
// 统计值由调用方查好再传进来，这个函数本身不发任何 SQL：
// 一旦允许它在里面查库，它就从纯转换退化成带副作用的组装器，
// 调用方也没法再用假数据测响应形状。
func toProfileResp(u *model.User, following, followers int64, isFollowing bool) profileResp {
	return profileResp{
		userResp:       toUserResp(u),
		FollowingCount: following,
		FollowerCount:  followers,
		IsFollowing:    isFollowing,
	}
}

// toUserResp 把数据库模型转换成对外结构体。
// 转换只发生在 handler 层：service 继续只认识 model.User，
// 不需要 import handler 包，依赖方向保持单向。
//
// 昵称为空时用用户名兜底，保证响应里的 nickname 永远有可显示的内容。
// 这是展示语义，不是数据语义：库里存的仍然是空串，
// 所以同时输出 has_custom_nickname，让编辑表单能还原「用户到底设过没有」。
func toUserResp(u *model.User) userResp {
	return userResp{
		ID:                u.ID,
		Username:          u.Username,
		Nickname:          displayName(u.Nickname, u.Username),
		AvatarURL:         u.AvatarURL,
		Bio:               u.Bio,
		HasCustomNickname: u.Nickname != "",
		CreatedAt:         u.CreatedAt,
	}
}
