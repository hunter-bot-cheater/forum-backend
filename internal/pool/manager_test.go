package pool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Manager 的清扫与列举，用例都围绕「什么时候能被收掉」。
//
// 不收拾的后果是内存只增不减：一晚上几百局全部留在 map 里，
// 每局还挂着十几颗球的位置与至少一条已经断开的连接记录。

// finishedMatch 造一局并让它结算（座位 0 主动退出，座位 1 胜）。
func finishedMatch(t *testing.T, manager *Manager, matchID uint) *Match {
	t.Helper()

	state := newMatchState(matchID, "000123", [2]PlayerBrief{
		{UserID: matchID*2 + 1, Nickname: "甲"},
		{UserID: matchID*2 + 2, Nickname: "乙"},
	})
	match := manager.Create(state)
	if err := match.Join(Seat0, &fakeConn{}); err != nil {
		t.Fatalf("进房失败：%v", err)
	}
	if err := match.Leave(Seat0); err != nil {
		t.Fatalf("退出失败：%v", err)
	}
	return match
}

// 已经结束超过保留期的对局会被收掉。
func TestSweepReapsFinishedMatches(t *testing.T) {
	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	match := finishedMatch(t, manager, 71)
	manager.sweep(time.Minute)

	// 刚结束的还在保留期内，不该被收。
	if _, ok := manager.Get(71); !ok {
		t.Fatal("刚结束的对局不该立刻被收掉")
	}

	// 把结束时间推到保留期之外再扫一次。
	match.finishedAtMu.Lock()
	match.finishedAt = time.Now().Add(-2 * time.Minute)
	match.finishedAtMu.Unlock()

	manager.sweep(time.Minute)
	if _, ok := manager.Get(71); ok {
		t.Fatal("超过保留期的对局应当被收掉")
	}
}

// 还在打的对局永远不被收，哪怕它已经开了很久。
//
// 判据是「结束多久」而不是「开了多久」：一局可以打到十分钟，
// 按创建时间收会把正在进行的对局清掉，玩家下一杆直接拿到「对局已结束」。
func TestSweepKeepsPlayingMatches(t *testing.T) {
	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	state := newMatchState(72, "000124", [2]PlayerBrief{{UserID: 1}, {UserID: 2}})
	manager.Create(state)

	manager.sweep(0)
	if _, ok := manager.Get(72); !ok {
		t.Fatal("正在进行的对局被收掉了")
	}
}

// 没人理的房间要被收掉：点一下台球就开一间，关掉页面之后不该永远留着。
//
// 判据是「最近一次有人跟它说话」多久之前，而不是「建了多久」：
// 双方在房间里等的时候一个连接都没有，只能靠轮询打点。
func TestSweepReapsAbandonedRooms(t *testing.T) {
	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	abandoned := manager.Create(newRoomState(81, "000181", [2]PlayerBrief{{UserID: 5}}))
	manager.Create(newRoomState(82, "000182", [2]PlayerBrief{{UserID: 6}}))

	// 把 81 的活跃时间推到保留期之外，82 保持刚建好的样子。
	atomic.StoreInt64(&abandoned.lastActive, time.Now().Add(-2*time.Minute).UnixNano())

	manager.sweep(time.Minute)
	if _, ok := manager.Get(81); ok {
		t.Error("超过保留期没人理的房间应当被收掉")
	}
	if _, ok := manager.Get(82); !ok {
		t.Error("刚建好的房间被收掉了")
	}
}

// 有人还在轮询的房间不许收，哪怕它已经开了很久。
//
// 快照请求就是打点：房间页每 1.5 秒调一次，一分钟内一定有一次。
func TestSweepKeepsRoomThatKeepsPolling(t *testing.T) {
	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	match := manager.Create(newRoomState(83, "000183", [2]PlayerBrief{{UserID: 7}}))
	// 先把活跃时间推到很久以前，再让一次快照把它拉回来。
	atomic.StoreInt64(&match.lastActive, time.Now().Add(-2*time.Minute).UnixNano())
	if _, err := match.Snapshot(Seat0); err != nil {
		t.Fatalf("取快照失败：%v", err)
	}

	manager.sweep(time.Minute)
	if _, ok := manager.Get(83); !ok {
		t.Fatal("有人还在轮询的房间被收掉了")
	}
}

// Janitor 在 ctx 取消后要退出，不能留一个 goroutine 在那里空转。
func TestJanitorStopsOnContextCancel(t *testing.T) {
	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	finishedMatch(t, manager, 73)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.Janitor(ctx, 5*time.Millisecond, 0)
		close(done)
	}()

	// 等它把对局扫掉，再取消。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := manager.Get(73); !ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := manager.Get(73); ok {
		t.Fatal("Janitor 没有收掉已结束的对局")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Janitor 没有退出")
	}
}

// 同一个 match id 重复创建会顶掉旧的那一局，旧局的连接被关掉。
func TestCreateReplacesOldMatch(t *testing.T) {
	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	state := newMatchState(74, "000125", [2]PlayerBrief{{UserID: 1}, {UserID: 2}})
	old := manager.Create(state)
	oldConn := &fakeConn{}
	if err := old.Join(Seat0, oldConn); err != nil {
		t.Fatalf("进房失败：%v", err)
	}

	manager.Create(state)
	// 旧 actor 退出时会关掉它名下的连接。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		oldConn.mu.Lock()
		closed := oldConn.closed
		oldConn.mu.Unlock()
		if closed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	oldConn.mu.Lock()
	defer oldConn.mu.Unlock()
	if !oldConn.closed {
		t.Fatal("旧对局的连接没有被关掉")
	}
}

// List 要能一次拿到内存里的全部对局，观战列表靠它枚举。
func TestListReturnsEveryMatch(t *testing.T) {
	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	for index := 0; index < 3; index += 1 {
		state := newMatchState(uint(80+index), "000126", [2]PlayerBrief{{UserID: 1}, {UserID: 2}})
		manager.Create(state)
	}

	list := manager.List()
	if len(list) != 3 {
		t.Fatalf("List 返回 %d 局，期望 3 局", len(list))
	}
	for _, match := range list {
		if _, err := match.Snapshot(SeatSpectator); err != nil {
			t.Errorf("取快照失败：%v", err)
		}
	}
}
