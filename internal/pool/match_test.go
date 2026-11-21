package pool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"forum-backend/internal/pool/sim"
)

// fakeConn 记录收到的每一条消息，供用例断言广播了什么。
type fakeConn struct {
	mu       sync.Mutex
	messages []Message
	closed   bool
}

func (c *fakeConn) Send(msg Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, msg)
	return true
}

func (c *fakeConn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// types 返回收到的消息类型序列。
func (c *fakeConn) types() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	kinds := make([]string, 0, len(c.messages))
	for _, msg := range c.messages {
		kinds = append(kinds, msg.Type)
	}
	return kinds
}

// last 返回某一类消息的最后一条。
func (c *fakeConn) last(kind string) (Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := len(c.messages) - 1; index >= 0; index-- {
		if c.messages[index].Type == kind {
			return c.messages[index], true
		}
	}
	return Message{}, false
}

// fakeSim 是可控的仿真：想让它返回什么就返回什么，也可以让它失败。
type fakeSim struct {
	mu     sync.Mutex
	shot   *sim.Shot
	err    error
	inputs []sim.Request
}

func (f *fakeSim) Shoot(_ context.Context, req sim.Request) (*sim.Shot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, req)
	if f.err != nil {
		return nil, f.err
	}
	return f.shot, nil
}

func (f *fakeSim) lastRequest() (sim.Request, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inputs) == 0 {
		return sim.Request{}, false
	}
	return f.inputs[len(f.inputs)-1], true
}

// quietShot 是一杆「母球碰了一下 9 号、没有进球、9 号碰库」的结果：
// 合法但不进球，所以一定会换人，适合当多数用例的基准。
func quietShot() *sim.Shot {
	return &sim.Shot{
		Duration: 1.2,
		Events: []sim.Event{
			{T: 0.1, Type: sim.EventBallBall, A: sim.CueBallID, B: "9"},
			{T: 0.5, Type: sim.EventCushion, A: "9", B: "lc"},
		},
		Final: []sim.ResultBall{
			{ID: sim.CueBallID, X: 0.5, Y: 0.9, OnTable: true},
			{ID: "9", X: 0.6, Y: 1.2, OnTable: true},
		},
		Frames: []sim.Frame{{T: 0, Balls: []byte(`[["cue",0.49,0.99,0]]`)}},
		SimMS:  12,
	}
}

// newTestMatch 起一局用于测试的对局，超时设得很长，避免干扰。
func newTestMatch(t *testing.T, fake *fakeSim) (*Match, *fakeConn, *fakeConn) {
	t.Helper()

	state := newMatchState(41, "123456", [2]PlayerBrief{{UserID: 1, Nickname: "甲"}, {UserID: 2, Nickname: "乙"}})
	match := NewMatch(state, Options{Sim: fake, TurnTimeout: time.Minute})
	t.Cleanup(match.Close)

	conn0 := &fakeConn{}
	conn1 := &fakeConn{}
	if err := match.Join(Seat0, conn0); err != nil {
		t.Fatalf("座位 0 进房失败：%v", err)
	}
	if err := match.Join(Seat1, conn1); err != nil {
		t.Fatalf("座位 1 进房失败：%v", err)
	}
	return match, conn0, conn1
}

// 进房要收到 joined 与 state，顺序不能反：前端要先用 joined 里的座位号认自己。
func TestJoinSendsJoinedThenState(t *testing.T) {
	match, conn0, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	kinds := conn0.types()
	if len(kinds) < 2 || kinds[0] != MsgJoined || kinds[1] != MsgState {
		t.Fatalf("前两条应当是 joined、state，实际 %v", kinds)
	}

	snapshot, err := match.Snapshot(Seat0)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	if snapshot.Seat != Seat0 || snapshot.MatchID != 41 {
		t.Errorf("快照里的座位或对局号不对：%+v", snapshot)
	}
	if len(snapshot.Balls) != len(StandardRack) {
		t.Errorf("开局球数 = %d，期望 %d", len(snapshot.Balls), len(StandardRack))
	}
}

