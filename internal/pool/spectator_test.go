package pool

import (
	"errors"
	"testing"
	"time"

	"forum-backend/internal/pool/sim"
)

// 观战者拿到的是中立视角：joined 与快照里的座位都是 SeatSpectator。
//
// 这两处必须一致。只改一处的话，观战页会从快照里读到一个 0 或 1，
// 于是把某一方的球当成「我的球」，界面上看着像坐错了座位。
func TestWatchSendsSpectatorSeat(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	watcher := &fakeConn{}
	if err := match.Watch(watcher); err != nil {
		t.Fatalf("观战进房失败：%v", err)
	}

	kinds := watcher.types()
	if len(kinds) < 2 || kinds[0] != MsgJoined || kinds[1] != MsgState {
		t.Fatalf("前两条应当是 joined、state，实际 %v", kinds)
	}

	joined, _ := watcher.last(MsgJoined)
	if joined.Data.(JoinedData).Seat != SeatSpectator {
		t.Errorf("joined 里的座位 = %d，期望 %d", joined.Data.(JoinedData).Seat, SeatSpectator)
	}

	state, _ := watcher.last(MsgState)
	snapshot := state.Data.(Snapshot)
	if snapshot.Seat != SeatSpectator {
		t.Errorf("快照里的座位 = %d，期望 %d", snapshot.Seat, SeatSpectator)
	}
	// 中立视角也要能看到双方是谁，否则观战页画不出顶部的两块名牌。
	if snapshot.Players[Seat0].Nickname != "甲" || snapshot.Players[Seat1].Nickname != "乙" {
		t.Errorf("快照里的双方资料不对：%+v", snapshot.Players)
	}
}

// 一杆的广播要同时到达两个玩家与观战者。
//
// 观战者如果只拿得到进房那一份快照，之后画面就停在第 0 杆。
func TestWatchReceivesShotBroadcast(t *testing.T) {
	match, conn0, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	watcher := &fakeConn{}
	if err := match.Watch(watcher); err != nil {
		t.Fatalf("观战进房失败：%v", err)
	}

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 0); err != nil {
		t.Fatalf("出杆失败：%v", err)
	}

	if _, ok := watcher.last(MsgShot); !ok {
		t.Fatal("观战者没收到 shot")
	}
	// 一杆结束还会补一份快照：观战者也要收到，否则定组与手中球停在上一杆。
	state, ok := watcher.last(MsgState)
	if !ok {
		t.Fatal("观战者没收到一杆结束的快照")
	}
	if state.Data.(Snapshot).ShotSeq != 1 {
		t.Errorf("快照里的杆数 = %d，期望 1", state.Data.(Snapshot).ShotSeq)
	}
	// 观战不影响出杆的人：射手手里那条连接照常收到自己的 shot。
	if _, ok := conn0.last(MsgShot); !ok {
		t.Error("射手没收到 shot")
	}
}

// 观战者进房不该惊动玩家。
//
// 玩家那条连接上的 peer 事件表示「对手回来了 / 掉线了」，
// 观战者进出如果也发 peer，玩家会看到「对方回来了」而后台其实没人回来。
func TestWatchDoesNotNotifyPlayers(t *testing.T) {
	match, conn0, conn1 := newTestMatch(t, &fakeSim{shot: quietShot()})

	before0 := len(conn0.types())
	before1 := len(conn1.types())

	if err := match.Watch(&fakeConn{}); err != nil {
		t.Fatalf("观战进房失败：%v", err)
	}

	if len(conn0.types()) != before0 || len(conn1.types()) != before1 {
		t.Errorf("观战进房后玩家收到了消息：座位0 %v，座位1 %v", conn0.types(), conn1.types())
	}
}

