package user

import (
	"context"
	"testing"

	"forum-backend/internal/testutil"
)

// TestBriefsDedupesAndSkipsMissing 验证批量取展示信息时：
// 重复 id 只查一次、id=0 被过滤、不存在的 id 不出现在结果里。
// 最后一条是通知列表能容错的历史数据场景（用户注销后通知还在）。
func TestBriefsDedupesAndSkipsMissing(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewUserService(db)

	a := testutil.SeedUser(t, db, "alice")
	b := testutil.SeedUser(t, db, "bob")
	// 给 b 设昵称，验证 Brief 带的是展示资料而不是用户名。
	nickname := "波波"
	if _, err := svc.UpdateProfile(context.Background(), b.ID, ProfilePatch{Nickname: &nickname}); err != nil {
		t.Fatalf("设置昵称失败: %v", err)
	}

	got, err := svc.Briefs(context.Background(), []uint{a.ID, a.ID, b.ID, 0, 999999})
	if err != nil {
		t.Fatalf("批量取展示信息失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("结果条数 = %d，期望 2", len(got))
	}
	if got[a.ID].DisplayName() != "alice" {
		t.Errorf("a 的展示名 = %q，期望回退成用户名 alice", got[a.ID].DisplayName())
	}
	if got[b.ID].DisplayName() != "波波" {
		t.Errorf("b 的展示名 = %q，期望昵称 波波", got[b.ID].DisplayName())
	}
	// 用户名也要一并返回：前端做 @提及 或跳转主页时需要稳定标识。
	if got[b.ID].Username != "bob" {
		t.Errorf("b 的 username = %q", got[b.ID].Username)
	}
}

// TestBriefsEmptyInput 验证空入参直接返回空 map，不会拼出 IN () 这种非法 SQL。
func TestBriefsEmptyInput(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewUserService(db)

	got, err := svc.Briefs(context.Background(), nil)
	if err != nil {
		t.Fatalf("空入参应当成功: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("结果条数 = %d，期望 0", len(got))
	}
}