// 一杆合法但不进球：广播 shot 与 turn，回合交给对手。
func TestShootBroadcastsShotAndPassesTurn(t *testing.T) {
	fake := &fakeSim{shot: quietShot()}
	match, conn0, conn1 := newTestMatch(t, fake)

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 0); err != nil {
		t.Fatalf("出杆失败：%v", err)
	}

	if _, ok := conn0.last(MsgShot); !ok {
		t.Error("射手没收到 shot")
	}
	if _, ok := conn1.last(MsgShot); !ok {
		t.Error("对手也要收到 shot，否则两边画面不一致")
	}
	turnMsg, ok := conn0.last(MsgTurn)
	if !ok {
		t.Fatal("没有 turn 消息")
	}
	if turnMsg.Data.(TurnData).Seat != Seat1 {
		t.Errorf("回合应当交给座位 1，实际 %d", turnMsg.Data.(TurnData).Seat)
	}

	snapshot, _ := match.Snapshot(Seat0)
	if snapshot.Turn != Seat1 || snapshot.ShotSeq != 1 {
		t.Errorf("快照没跟上：turn=%d seq=%d", snapshot.Turn, snapshot.ShotSeq)
	}
}

// 一杆结束后必须再补一份完整快照。
//
// 这条用例守的是「增量帧不带定组与手中球」这个坑：只发 shot/foul/turn 时，
// 前端进球之后仍显示未定组、犯规之后拿不到 ball_in_hand，
// 于是没法落位母球，对局就卡在「出杆被拒」上。
func TestShootBroadcastsSnapshotAfterShot(t *testing.T) {
	fake := &fakeSim{shot: quietShot()}
	match, conn0, _ := newTestMatch(t, fake)

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 0); err != nil {
		t.Fatalf("出杆失败：%v", err)
	}

	kinds := conn0.types()
	last := kinds[len(kinds)-1]
	if last != MsgState {
		t.Fatalf("一杆的最后一条消息应当是 state，实际是 %q（全部：%v）", last, kinds)
	}

	snapshot, ok := conn0.last(MsgState)
	if !ok {
		t.Fatal("没有 state 消息")
	}
	// 快照里的轮次要与 turn 帧一致，否则前端按快照重建场景会得到另一个人。
	if snapshot.Data.(Snapshot).Turn != Seat1 {
		t.Errorf("快照里的轮次 = %d，期望 1", snapshot.Data.(Snapshot).Turn)
	}
}

// 发给 pool-sim 的球位只含台面上的球，母球也在里面。
func TestShootSendsOnlyBallsOnTable(t *testing.T) {
	fake := &fakeSim{shot: quietShot()}
	match, _, _ := newTestMatch(t, fake)

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 0); err != nil {
		t.Fatalf("出杆失败：%v", err)
	}

	request, ok := fake.lastRequest()
	if !ok {
		t.Fatal("没有发出仿真请求")
	}
	if len(request.Balls) != len(StandardRack) {
		t.Errorf("球数 = %d，期望 %d", len(request.Balls), len(StandardRack))
	}

	found := false
	for _, item := range request.Balls {
		if item.ID == sim.CueBallID {
			found = true
		}
	}
	if !found {
		t.Error("请求里没有母球")
	}
}

// 不是你的回合就不许出杆。
func TestShootRejectsOutOfTurn(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	err := match.Shoot(Seat1, sim.CueParams{Phi: 1.5, Power: 0.6}, 0)
	if !errors.Is(err, ErrNotYourTurn) {
		t.Fatalf("err = %v，期望 ErrNotYourTurn", err)
	}
}

// 参数越界直接拒，不要浪费一次跨语言调用。
func TestShootRejectsBadParams(t *testing.T) {
	fake := &fakeSim{shot: quietShot()}
	match, _, _ := newTestMatch(t, fake)

	cases := []sim.CueParams{
		{Power: 1.5},
		{Power: -0.1},
		{Power: 0.5, A: 1.2},
		{Power: 0.5, B: -1.5},
		{Power: 0.5, Theta: 2},
	}
	for _, params := range cases {
		if err := match.Shoot(Seat0, params, 0); !errors.Is(err, ErrBadParams) {
			t.Errorf("参数 %+v 应当被拒，实际 err=%v", params, err)
		}
	}

	if _, ok := fake.lastRequest(); ok {
		t.Error("参数非法时不该发出仿真请求")
	}
}

