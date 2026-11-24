package pool

import (
	"context"
	"errors"
	"math"
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

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0); err != nil {
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

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0); err != nil {
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

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0); err != nil {
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

	err := match.Shoot(Seat1, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0)
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
		if err := match.Shoot(Seat0, params, nil, 0); !errors.Is(err, ErrBadParams) {
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

	err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0)
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

// clientShotFrom 用一份快照里的球拼出「客户端算好的一杆」。
//
// final 与开始时的球一一对应、轨迹非空、时长在区间内，是 validateShotPayload
// 的硬要求；这里按最正常的形状造，用例只改自己要破坏的那一项。
func clientShotFrom(balls []sim.ResultBall, cueX, cueY float64) *sim.Shot {
	final := make([]sim.ResultBall, 0, len(balls))
	for _, ball := range balls {
		if !ball.OnTable {
			continue
		}
		x, y := ball.X, ball.Y
		if ball.ID == sim.CueBallID {
			x, y = cueX, cueY
		}
		final = append(final, sim.ResultBall{ID: ball.ID, X: x, Y: y, OnTable: true})
	}
	return &sim.Shot{
		Duration: 1.4,
		Events: []sim.Event{
			{T: 0.1, Type: sim.EventBallBall, A: sim.CueBallID, B: "9"},
			{T: 0.6, Type: sim.EventCushion, A: "9", B: ""},
		},
		Final:  final,
		Frames: []sim.Frame{{T: 0, Balls: []byte(`[["cue",0.49,0.99,0]]`)}},
	}
}

// 带了客户端结果的出杆必须直接采用它：不再调仿真，广播的就是它给的帧。
//
// 这是「本地即权威」的核心约定。仿真故意设成失败：一旦代码又走回现算那条路，
// 这一杆会直接报 ErrSimFailed，用例立刻红。
func TestShootAdoptsClientShot(t *testing.T) {
	fake := &fakeSim{err: sim.ErrUnavailable}
	match, conn0, _ := newTestMatch(t, fake)

	snapshot, err := match.Snapshot(Seat0)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	payload := clientShotFrom(snapshot.Balls, 0.42, 0.6)

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, payload, 7); err != nil {
		t.Fatalf("出杆失败：%v", err)
	}
	if _, ok := fake.lastRequest(); ok {
		t.Error("带了客户端结果就不该再调仿真")
	}

	msg, ok := conn0.last(MsgShot)
	if !ok {
		t.Fatal("没有广播 shot")
	}
	data, ok := msg.Data.(ShotData)
	if !ok {
		t.Fatalf("shot 的数据类型不对：%T", msg.Data)
	}
	if len(data.Frames) != len(payload.Frames) || len(data.Events) != len(payload.Events) {
		t.Errorf("广播的轨迹或事件数与客户端给的不一致：帧 %d/%d 事件 %d/%d",
			len(data.Frames), len(payload.Frames), len(data.Events), len(payload.Events))
	}
	// 客户端算的杆不该报仿真耗时：这个数字一旦混进指标，物理服务快慢就彻底看不出来了。
	if data.SimMS != 0 {
		t.Errorf("SimMS = %d，期望 0", data.SimMS)
	}

	// 终局要真的落到状态上：下一杆的起点就是这份 final。
	after, err := match.Snapshot(Seat0)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	found := false
	for _, ball := range after.Balls {
		if ball.ID != sim.CueBallID {
			continue
		}
		found = true
		if ball.X != 0.42 || ball.Y != 0.6 {
			t.Errorf("母球终局 = (%.3f, %.3f)，期望 (0.42, 0.6)", ball.X, ball.Y)
		}
	}
	if !found {
		t.Error("快照里没有母球")
	}
}

