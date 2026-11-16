package user

import (
	"context"
	"testing"
	"time"

	"forum-backend/internal/testutil"
)

// TestLoginGuardLocksAtThreshold 验证前 max-1 次只计数不放锁，第 max 次才锁。
//
// 断言逐次都检查，而不是只看最后一次：如果在第 1 次就把 locked 返回 true，
// 只看末尾那条用例仍然会通过，但用户会被一次手误锁住。
func TestLoginGuardLocksAtThreshold(t *testing.T) {
	guard := NewLoginGuard(testutil.NewRedis(t))
	ctx := context.Background()

	for i := 1; i <= loginMaxAttempts; i++ {
		locked, retryAfter, err := guard.Fail(ctx, "alice")
		if err != nil {
			t.Fatalf("第 %d 次 Fail() error = %v", i, err)
		}
		wantLocked := i >= loginMaxAttempts
		if locked != wantLocked {
			t.Fatalf("第 %d 次 Fail() locked = %v, want %v", i, locked, wantLocked)
		}
		if wantLocked && retryAfter <= 0 {
			t.Fatalf("第 %d 次触发锁定但 retryAfter = %v", i, retryAfter)
		}
	}

	locked, retryAfter, err := guard.Check(ctx, "alice")
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !locked {
		t.Fatal("达到阈值后 Check() 应当是锁定状态")
	}
	if retryAfter <= 0 || retryAfter > loginFailWindow {
		t.Fatalf("retryAfter = %v, want 落在 (0, %v] 之间", retryAfter, loginFailWindow)
	}
}

// TestLoginGuardAllowsBeforeThreshold 验证没失败过、以及只错几次时都放行。
func TestLoginGuardAllowsBeforeThreshold(t *testing.T) {
	guard := NewLoginGuard(testutil.NewRedis(t))
	ctx := context.Background()

	if locked, _, err := guard.Check(ctx, "nobody"); err != nil || locked {
		t.Fatalf("没有失败记录时 Check() = (%v, %v), want (false, nil)", locked, err)
	}

	// 比阈值少一次：还不该锁。
	for i := 1; i < loginMaxAttempts; i++ {
		if _, _, err := guard.Fail(ctx, "bob"); err != nil {
			t.Fatalf("Fail() error = %v", err)
		}
	}
	if locked, _, err := guard.Check(ctx, "bob"); err != nil || locked {
		t.Fatalf("失败 %d 次后 Check() = (%v, %v), want (false, nil)", loginMaxAttempts-1, locked, err)
	}
}

// TestLoginGuardResetClearsFailures 验证成功登录会把计数清零。
// 不清零的话，一天里零散输错几次的人会在毫无预兆的情况下被锁。
func TestLoginGuardResetClearsFailures(t *testing.T) {
	guard := NewLoginGuard(testutil.NewRedis(t))
	ctx := context.Background()

	for i := 0; i < loginMaxAttempts-1; i++ {
		if _, _, err := guard.Fail(ctx, "carol"); err != nil {
			t.Fatalf("Fail() error = %v", err)
		}
	}
	if err := guard.Reset(ctx, "carol"); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if locked, _, err := guard.Check(ctx, "carol"); err != nil || locked {
		t.Fatalf("Reset 后 Check() = (%v, %v), want (false, nil)", locked, err)
	}
}

// TestLoginGuardNormalizesUsername 验证大小写与首尾空格归一到同一个计数器。
//
// 登录查询用的是 MySQL 默认排序规则（不区分大小写），"Alice" 和 "alice" 是同一个账号。
// 计数不归一的话，攻击者大小写轮换就能把 5 次上限变成 2^n 次。
func TestLoginGuardNormalizesUsername(t *testing.T) {
	guard := NewLoginGuard(testutil.NewRedis(t))
	ctx := context.Background()

	for i := 0; i < loginMaxAttempts; i++ {
		if _, _, err := guard.Fail(ctx, "Dave"); err != nil {
			t.Fatalf("Fail() error = %v", err)
		}
	}
	// 换成小写加空格再看：必须仍然是被锁状态。
	locked, _, err := guard.Check(ctx, "  dave ")
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !locked {
		t.Fatal("大小写不同的同一个用户名应当共享计数")
	}
}

// TestLoginGuardResetIsIdempotent 验证清一个不存在的计数不报错。
// 每次成功登录都会调用 Reset，对从没失败过的用户就是空操作。
func TestLoginGuardResetIsIdempotent(t *testing.T) {
	guard := NewLoginGuard(testutil.NewRedis(t))
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := guard.Reset(ctx, "erin"); err != nil {
			t.Fatalf("第 %d 次 Reset() error = %v", i+1, err)
		}
	}
	// 顺便确认参数与锁定时长常量没被写成 0。
	if loginFailWindow <= time.Minute {
		t.Fatalf("loginFailWindow = %v，太短了", loginFailWindow)
	}
}