// 仿真失败：状态不动、回合还在自己手里、错误帧交给 WS 层统一发。
//
// actor 不自己发 error：它出错时会 return，WS 层按 error 码回一条。
// 两边都发的话玩家会连着看到两条一模一样的「球桌暂时算不动」。
func TestShootSurvivesSimFailure(t *testing.T) {
	fake := &fakeSim{err: sim.ErrUnavailable}
	match, conn0, _ := newTestMatch(t, fake)

	err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 0)
	if !errors.Is(err, ErrSimFailed) {
		t.Fatalf("err = %v，期望 ErrSimFailed", err)
	}

	if _, ok := conn0.last(MsgError); ok {
		t.Error("actor 不该自己发 error 帧，交给 WS 层发")
	}

	snapshot, _ := match.Snapshot(Seat0)
	if snapshot.Turn != Seat0 || snapshot.ShotSeq != 0 {
		t.Errorf("失败的一杆不该改状态：turn=%d seq=%d", snapshot.Turn, snapshot.ShotSeq)
	}
}

// 犯规要广播 foul，并且把手中球标出来。
func TestShootBroadcastsFoul(t *testing.T) {
	// 母球落袋：开局没定组，母球进袋就是犯规。
	fake := &fakeSim{shot: &sim.Shot{
		Events: []sim.Event{
			{T: 0.1, Type: sim.EventBallBall, A: sim.CueBallID, B: "9"},
			{T: 0.9, Type: sim.EventBallPocket, A: sim.CueBallID, B: "lc"},
		},
		Final: []sim.ResultBall{
			{ID: sim.CueBallID, OnTable: false},
			{ID: "9", X: 0.6, Y: 1.2, OnTable: true},
		},
	}}
	match, conn0, _ := newTestMatch(t, fake)

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 0); err != nil {
		t.Fatalf("出杆失败：%v", err)
	}

	foulMsg, ok := conn0.last(MsgFoul)
	if !ok {
		t.Fatal("没有 foul 消息")
	}
	data := foulMsg.Data.(FoulData)
	if data.Code != FoulCuePocketed || !data.BallInHand {
		t.Errorf("犯规信息不对：%+v", data)
	}

	snapshot, _ := match.Snapshot(Seat1)
	if !snapshot.BallInHand || snapshot.Turn != Seat1 {
		t.Errorf("应当换人并给手中球：%+v", snapshot)
	}
}

// 手里握着球不能出杆，必须先落位。
func TestShootRequiresCuePlacement(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	// 先制造一个犯规，让球权带上手中球。
	match.state.BallInHand = true
	match.state.Turn = Seat0

	err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 0)
	if !errors.Is(err, ErrMustPlaceCue) {
		t.Fatalf("err = %v，期望 ErrMustPlaceCue", err)
	}
}

// 摆球：台外坐标拒掉，合法坐标落位并清掉手中球标记。
func TestPlaceCue(t *testing.T) {
	match, conn0, _ := newTestMatch(t, &fakeSim{shot: quietShot()})
	match.state.BallInHand = true

	if err := match.PlaceCue(Seat0, -0.5, 0.5); !errors.Is(err, ErrBadParams) {
		t.Errorf("台外坐标应当被拒，实际 %v", err)
	}
	if err := match.PlaceCue(Seat0, 0.5, 0.5); err != nil {
		t.Fatalf("合法坐标不该被拒：%v", err)
	}

	snapshot, _ := match.Snapshot(Seat0)
	if snapshot.BallInHand {
		t.Error("落位之后手中球标记应当清掉")
	}
	for _, item := range snapshot.Balls {
		if item.ID != sim.CueBallID {
			continue
		}
		if !item.OnTable || item.X != 0.5 || item.Y != 0.5 {
			t.Errorf("母球没落到位：%+v", item)
		}
	}
	if _, ok := conn0.last(MsgState); !ok {
		t.Error("落位后要广播快照，否则对手画面上球还在袋里")
	}
}