// 形状不对的客户端结果一律拒，而且不能改动状态。
func TestShootRejectsBadClientShot(t *testing.T) {
	match, conn0, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	snapshot, err := match.Snapshot(Seat0)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}

	cases := []struct {
		name   string
		mutate func(*sim.Shot)
	}{
		{"没有轨迹", func(s *sim.Shot) { s.Frames = nil }},
		{"轨迹帧数超上限", func(s *sim.Shot) {
			s.Frames = make([]sim.Frame, maxShotFrames+1)
		}},
		{"时长超上限", func(s *sim.Shot) { s.Duration = maxShotSeconds + 1 }},
		{"时长是负数", func(s *sim.Shot) { s.Duration = -0.5 }},
		{"坐标是 NaN", func(s *sim.Shot) { s.Final[0].X = math.NaN() }},
		{"坐标是 Inf", func(s *sim.Shot) { s.Final[0].Y = math.Inf(1) }},
		{"坐标差了一个数量级", func(s *sim.Shot) { s.Final[0].X = 990.6 }},
		{"少报一颗球", func(s *sim.Shot) { s.Final = s.Final[1:] }},
		{"多报一颗不存在的球", func(s *sim.Shot) {
			s.Final = append(s.Final, sim.ResultBall{ID: "99", X: 0.5, Y: 0.5, OnTable: true})
		}},
		{"同一颗球报两次", func(s *sim.Shot) {
			s.Final[len(s.Final)-1] = s.Final[0]
		}},
	}

	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			shot := clientShotFrom(snapshot.Balls, 0.42, 0.6)
			item.mutate(shot)
			err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, shot, 0)
			if !errors.Is(err, ErrBadParams) {
				t.Fatalf("err = %v，期望 ErrBadParams", err)
			}
		})
	}

	// 一杆都不该落地：被拒的请求既不能广播，也不能把序号推上去。
	if _, ok := conn0.last(MsgShot); ok {
		t.Error("被拒的一杆不该广播 shot")
	}
	after, _ := match.Snapshot(Seat0)
	if after.ShotSeq != 0 || after.Turn != Seat0 {
		t.Errorf("被拒的一杆改了状态：seq=%d turn=%d", after.ShotSeq, after.Turn)
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

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0); err != nil {
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

// 摆球：台外坐标拒掉，合法坐标落位；落位不收回手中球，可以接着挪。
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
	if !snapshot.BallInHand || !snapshot.CuePlaced {
		t.Errorf("落位后应当留着手中球并记下已摆过：%+v", snapshot)
	}
	// 摆完还得能再挪：这是「同一颗球摆两次」的核心，服务端不能再拒。
	if err := match.PlaceCue(Seat0, 0.5, 0.7); err != nil {
		t.Fatalf("重复落位不该被拒：%v", err)
	}
	for _, item := range snapshot.Balls {
		if item.ID != sim.CueBallID {
			continue
		}
		if !item.OnTable || item.X != 0.5 || item.Y != 0.5 {
			t.Errorf("母球没落到位：%+v", item)
		}
		// z 必须是 0：母球落袋后 z 是袋里的负值，不归零的话它留在台面以下，
		// pooltool 会抛 argmin of an empty sequence，落位之后永远出不了杆。
		if item.Z != 0 {
			t.Errorf("母球 z = %v，期望 0", item.Z)
		}
	}
	if _, ok := conn0.last(MsgState); !ok {
		t.Error("落位后要广播快照，否则对手画面上球还在袋里")
	}

	// 摆过之后就能出杆了：闸门看的是 CuePlaced，不是 BallInHand。
	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0); err != nil {
		t.Fatalf("摆好之后出杆不该被拒：%v", err)
	}
}

// 母球不能摆在别的球身上。
//
// 允许的话，快照里就是「两颗球叠在一起」，下一步物理还要先花一步把它们挤开；
// 玩家看到的是渲染坏了，实际是摆出了一个非法球位。恰好相切仍然是合法的。
func TestPlaceCueRejectsOverlap(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})
	match.state.BallInHand = true

	// 顶点球在 (0.4953, 1.4859)，把母球摆在它球心上。
	if err := match.PlaceCue(Seat0, 0.4953, 1.4859); !errors.Is(err, ErrBadParams) {
		t.Fatalf("压在别的球上应当被拒，实际 %v", err)
	}
	// 贴着摆（球心距 2R 再多 2 毫米）是合法的：这条线不能画得太紧，
	// 真实摆位里「紧贴着一颗球」是常见需求。
	if err := match.PlaceCue(Seat0, 0.4953, 1.4859-ballRadius*2-0.002); err != nil {
		t.Fatalf("贴着别的球摆不该被拒：%v", err)
	}
}

// 母球还在袋里（没摆过）时出杆要被拒：那颗球的 z 是负值，物理算不出来。
func TestShootRequiresCuePlacement(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})
	match.state.BallInHand = true
	match.state.Turn = Seat0
	// 摔袋：球位留着，但已经不在台面上了。
	cue, ok := match.state.ballByID(sim.CueBallID)
	if !ok {
		t.Fatal("初始状态里没有母球")
	}
	cue.OnTable = false
	cue.Z = -0.05
	match.state.setBall(cue)

	err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0)
	if !errors.Is(err, ErrMustPlaceCue) {
		t.Fatalf("err = %v，期望 ErrMustPlaceCue", err)
	}
}

// 手里有母球、但它本来就摆在台上（犯规没摔袋）时，直接出杆是合法的。
//
// 「手中球」是「可以挪」不是「必须挪」：这里一旦拦下来，玩家看到的是台上
// 明明有颗白球却被告知「请先把母球放到台面上」，只能理解成程序坏了。
func TestShootAllowsCueOnTableWithBallInHand(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})
	match.state.BallInHand = true
	match.state.CuePlaced = false
	match.state.Turn = Seat0

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0); err != nil {
		t.Fatalf("母球在台上时不该要求先落位，实际 %v", err)
	}
}

