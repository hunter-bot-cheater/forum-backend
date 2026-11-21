package pool

import (
	"errors"
	"testing"
	"time"
)

// newTestInvites 造一份可以手动拨时钟的登记表。
func newTestInvites(now time.Time) *Invites {
	registry := NewInvites()
	registry.nowFunc = func() time.Time { return now }
	return registry
}

// 同一时间只能挂一条待接受的邀请。
func TestInvitesRejectSecondPending(t *testing.T) {
	registry := newTestInvites(time.Unix(1000, 0))

	if _, err := registry.Create(1, 2, 10, "000001"); err != nil {
		t.Fatalf("第一条邀请不该失败：%v", err)
	}
	if _, err := registry.Create(1, 3, 11, "000002"); !errors.Is(err, ErrInviteBusy) {
		t.Fatalf("err = %v，期望 ErrInviteBusy", err)
	}

	// 换个人发就允许：限制是按发起人算的，不是全局。
	if _, err := registry.Create(3, 4, 12, "000003"); err != nil {
		t.Errorf("别人发邀请不该被挡：%v", err)
	}
}

// 邀请过期后从待处理列表里消失，状态变 expired。
func TestInvitesExpire(t *testing.T) {
	now := time.Unix(1000, 0)
	registry := newTestInvites(now)
	invite, _ := registry.Create(1, 2, 10, "000001")

	if len(registry.PendingFor(2)) != 1 {
		t.Fatal("刚发的邀请应当在待处理列表里")
	}

	// 把时钟拨到过期之后。
	registry.nowFunc = func() time.Time { return now.Add(InviteTTL + time.Second) }

	if pending := registry.PendingFor(2); len(pending) != 0 {
		t.Errorf("过期的邀请不该还在列表里，实际 %d 条", len(pending))
	}

	got, ok := registry.Get(invite.ID)
	if !ok {
		t.Fatal("过期后短时间内还应当能查到，好回一句「邀请已失效」")
	}
	if got.Status != InviteExpired {
		t.Errorf("状态 = %s，期望 %s", got.Status, InviteExpired)
	}

	// 已经过期的邀请不能再被接受。
	if registry.SetStatus(invite.ID, InviteAccepted) {
		t.Error("过期邀请不该还能改成已接受")
	}
}

// 清理只删「过期很久」的，刚过期那几十秒还要留着给人看提示。
func TestInvitesSweepKeepsRecent(t *testing.T) {
	now := time.Unix(1000, 0)
	registry := newTestInvites(now)
	registry.Create(1, 2, 10, "000001")

	registry.nowFunc = func() time.Time { return now.Add(InviteTTL + time.Second) }
	if removed := registry.Sweep(); removed != 0 {
		t.Errorf("刚过期就清掉 %d 条，太快了", removed)
	}

	registry.nowFunc = func() time.Time { return now.Add(2*InviteTTL + time.Second) }
	if removed := registry.Sweep(); removed != 1 {
		t.Errorf("清掉 %d 条，期望 1 条", removed)
	}
}

// 只有 pending 能改状态，重复接受不会把已用过的邀请翻出来。
func TestInvitesStatusIsOneWay(t *testing.T) {
	registry := newTestInvites(time.Unix(1000, 0))
	invite, _ := registry.Create(1, 2, 10, "000001")

	if !registry.SetStatus(invite.ID, InviteAccepted) {
		t.Fatal("第一次接受应当成功")
	}
	if registry.SetStatus(invite.ID, InviteAccepted) {
		t.Error("重复接受不该成功")
	}
	if registry.SetStatus("不存在", InviteAccepted) {
		t.Error("不存在的邀请不该能改状态")
	}
}

// 房号必须是 6 位数字，前导 0 不能丢：它是字符串，不是数字。
func TestNewRoomCodeFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		code, err := NewRoomCode()
		if err != nil {
			t.Fatalf("生成房号失败：%v", err)
		}
		if len(code) != 6 {
			t.Fatalf("房号长度 = %d，期望 6（%q）", len(code), code)
		}
		for _, char := range code {
			if char < '0' || char > '9' {
				t.Fatalf("房号里出现非数字字符：%q", code)
			}
		}
	}
}