// 没手中球时摆球要被拒，避免摆出一个「凭空出现」的母球。
func TestPlaceCueRequiresBallInHand(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	if err := match.PlaceCue(Seat0, 0.5, 0.5); !errors.Is(err, ErrNotBallInHand) {
		t.Fatalf("err = %v，期望 ErrNotBallInHand", err)
	}
}

// 断线重连：新连接顶上同一个座位，先收 joined 再收完整快照，然后能接着出杆。
//
// 这是 M6 的服务端一半：断线不判负、对局继续走读秒、重连靠一份快照收敛。
func TestRejoinAfterDisconnect(t *testing.T) {
	match, conn0, conn1 := newTestMatch(t, &fakeSim{shot: quietShot()})

	// 座位 0 掉线：对手要收到 offline，否则他会以为是自己卡住了。
	if err := match.NotifyDisconnect(Seat0, conn0); err != nil {
		t.Fatalf("断线通知失败：%v", err)
	}
	peerMsg, ok := conn1.last(MsgPeer)
	if !ok {
		t.Fatal("对手没有收到掉线通知")
	}
	if peerMsg.Data.(PeerData).Event != PeerOffline {
		t.Errorf("peer 事件 = %s，期望 %s", peerMsg.Data.(PeerData).Event, PeerOffline)
	}

	// 重连：新连接顶掉旧的那条。
	fresh := &fakeConn{}
	if err := match.Join(Seat0, fresh); err != nil {
		t.Fatalf("重连失败：%v", err)
	}
	kinds := fresh.types()
	// joined 必须在 state 前面：前端要先用它认自己坐哪边。
	if len(kinds) < 2 || kinds[0] != MsgJoined || kinds[1] != MsgState {
		t.Fatalf("重连后应当先 joined 再 state，实际 %v", kinds)
	}

	stateMsg, _ := fresh.last(MsgState)
	snapshot := stateMsg.Data.(Snapshot)
	if snapshot.Seat != Seat0 {
		t.Errorf("快照里的座位 = %d，期望 0", snapshot.Seat)
	}
	if snapshot.Status != "playing" {
		t.Errorf("状态 = %s，期望 playing（断线不该判负）", snapshot.Status)
	}
	if len(snapshot.Balls) != len(StandardRack) {
		t.Errorf("快照里有 %d 颗球，期望 %d 颗", len(snapshot.Balls), len(StandardRack))
	}

	// 接着出杆：轮次没被断线弄丢。
	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 1); err != nil {
		t.Fatalf("重连后出杆失败：%v", err)
	}
}

// 旧连接的退出回调晚到一步时，不能把已经重连上来的新连接一起清掉。
func TestDisconnectOfOldConnKeepsNewOne(t *testing.T) {
	match, conn0, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	fresh := &fakeConn{}
	if err := match.Join(Seat0, fresh); err != nil {
		t.Fatalf("重连失败：%v", err)
	}
	// 旧连接此时才报断线。
	if err := match.NotifyDisconnect(Seat0, conn0); err != nil {
		t.Fatalf("断线通知失败：%v", err)
	}

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 1); err != nil {
		t.Fatalf("重连后出杆失败：%v", err)
	}
	// 新连接必须收到这一杆：被旧回调误清的话这里什么也收不到。
	if _, ok := fresh.last(MsgShot); !ok {
		t.Error("新连接没收到这一杆，说明它被旧连接的断线回调清掉了")
	}
}

// 主动退出判负。
func TestLeaveLosesTheMatch(t *testing.T) {
	match, conn0, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	if err := match.Leave(Seat0); err != nil {
		t.Fatalf("退出失败：%v", err)
	}

	resultMsg, ok := conn0.last(MsgResult)
	if !ok {
		t.Fatal("没有结算消息")
	}
	if resultMsg.Data.(ResultData).Winner != Seat1 {
		t.Errorf("退出方应当输，赢家 = %d", resultMsg.Data.(ResultData).Winner)
	}

	snapshot, _ := match.Snapshot(Seat0)
	if snapshot.Status != "finished" || snapshot.WinReason != WinLeave {
		t.Errorf("状态没结算：%+v", snapshot)
	}
}