// 没手中球时摆球要被拒，避免摆出一个「凭空出现」的母球。
func TestPlaceCueRequiresBallInHand(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	if err := match.PlaceCue(Seat0, 0.5, 0.5); !errors.Is(err, ErrNotBallInHand) {
		t.Fatalf("err = %v，期望 ErrNotBallInHand", err)
	}
}

// newWaitingMatch 造一间双方都已进房、还没点准备的对局。
//
// 走 SetReady 这条路才能真正经过 handleReady，也就是「开局发手中球」的那一处：
// 直接用 newMatchState 造出来的对局跳过了开局，看不到开球规则。
func newWaitingMatch(t *testing.T, fake *fakeSim) (*Match, *fakeConn, *fakeConn) {
	t.Helper()

	state := newRoomState(42, "654322", [2]PlayerBrief{{UserID: 1, Nickname: "甲"}, {UserID: 2, Nickname: "乙"}})
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
	for _, seat := range []Seat{Seat0, Seat1} {
		if err := match.SetReady(seat, true); err != nil {
			t.Fatalf("座位 %d 准备失败：%v", seat, err)
		}
	}
	return match, conn0, conn1
}

// 开球给开球方手中球，但只准摆在开球线之后。
//
// 摆球表里那颗母球坐标只是一个默认点：钉死它，玩家会觉得「白球不听话」。
// 线外（更靠近球堆那侧）必须拒掉，否则「开球线」只是画着好看。
func TestBreakAllowsPlacingBehindHeadString(t *testing.T) {
	match, _, _ := newWaitingMatch(t, &fakeSim{shot: quietShot()})

	snapshot, err := match.Snapshot(Seat0)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	if !snapshot.BallInHand || snapshot.PlaceZone != PlaceBehindHead {
		t.Fatalf("开球应当是开球线后的手中球：%+v", snapshot)
	}

	// 线外：贴着球堆那一侧，越界。
	if err := match.PlaceCue(Seat0, 0.5, cueHeadString+0.05); !errors.Is(err, ErrBadParams) {
		t.Errorf("开球线外落位应当被拒，实际 %v", err)
	}
	// 线内：靠近开球端那一侧，怎么摆都行。
	if err := match.PlaceCue(Seat0, 0.3, cueHeadString-0.2); err != nil {
		t.Fatalf("开球线后落位不该被拒：%v", err)
	}

	// 开球打出去之后限制就该收回：之后的自由球都是整张台面。
	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0); err != nil {
		t.Fatalf("出杆失败：%v", err)
	}
	after, err := match.Snapshot(Seat1)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	if after.PlaceZone != PlaceAnywhere {
		t.Errorf("开球后的可摆区域 = %q，期望 %q", after.PlaceZone, PlaceAnywhere)
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
	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 1); err != nil {
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

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 1); err != nil {
		t.Fatalf("重连后出杆失败：%v", err)
	}
	// 新连接必须收到这一杆：被旧回调误清的话这里什么也收不到。
	if _, ok := fresh.last(MsgShot); !ok {
		t.Error("新连接没收到这一杆，说明它被旧连接的断线回调清掉了")
	}
}

