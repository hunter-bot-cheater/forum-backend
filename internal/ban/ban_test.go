package ban

import (
	"context"
	"errors"
	"testing"
	"time"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
)

// TestBannedUntilReadsCurrentState 验证未封、封禁中、已过期三种状态各自的结果。
//
// 「已过期」这一支必须自己算：没有定时任务去清库里的旧值，
// 解封完全依赖读取时和当前时间比一次。这一步写错，用户会被永久锁住。
func TestBannedUntilReadsCurrentState(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	ctx := context.Background()
	u := testutil.SeedUser(t, db, "alice")

	until, err := store.BannedUntil(ctx, u.ID)
	if err != nil {
		t.Fatalf("读取未封禁状态失败: %v", err)
	}
	if !until.IsZero() {
		t.Fatalf("未封禁时解除时间 = %v，期望零值", until)
	}

	future := time.Now().Add(time.Hour)
	if err := store.Ban(ctx, u.ID, future, "test"); err != nil {
		t.Fatalf("封禁失败: %v", err)
	}
	until, err = store.BannedUntil(ctx, u.ID)
	if err != nil {
		t.Fatalf("读取封禁状态失败: %v", err)
	}
	if until.IsZero() {
		t.Fatal("封禁中却返回了零值")
	}

	// 直接把解除时间改到过去，模拟「时间走过了封禁期」。
	// 不调 Ban 是因为它只接受未来的时间语义，这里要的是库里的旧值。
	if err := db.Model(&model.User{}).Where("id = ?", u.ID).
		Update("banned_until", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("改写解除时间失败: %v", err)
	}
	until, err = store.BannedUntil(ctx, u.ID)
	if err != nil {
		t.Fatalf("读取过期封禁失败: %v", err)
	}
	if !until.IsZero() {
		t.Fatalf("过期封禁应视为未封，实际返回 %v", until)
	}
}

// TestBanMissingUser 验证封一个不存在的 id 会报 404。
// 静默成功会让管理员误以为处理完了，而实际上什么都没发生。
func TestBanMissingUser(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)

	err := store.Ban(context.Background(), 99999, time.Now().Add(time.Hour), "test")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误 = %v，期望 ErrNotFound", err)
	}
}