// 超时一次换人；连续两次判负。
func TestTimeoutSwitchesThenLoses(t *testing.T) {
	fake := &fakeSim{shot: quietShot()}
	state := newMatchState(7, "654321", [2]PlayerBrief{{UserID: 1}, {UserID: 2}})
	match := NewMatch(state, Options{Sim: fake, TurnTimeout: 30 * time.Millisecond})
	t.Cleanup(match.Close)

	conn0 := &fakeConn{}
	conn1 := &fakeConn{}
	_ = match.Join(Seat0, conn0)
	_ = match.Join(Seat1, conn1)

	// 第一次超时：座位 0 丢回合，座位 1 接手。
	waitFor(t, 2*time.Second, func() bool {
		snapshot, _ := match.Snapshot(Seat0)
		return snapshot.Turn == Seat1
	})
	foulMsg, ok := conn0.last(MsgFoul)
	if !ok {
		t.Fatal("超时应当广播 foul")
	}
	if foulMsg.Data.(FoulData).Code != "timeout" {
		t.Errorf("原因码 = %s，期望 timeout", foulMsg.Data.(FoulData).Code)
	}

	// foul 与 turn 都是增量帧，都不带「手中球」这一项。
	// 少补一份完整快照，接手方会看到自己的回合却出不了杆
	// （服务端按 ErrMustPlaceCue 拒掉、状态又不变），界面就此卡死。
	stateMsg, ok := conn0.last(MsgState)
	if !ok {
		t.Fatal("超时换人后没有广播快照")
	}
	afterTimeout := stateMsg.Data.(Snapshot)
	if afterTimeout.Turn != Seat1 {
		t.Errorf("快照里的回合 = %d，期望换成座位 1", afterTimeout.Turn)
	}

	// 第二次超时落在座位 1 身上，这是它第一次，所以还是换人。
	waitFor(t, 2*time.Second, func() bool {
		snapshot, _ := match.Snapshot(Seat0)
		return snapshot.Turn == Seat0
	})

	// 座位 0 第二次超时 —— 中间它没出过杆，因此判负。
	waitFor(t, 4*time.Second, func() bool {
		snapshot, _ := match.Snapshot(Seat0)
		return snapshot.Status == "finished"
	})

	snapshot, _ := match.Snapshot(Seat0)
	if snapshot.Winner == nil || *snapshot.Winner != Seat1 {
		t.Errorf("连续超时应当判负，赢家 = %v", snapshot.Winner)
	}
	if snapshot.WinReason != WinTimeout {
		t.Errorf("WinReason = %s，期望 %s", snapshot.WinReason, WinTimeout)
	}
}

// 并发出杆：同一时刻只有一个人能成功，-race 下不能出现状态竞争。
func TestConcurrentShootsOnlyOneWins(t *testing.T) {
	fake := &fakeSim{shot: quietShot()}
	match, _, _ := newTestMatch(t, fake)

	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0

	for index := 0; index < 10; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, 0)
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// 第一杆成功后回合交给座位 1，后面的九杆都该被 ErrNotYourTurn 挡掉。
	if success != 1 {
		t.Errorf("成功出杆 %d 次，期望 1 次", success)
	}

	snapshot, _ := match.Snapshot(Seat0)
	if snapshot.ShotSeq != 1 {
		t.Errorf("ShotSeq = %d，期望 1", snapshot.ShotSeq)
	}
}

// Close 之后所有调用都要返回 ErrMatchClosed，而不是 panic 或永久阻塞。
func TestCloseStopsTheActor(t *testing.T) {
	match, conn0, _ := newTestMatch(t, &fakeSim{shot: quietShot()})
	match.Close()

	select {
	case <-match.Done():
	case <-time.After(time.Second):
		t.Fatal("Close 之后 actor 没退出")
	}

	if !conn0.closed {
		t.Error("关闭时应当断开所有连接")
	}
	if err := match.Shoot(Seat0, sim.CueParams{Power: 0.5}, 0); !errors.Is(err, ErrMatchClosed) {
		t.Errorf("err = %v，期望 ErrMatchClosed", err)
	}
}

// waitFor 轮询等待条件成立，超时就失败。用于观察异步的读秒行为。
func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时（%s）", timeout)
}