// 旧连接的断线回调晚到，不能让对手看到「他掉线了」。
//
// 重连之后旧回调才来是常态（close 是异步的）。照样发一条 peer offline，
// 对方会一直挂着「对手掉线」的提示，直到下一次有人进出房间才被覆盖——
// 而那期间他其实正坐在对面。
func TestStaleDisconnectDoesNotAnnounceOffline(t *testing.T) {
	match, conn0, conn1 := newTestMatch(t, &fakeSim{shot: quietShot()})

	fresh := &fakeConn{}
	if err := match.Join(Seat0, fresh); err != nil {
		t.Fatalf("重连失败：%v", err)
	}
	before := len(conn1.types())

	if err := match.NotifyDisconnect(Seat0, conn0); err != nil {
		t.Fatalf("断线通知失败：%v", err)
	}

	if len(conn1.types()) != before {
		t.Errorf("对手收到了消息：%v", conn1.types())
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
			err := match.Shoot(Seat0, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, 0)
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
	if err := match.Shoot(Seat0, sim.CueParams{Power: 0.5}, nil, 0); !errors.Is(err, ErrMatchClosed) {
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

// 瞄准帧要转给对面与观战者，但不能回显给自己。
//
// 回显没有意义：本地的杆子已经是这个角度了，收到一条旧帧只会让它抖一下。
func TestRelayAimReachesOpponentAndSpectators(t *testing.T) {
	match, conn0, conn1 := newTestMatch(t, &fakeSim{shot: quietShot()})
	watcher := &fakeConn{}
	if err := match.Watch(watcher); err != nil {
		t.Fatalf("观战进房失败：%v", err)
	}

	match.RelayAim(Seat0, 1.25, 0.42, 0.6, -0.35)

	// RelayAim 是异步投递的（不占调用方），这里等它落到 actor 里再断言。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := conn1.last(MsgAim); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	msg, ok := conn1.last(MsgAim)
	if !ok {
		t.Fatal("对手没收到 aim 帧")
	}
	data, ok := msg.Data.(AimData)
	if !ok {
		t.Fatalf("aim 帧的数据类型 = %T", msg.Data)
	}
	if data.Seat != Seat0 || data.Phi != 1.25 || data.Power != 0.42 {
		t.Errorf("aim 帧内容不对：%+v", data)
	}
	// 击球点要跟角度、力度同帧搬运，否则对面看到的加塞会停在上一帧。
	if data.A != 0.6 || data.B != -0.35 {
		t.Errorf("aim 帧的击球点不对：%+v", data)
	}
	if _, ok := watcher.last(MsgAim); !ok {
		t.Error("观战者没收到 aim 帧")
	}
	if _, ok := conn0.last(MsgAim); ok {
		t.Error("不该把 aim 帧回显给出杆的人")
	}
}

// 不是自己回合时发来的瞄准帧一律丢弃，否则上一手留下的杆子会一直挂着。
func TestRelayAimIgnoredWhenNotYourTurn(t *testing.T) {
	match, _, conn1 := newTestMatch(t, &fakeSim{shot: quietShot()})

	// 开局是座位 0 的回合，座位 1 发来的帧应当被丢掉。
	match.RelayAim(Seat1, 0.5, 0.9, 0, 0)
	time.Sleep(80 * time.Millisecond)

	if _, ok := conn1.last(MsgAim); ok {
		t.Error("对手回合之外的瞄准帧不该被转发")
	}
}

// 聊天要转给对面与观战者，但不回显给自己。
//
// 发送方那边是本地先落进列表的，再收一份只会让同一句话闪一下。
func TestRelayChatReachesOpponentNotSender(t *testing.T) {
	match, conn0, conn1 := newTestMatch(t, &fakeSim{shot: quietShot()})
	watcher := &fakeConn{}
	if err := match.Watch(watcher); err != nil {
		t.Fatalf("观战进房失败：%v", err)
	}

	match.RelayChat(Seat0, "这杆有点险")

	// RelayChat 与 RelayAim 一样是异步投递，等它落到 actor 里再断言。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := conn1.last(MsgChat); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	msg, ok := conn1.last(MsgChat)
	if !ok {
		t.Fatal("对手没收到聊天")
	}
	data, ok := msg.Data.(ChatData)
	if !ok {
		t.Fatalf("聊天的数据类型 = %T", msg.Data)
	}
	if data.Seat != Seat0 || data.Text != "这杆有点险" {
		t.Errorf("聊天内容不对：%+v", data)
	}
	// 编号要递增，客户端拿它当列表 key。
	if data.ID == 0 {
		t.Error("聊天没有编号")
	}
	if _, ok := watcher.last(MsgChat); !ok {
		t.Error("观战者没收到聊天")
	}
	if _, ok := conn0.last(MsgChat); ok {
		t.Error("不该把聊天回显给发送方")
	}
}

// 同一座位连发要挡掉：按住回车不能刷屏。
//
// 间隔是包级常量（700ms），测试里两次调用之间不 sleep，第二条必然被丢。
func TestRelayChatRateLimited(t *testing.T) {
	match, _, conn1 := newTestMatch(t, &fakeSim{shot: quietShot()})

	match.RelayChat(Seat0, "第一条")
	match.RelayChat(Seat0, "第二条")
	time.Sleep(120 * time.Millisecond)

	msg, ok := conn1.last(MsgChat)
	if !ok {
		t.Fatal("第一条聊天没转发")
	}
	data := msg.Data.(ChatData)
	if data.Text != "第一条" {
		t.Errorf("转发的是 %q，应当是第一条", data.Text)
	}
}

// 空消息直接丢：前端也会拦，但客户端不可信。
func TestRelayChatDroppedWhenEmpty(t *testing.T) {
	match, _, conn1 := newTestMatch(t, &fakeSim{shot: quietShot()})

	match.RelayChat(Seat0, "")
	time.Sleep(80 * time.Millisecond)

	if _, ok := conn1.last(MsgChat); ok {
		t.Error("空的聊天不该被转发")
	}
}