// 观战名额是硬上限：第 11 个人进不来，且报的是可识别的错误。
func TestWatchStopsAtCapacity(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	for index := 0; index < maxSpectators; index += 1 {
		if err := match.Watch(&fakeConn{}); err != nil {
			t.Fatalf("第 %d 个观战者就被拒了：%v", index+1, err)
		}
	}
	if match.SpectatorCount() != maxSpectators {
		t.Fatalf("观战人数 = %d，期望 %d", match.SpectatorCount(), maxSpectators)
	}

	if err := match.Watch(&fakeConn{}); !errors.Is(err, ErrSpectatorFull) {
		t.Fatalf("第 %d 个观战者应当被拒成 ErrSpectatorFull，实际 %v", maxSpectators+1, err)
	}
}

// 观战者断线只从名单里摘掉，并且腾出名额。
//
// 反过来（断线不清理）的后果是：一场对局挂过十个人之后，
// 后来的人再点观战永远是「人数已满」，直到这局结束。
func TestSpectatorDisconnectFreesSlot(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	watcher := &fakeConn{}
	if err := match.Watch(watcher); err != nil {
		t.Fatalf("观战进房失败：%v", err)
	}
	if match.SpectatorCount() != 1 {
		t.Fatalf("观战人数 = %d，期望 1", match.SpectatorCount())
	}

	if err := match.NotifyDisconnect(SeatSpectator, watcher); err != nil {
		t.Fatalf("观战断线处理失败：%v", err)
	}
	if match.SpectatorCount() != 0 {
		t.Fatalf("断线后观战人数 = %d，期望 0", match.SpectatorCount())
	}

	// 名额真的腾出来了：后续观战者能进来，且摘掉的不是别人。
	next := &fakeConn{}
	if err := match.Watch(next); err != nil {
		t.Fatalf("腾出名额后仍然进不来：%v", err)
	}
	// 已经断开的那条连接不该再收到广播。
	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 0); err != nil {
		t.Fatalf("出杆失败：%v", err)
	}
	if _, ok := watcher.last(MsgShot); ok {
		t.Error("已断开的观战连接仍收到广播")
	}
	if _, ok := next.last(MsgShot); !ok {
		t.Error("新加入的观战者没收到广播")
	}
}

// 观战者没有回合，出杆必须被拒，且不能改动任何状态。
func TestSpectatorCannotShoot(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})
	if err := match.Watch(&fakeConn{}); err != nil {
		t.Fatalf("观战进房失败：%v", err)
	}

	if err := match.Shoot(SeatSpectator, sim.CueParams{Phi: 1.5, Power: 0.6}, 0); !errors.Is(err, ErrNotYourTurn) {
		t.Fatalf("观战者出杆应当被拒成 ErrNotYourTurn，实际 %v", err)
	}

	snapshot, err := match.Snapshot(SeatSpectator)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	if snapshot.ShotSeq != 0 || snapshot.Turn != Seat0 {
		t.Errorf("观战者的无效出杆改动了状态：%+v", snapshot)
	}
}

// 观战不进房、不算准备：只有观战者时对局仍然停在房间阶段。
//
// 准备状态是按座位记的，观战者没有座位，理论上碰不到；
// 这条用例守的是「将来若有人给观战者加座位，会不会把开局条件算进去」。
func TestWatchDoesNotStartRoom(t *testing.T) {
	state := newRoomState(42, "654321", [2]PlayerBrief{{UserID: 1, Nickname: "甲"}, {UserID: 2, Nickname: "乙"}})
	match := NewMatch(state, Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	t.Cleanup(match.Close)

	watcher := &fakeConn{}
	if err := match.Watch(watcher); err != nil {
		t.Fatalf("观战进房失败：%v", err)
	}

	snapshot, err := match.Snapshot(SeatSpectator)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	if snapshot.Status != "waiting" {
		t.Errorf("对局状态 = %q，应当仍是 waiting", snapshot.Status)
	}
	if snapshot.Ready[Seat0] || snapshot.Ready[Seat1] {
		t.Errorf("观战把准备状态改了：%+v", snapshot.Ready)
	}
}
