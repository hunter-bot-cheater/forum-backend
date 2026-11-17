package user

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
)

// 展示资料的字段上限。与 model.User 上的 gorm size 保持一致：
// 校验放在 service 里，是因为 gorm 的 size 在 MySQL 上只决定列宽，
// 超长内容会被静默截断（严格模式下才报错），客户端拿不到明确失败信号。
const (
	maxNicknameRunes = 32
	maxBioRunes      = 200
	maxAvatarURLLen  = 255
)

// ProfilePatch 是个人资料的局部更新入参。
// 三个字段都用指针：nil 表示「这次不动这一项」，空字符串表示「清空」。
// 用值类型就没法区分这两种意图，前端只想改头像时会把昵称一起覆盖成空串。
type ProfilePatch struct {
	Nickname  *string // 昵称
	AvatarURL *string // 头像图片的地址
	Bio       *string // 个人简介
}

// GetByID 按 id 查用户，查不到统一返回 domain.ErrNotFound。
// 关注、私信这些模块要校验「目标用户是否存在」，也走这一个入口，
// 避免每个模块各写一遍 gorm.ErrRecordNotFound 的翻译。
func (s *UserService) GetByID(ctx context.Context, id uint) (*model.User, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: user id is required", domain.ErrInvalidInput)
	}

	var u model.User
	if err := s.db.WithContext(ctx).First(&u, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("find user %d: %w", id, err)
	}
	return &u, nil
}

// UpdateProfile 局部更新当前用户的展示资料，返回更新后的完整记录。
//
// 先逐项校验再落库：校验只针对传了值的字段，没传的字段既不校验也不写。
// 全部字段都是 nil 时直接把当前记录读回来，等价于空操作，不额外发 UPDATE。
func (s *UserService) UpdateProfile(ctx context.Context, userID uint, patch ProfilePatch) (*model.User, error) {
	updates := make(map[string]any, 3)

	// 逐字段处理：先做归一化（去首尾空白），再校验，最后才进 updates。
	// 校验和写库分开，是为了让任何一项不合法时都不会留下「改了半个资料」的状态。
	if patch.Nickname != nil {
		nickname, err := normalizeNickname(*patch.Nickname)
		if err != nil {
			return nil, err
		}
		updates["nickname"] = nickname
	}
	if patch.AvatarURL != nil {
		avatar, err := normalizeAvatarURL(*patch.AvatarURL)
		if err != nil {
			return nil, err
		}
		updates["avatar_url"] = avatar
	}
	if patch.Bio != nil {
		bio, err := normalizeBio(*patch.Bio)
		if err != nil {
			return nil, err
		}
		updates["bio"] = bio
	}

	if len(updates) == 0 {
		return s.GetByID(ctx, userID)
	}

	// 用 map 而不是结构体更新：GORM 用结构体时会跳过零值字段，
	// 导致「把简介改成空串」这种清空操作发不出 SQL。
	// 这里只按 id 定位本人，userID 来自 token，客户端无法指定改谁。
	if err := s.db.WithContext(ctx).Model(&model.User{}).
		Where("id = ?", userID).
		Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update user profile %d: %w", userID, err)
	}

	// 重新读一次再返回：Updates 不会回填 UpdatedAt 以外的东西，
	// 客户端拿到的必须是库里的真值，而不是「我刚才提交了什么」。
	return s.GetByID(ctx, userID)
}

// normalizeNickname 去空白后校验昵称。
// 允许传空串，语义是「清空昵称、展示时回退成用户名」。
func normalizeNickname(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if n := len([]rune(v)); n > maxNicknameRunes {
		return "", fmt.Errorf("%w: nickname must be at most %d characters", domain.ErrInvalidInput, maxNicknameRunes)
	}
	// 换行和控制字符会把列表排版打乱，也可能被前端当成 HTML 片段处理，统一拒绝。
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: nickname contains control characters", domain.ErrInvalidInput)
		}
	}
	return v, nil
}

// normalizeAvatarURL 校验头像地址。三种合法形态：
//   - 空串：清空，前端用名字首字兜底
//   - http/https 绝对地址：老数据还在用，继续收
//   - 以单个 / 开头的站内相对地址：我们自己上传的文件走这条
//
// 前端已经不给「填图片地址」的入口了（只保留上传），
// 这里仍然接受绝对地址是因为库里可能存着早年贴的外链：
// 用户只改昵称时会把 avatar_url 原样带上来，拒掉等于让人没法保存其它字段。
//
// 为什么必须支持站内相对地址：上传接口返回的就是 /uploads/avatars/...
// 这种路径，前端拿到后直接写进 avatar_url。如果只收绝对地址，
// 上传成功却保存失败，用户看到的是「传上去了但头像没变」。
// 也不该在这里把它拼成绝对地址：域名会被钉进用户数据，
// 以后换域名或加 CDN，所有人的头像一起挂掉。
//
// 不接受 "//host/path"：那是协议相对地址，等价于一个外链，
// 却绕过了下面的 scheme/host 校验。
func normalizeAvatarURL(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if len(v) > maxAvatarURLLen {
		return "", fmt.Errorf("%w: avatar_url must be at most %d bytes", domain.ErrInvalidInput, maxAvatarURLLen)
	}

	// 站内相对地址：只允许单个前导斜杠，且不能含空白或控制字符。
	// 没有 scheme 就不可能是 javascript:，同源请求也带不出数据。
	if strings.HasPrefix(v, "/") {
		if strings.HasPrefix(v, "//") {
			return "", fmt.Errorf("%w: avatar_url must not be protocol-relative", domain.ErrInvalidInput)
		}
		for _, r := range v {
			if r <= 0x20 || r == 0x7f {
				return "", fmt.Errorf("%w: avatar_url contains whitespace or control characters", domain.ErrInvalidInput)
			}
		}
		return v, nil
	}

	parsed, err := url.Parse(v)
	if err != nil {
		return "", fmt.Errorf("%w: avatar_url is not a valid url", domain.ErrInvalidInput)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: avatar_url must start with http or https", domain.ErrInvalidInput)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("%w: avatar_url has no host", domain.ErrInvalidInput)
	}
	return v, nil
}

// normalizeBio 去空白后校验个人简介，rune 计数与列宽 200 对应。
func normalizeBio(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if n := len([]rune(v)); n > maxBioRunes {
		return "", fmt.Errorf("%w: bio must be at most %d characters", domain.ErrInvalidInput, maxBioRunes)
	}
	return v, nil
}
