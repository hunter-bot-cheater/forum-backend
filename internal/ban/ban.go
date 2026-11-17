// Package ban 管理用户的发言封禁：写入封禁状态、判断当前是否在封禁期内。
//
// 单独成包而不是塞进 user 服务：用它的两头都不在 user 里——
// 举报模块处置时要封人，写操作中间件在每个写请求上都要判一次。
// 让两边都 import user 会把 user 服务的一整套依赖（Redis、关注关系）带进来，
// 而这里真正用到的只是 users 表上的两列。
package ban

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
)

// Store 读写 users 表上的封禁列，不缓存。
//
// 为什么不加 Redis 缓存：封禁是极低频状态，绝大多数用户永远查不到解除时间，
// 缓存命中的收益抵不上「已解封但缓存还没过期」的一致性成本。
// 这里按主键取一列，MySQL 上是微秒级，挂在写请求前面没有可感知的代价。
type Store struct {
	db *gorm.DB
}

// NewStore 注入数据库连接，由 router 在装配期调用。
func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// BannedUntil 返回封禁的解除时间；未封禁或已过期时返回零值。
//
// 过期的封禁在这里就抹平成零值，而不是交给每个调用方自己写 `After(now)`：
// 判断只留一处，中间件与处置逻辑的「是否被封」才不会出现两种口径。
func (s *Store) BannedUntil(ctx context.Context, userID uint) (time.Time, error) {
	var row struct {
		BannedUntil *time.Time
	}
	err := s.db.WithContext(ctx).
		Model(&model.User{}).
		Select("banned_until").
		Where("id = ?", userID).
		Take(&row).Error
	if err != nil {
		// 用户不存在按未封处理：调用它的写请求后面会因为没有作者而失败，
		// 在这里返回错误只会把一个「账号已注销」变成 500。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("load ban state of user %d: %w", userID, err)
	}
	if row.BannedUntil == nil || !row.BannedUntil.After(time.Now()) {
		return time.Time{}, nil
	}
	return *row.BannedUntil, nil
}

// Ban 把用户封到 until，reason 只给管理员排查用，不对外返回。
//
// 重复封禁直接覆盖：管理员改判一个更长的时长是正常操作，
// 不需要先解封再封一次。
func (s *Store) Ban(ctx context.Context, userID uint, until time.Time, reason string) error {
	res := s.db.WithContext(ctx).Model(&model.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{
			"banned_until": until,
			"ban_reason":   reason,
		})
	if res.Error != nil {
		return fmt.Errorf("ban user %d: %w", userID, res.Error)
	}
	// 影响 0 行说明库里没有这个人。举报记录里存的是当时查到的作者 id，
	// 理论上不会走到这里，真出现了说明数据被外部改动过，按 404 处理比静默成功好。
	if res.RowsAffected == 0 {
		return fmt.Errorf("ban user %d: %w", userID, domain.ErrNotFound)
	}
	return nil
}
