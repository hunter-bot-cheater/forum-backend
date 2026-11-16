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

// TestToUserRespHasCustomNickname 验证「设过昵称」和「没设昵称」能被区分开。
//
// nickname 是展示名，空昵称会被回退成用户名，所以光看它无法判断用户设过没有。
// 用户在编辑表单里把昵称清成空串之后，前端必须能从响应里看出来，
// 否则输入框会一直回填用户名，用户永远表达不了「我要清空」。
func TestToUserRespHasCustomNickname(t *testing.T) {
	cases := []struct {
		name        string
		nickname    string
		wantDisplay string
		wantCustom  bool
	}{
		{"没设昵称", "", "olduser", false},
		{"设了昵称", "老王", "老王", true},
		{"昵称恰好等于用户名", "olduser", "olduser", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := toUserResp(&model.User{ID: 9, Username: "olduser", Nickname: tc.nickname})
			if resp.Nickname != tc.wantDisplay {
				t.Errorf("nickname = %q，期望 %q", resp.Nickname, tc.wantDisplay)
			}
			if resp.HasCustomNickname != tc.wantCustom {
				t.Errorf("has_custom_nickname = %v，期望 %v", resp.HasCustomNickname, tc.wantCustom)
			}
		})
	}
}
