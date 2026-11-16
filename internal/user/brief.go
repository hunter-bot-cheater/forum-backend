package user

import (
	"context"
	"fmt"

	"forum-backend/internal/model"
)

// Brief 是「展示一个用户」所需的最小字段集。
// 关注列表、粉丝列表、私信会话、通知列表都要显示对方是谁，
// 各自去查 users 表会写出四份一样的 SELECT，字段口径也会慢慢分叉，
// 所以统一从这里取。
//
// 刻意不带 bio 和时间字段：列表里用不到，带上只会让每行响应变大。
type Brief struct {
	ID        uint
	Username  string
	Nickname  string
	AvatarURL string
}

// DisplayName 返回列表上该显示的名字。
// 昵称为空就回退到用户名，保证前端任何位置都能直接渲染，不用自己判空。
func (b Brief) DisplayName() string {
	if b.Nickname != "" {
		return b.Nickname
	}
	return b.Username
}

// NewBrief 把数据库模型裁成 Brief。
func NewBrief(u *model.User) Brief {
	return Brief{
		ID:        u.ID,
		Username:  u.Username,
		Nickname:  u.Nickname,
		AvatarURL: u.AvatarURL,
	}
}

// Briefs 按 id 批量取展示信息，返回 id 到 Brief 的映射。
//
// 一次 IN 查询代替 N 次单查：分页列表每页最多 50 条，
// 逐条查会变成 1 + 50 次往返，批量查是固定 2 次。
// 不存在的 id 直接不出现在结果里，调用方取值时判 ok，
// 这样「用户注销后通知仍在」这类历史数据不会把整页请求打成 500。
func (s *UserService) Briefs(ctx context.Context, ids []uint) (map[uint]Brief, error) {
	out := make(map[uint]Brief, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	// 去重并过滤 0：调用方常把「文章 id + 用户 id」混在一起传，
	// 重复值不会改变结果，只会让 IN 列表变长。
	uniq := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out, nil
	}

	// 只选展示要用的列，避免把 password 哈希读进内存。
	var rows []model.User
	err := s.db.WithContext(ctx).
		Select("id", "username", "nickname", "avatar_url").
		Where("id IN ?", uniq).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("load user briefs: %w", err)
	}

	// 遍历结果而不是遍历 uniq：查不到的 id 自然不进 map。
	for i := range rows {
		out[rows[i].ID] = NewBrief(&rows[i])
	}
	return out, nil
}
