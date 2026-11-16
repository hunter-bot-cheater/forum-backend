package follow

import (
	"time"
)

// 本文件集中存放关注模块对外响应的结构体。

// followStateResp 是关注/取关之后的状态。
// 带上 follower_count，前端不用再发一次请求就能把主页上的粉丝数改对。
type followStateResp struct {
	Following     bool  `json:"following"`
	FollowerCount int64 `json:"follower_count"`
	Changed       bool  `json:"changed"`
}

// followItemResp 是列表里的一行。
// 只给展示与跳转需要的字段：昵称已经在 user.Brief 里做过回退，前端直接渲染。
// followed_by_me 供粉丝列表画「回关」按钮，在「我的关注」列表里恒为 true。
type followItemResp struct {
	ID           uint      `json:"id"`
	Username     string    `json:"username"`
	Nickname     string    `json:"nickname"`
	AvatarURL    string    `json:"avatar_url"`
	FollowedAt   time.Time `json:"followed_at"`
	FollowedByMe bool      `json:"followed_by_me"`
}

// followListResp 是列表接口的 data 部分。
// List 初始化为空切片，空结果输出 [] 而不是 null。
type followListResp struct {
	List     []followItemResp `json:"list"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

// toFollowStateResp 把 service 的状态转成响应。
func toFollowStateResp(state *State) followStateResp {
	return followStateResp{
		Following:     state.Following,
		FollowerCount: state.FollowerCount,
		Changed:       state.Changed,
	}
}

// toFollowListResp 把 service 的列表转成响应。
func toFollowListResp(items []Item, total int64, page, pageSize int) followListResp {
	list := make([]followItemResp, 0, len(items))
	for _, item := range items {
		list = append(list, followItemResp{
			ID:           item.User.ID,
			Username:     item.User.Username,
			Nickname:     item.User.DisplayName(),
			AvatarURL:    item.User.AvatarURL,
			FollowedAt:   item.FollowedAt,
			FollowedByMe: item.FollowedByMe,
		})
	}
	return followListResp{
		List:     list,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
}
