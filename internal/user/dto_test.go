package user

import (
	"testing"

	"forum-backend/internal/model"
)

// TestToUserRespFallsBackToUsername 验证昵称为空时响应里的 nickname 用用户名填。
// 老数据（加列前注册的用户）昵称都是空串，前端不该为这种情况各写一遍兜底。
func TestToUserRespFallsBackToUsername(t *testing.T) {
	resp := toUserResp(&model.User{ID: 7, Username: "olduser"})
	if resp.Nickname != "olduser" {
		t.Errorf("nickname = %q，期望回退成 olduser", resp.Nickname)
	}
}

// TestToUserRespKeepsNickname 验证有昵称时不被用户名覆盖。
func TestToUserRespKeepsNickname(t *testing.T) {
	resp := toUserResp(&model.User{ID: 8, Username: "alice", Nickname: "爱丽丝"})
	if resp.Nickname != "爱丽丝" {
		t.Errorf("nickname = %q，期望 爱丽丝", resp.Nickname)
	}
	if resp.Username != "alice" {
		t.Errorf("username = %q，期望 alice", resp.Username)
	}
}
