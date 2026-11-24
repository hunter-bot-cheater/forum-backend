package pool

import (
	"math"
	"testing"
	"time"

	"forum-backend/internal/pool/sim"
)

// botStateFor 造一份可以算球路的状态：母球与指定的几颗球摆在给定位置。
//
// 不需要完整的 16 颗球：算球路只读「在台面上的球」，摆几颗就是几颗。
func botStateFor(t *testing.T, bot Seat, cue sim.ResultBall, balls ...sim.ResultBall) MatchState {
	t.Helper()

	state := newMatchState(99, "654321", [2]PlayerBrief{{UserID: 1, Nickname: "甲"}, BotPlayer})
	state.Bot = bot
	state.BreakDone = true
	state.Balls = append([]sim.ResultBall{cue}, balls...)
	return state
}

// TestBotPlansAimsAtPocket 检查一条标准的「球对着袋口」的线能被算出来。
//
// 母球在中线上，1 号球在它正上方、与左上角袋成一条直线——这一杆存在且好打。
func TestBotPlansAimsAtPocket(t *testing.T) {
	cue := sim.ResultBall{ID: sim.CueBallID, X: 0.4953, Y: 0.40, OnTable: true}
	ball := sim.ResultBall{ID: "1", X: 0.30, Y: 1.30, OnTable: true}
	state := botStateFor(t, Seat1, cue, ball)
	match := NewMatch(state, Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	t.Cleanup(match.Close)

	plans := match.botPlans()
	if len(plans) == 0 {
		t.Fatal("球对着袋口却一套方案都没算出来")
	}
	// 最好的那条应该朝左上角袋（lt，坐标 -0.018 / 1.999）那一带打。
	best := plans[0]
	if best.params.Power < botMinPower || best.params.Power > botMaxPower {
		t.Fatalf("力度 %v 不在 %v~%v 之间", best.params.Power, botMinPower, botMaxPower)
	}
	// 目标球在左上，ghost 点也在左上，所以 phi 应当落在第二象限。
	if math.Cos(best.params.Phi) >= 0 || math.Sin(best.params.Phi) <= 0 {
		t.Fatalf("杆方向 %v 弧度没有指向左上方的目标球", best.params.Phi)
	}
}

// TestBotPlansAvoidsWrongGroup 检查定组之后机器人只打自己那组。
//
// 1 号（全色）正对袋口但它是对手的球，机器人拿的是花色组：
// 打它属于先碰错球，方案里不该出现。
func TestBotPlansAvoidsWrongGroup(t *testing.T) {
	cue := sim.ResultBall{ID: sim.CueBallID, X: 0.4953, Y: 0.40, OnTable: true}
	solid := sim.ResultBall{ID: "1", X: 0.30, Y: 1.30, OnTable: true}
	stripe := sim.ResultBall{ID: "9", X: 0.75, Y: 1.55, OnTable: true}
	state := botStateFor(t, Seat1, cue, solid, stripe)
	state.Groups = [2]BallGroup{GroupSolids, GroupStripes}

	match := NewMatch(state, Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	t.Cleanup(match.Close)

	targets := match.botTargets()
	if len(targets) != 1 || targets[0] != "9" {
		t.Fatalf("目标球 = %v，期望只剩自己的 9 号", targets)
	}
	for _, plan := range match.botPlans() {
		// 9 号在右上，所以杆方向必须朝右（cos > 0）。
		if math.Cos(plan.params.Phi) <= 0 {
			t.Fatalf("杆方向 %v 弧度指向了对手那颗 1 号球", plan.params.Phi)
		}
	}
}

// TestBotPlansFallsBackToSafety 检查一条能进的线都没有时退回安全球。
//
// 母球与目标球之间横着一排对手的球：不管瞄哪个袋口，母球都会先撞到它们。
// 挡路的球刻意选花色（对手的），否则它们自己也会变成可以打的目标，
// 用例就测不到「无路可走」这一支了。
func TestBotPlansFallsBackToSafety(t *testing.T) {
	cue := sim.ResultBall{ID: sim.CueBallID, X: 0.4953, Y: 0.40, OnTable: true}
	target := sim.ResultBall{ID: "1", X: 0.4953, Y: 1.70, OnTable: true}
	blockA := sim.ResultBall{ID: "9", X: 0.35, Y: 0.90, OnTable: true}
	blockB := sim.ResultBall{ID: "10", X: 0.4953, Y: 0.90, OnTable: true}
	blockC := sim.ResultBall{ID: "11", X: 0.64, Y: 0.90, OnTable: true}
	state := botStateFor(t, Seat1, cue, target, blockA, blockB, blockC)
	state.Groups = [2]BallGroup{GroupStripes, GroupSolids}

	match := NewMatch(state, Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	t.Cleanup(match.Close)

	plans := match.botPlans()
	if len(plans) != 1 {
		t.Fatalf("期望只有一套保底方案，拿到 %d 套", len(plans))
	}
	if plans[0].score != 0 {
		t.Fatalf("保底方案的分数应当是 0，拿到 %v", plans[0].score)
	}
	if plans[0].params.Power != botMinPower {
		t.Fatalf("保底力度应当是 %v，拿到 %v", botMinPower, plans[0].params.Power)
	}
}

// TestBotShootsAfterPlayer 检查轮到机器人时它会自己出杆。
//
// 玩家打一杆合法但不进球的球，轮次交给机器人；机器人想一会儿之后
// 应当再调用一次物理服务。
func TestBotShootsAfterPlayer(t *testing.T) {
	fake := &fakeSim{shot: quietShot()}
	state := newBotState(77, "111222", PlayerBrief{UserID: 1, Nickname: "甲"})
	match := NewMatch(state, Options{Sim: fake, TurnTimeout: time.Minute})
	t.Cleanup(match.Close)

	conn := &fakeConn{}
	if err := match.Join(Seat0, conn); err != nil {
		t.Fatalf("座位 0 进房失败：%v", err)
	}
	// 玩家出杆：这一杆不进球，轮次交给机器人。
	if err := match.Shoot(Seat0, sim.CueParams{Phi: math.Pi / 2, Power: 0.5}, nil, 1); err != nil {
		t.Fatalf("玩家出杆失败：%v", err)
	}

	waitFor(t, 6*time.Second, func() bool { return simCalls(fake) >= 2 })

	snapshot, err := match.Snapshot(Seat0)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	// 机器人也打了一杆 quietShot（同样不进球），轮次已经还回玩家。
	if snapshot.Turn != Seat0 {
		t.Fatalf("机器人出杆之后轮次应当是玩家，拿到座位 %d", snapshot.Turn)
	}
	if snapshot.ShotSeq < 2 {
		t.Fatalf("应当打完两杆，shot_seq = %d", snapshot.ShotSeq)
	}
}

// TestBotWaitsForShotAnimation 检查机器人不会在玩家那边球还没停时就出杆。
//
// 客户端按 0.8 倍速播一杆，quietShot 物理时长 1.2 秒 → 玩家屏幕上要放 1.5 秒，
// 再加上「想一想」的 1.0~1.3 秒，机器人最早也要 2.5 秒后才动。
// 1.6 秒这个探针点卡在两者之间：没有「等动画播完」这段延迟时，机器人
// 0.9~1.4 秒就出杆了，这里会抓到第二次物理调用。
func TestBotWaitsForShotAnimation(t *testing.T) {
	fake := &fakeSim{shot: quietShot()}
	state := newBotState(78, "111222", PlayerBrief{UserID: 1, Nickname: "甲"})
	match := NewMatch(state, Options{Sim: fake, TurnTimeout: time.Minute})
	t.Cleanup(match.Close)

	conn := &fakeConn{}
	if err := match.Join(Seat0, conn); err != nil {
		t.Fatalf("座位 0 进房失败：%v", err)
	}
	if err := match.Shoot(Seat0, sim.CueParams{Phi: math.Pi / 2, Power: 0.5}, nil, 1); err != nil {
		t.Fatalf("玩家出杆失败：%v", err)
	}

	// 这一杆在客户端 1.5 秒才播完，1.6 秒时机器人一定还没出手。
	time.Sleep(1600 * time.Millisecond)
	if calls := simCalls(fake); calls != 1 {
		t.Fatalf("1.6 秒内物理服务被调用了 %d 次，机器人应当等这一杆播完", calls)
	}
	waitFor(t, 6*time.Second, func() bool { return simCalls(fake) >= 2 })
}

// TestHumanMatchHasNoBot 检查普通对局不会被机器人接管。
//
// Bot 字段的零值是座位 0，漏写「没有机器人」的话，两个人打得好好的
// 对局会突然自己出杆——这条用例就是那天那个 bug 的守门人。
func TestHumanMatchHasNoBot(t *testing.T) {
	fake := &fakeSim{shot: quietShot()}
	match, _, _ := newTestMatch(t, fake)

	if err := match.Shoot(Seat0, sim.CueParams{Phi: 0.5, Power: 0.4}, nil, 1); err != nil {
		t.Fatalf("出杆失败：%v", err)
	}
	// 这一局根本没装机器人，等多久都不该有第二次物理调用。
	time.Sleep(2 * time.Second)
	if calls := simCalls(fake); calls != 1 {
		t.Fatalf("普通对局里物理服务被调用了 %d 次，期望只有玩家那一杆", calls)
	}
}

// TestBotPlacesCueBall 检查母球摔袋后机器人会先把它摆回台面。
func TestBotPlacesCueBall(t *testing.T) {
	cue := sim.ResultBall{ID: sim.CueBallID, X: -0.02, Y: 0.02, Z: -0.05, OnTable: false}
	ball := sim.ResultBall{ID: "1", X: 0.30, Y: 1.30, OnTable: true}
	state := botStateFor(t, Seat1, cue, ball)
	state.Turn = Seat1
	state.BallInHand = true
	state.CuePlaced = false

	match := NewMatch(state, Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	t.Cleanup(match.Close)

	spotX, spotY, ok := match.botCueSpot()
	if !ok {
		t.Fatal("台面空着却找不到可以摆母球的点")
	}
	if !insideTable(spotX, spotY) {
		t.Fatalf("摆位 %v,%v 在台面外", spotX, spotY)
	}

	// 走一遍真正的落位命令，确认摆完之后母球在台上、z 归零。
	if err := match.PlaceCue(Seat1, spotX, spotY); err != nil {
		t.Fatalf("落位失败：%v", err)
	}
	snapshot, err := match.Snapshot(Seat1)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	for _, item := range snapshot.Balls {
		if item.ID != sim.CueBallID {
			continue
		}
		if !item.OnTable {
			t.Fatal("落位之后母球仍然不在台面上")
		}
		if item.Z != 0 {
			t.Fatalf("落位之后 z = %v，应当是 0", item.Z)
		}
	}
}

// TestSnapshotReportsBotSeat 检查快照带上了机器人座位号，前端靠它区分人机局。
func TestSnapshotReportsBotSeat(t *testing.T) {
	state := newBotState(88, "333444", PlayerBrief{UserID: 7, Nickname: "甲"})
	match := NewMatch(state, Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	t.Cleanup(match.Close)

	snapshot, err := match.Snapshot(Seat0)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	if snapshot.Bot != Seat1 {
		t.Fatalf("快照里的 bot = %d，期望 1", snapshot.Bot)
	}
	if snapshot.Players[Seat1].Nickname != BotPlayer.Nickname {
		t.Fatalf("座位 1 的昵称 = %q，期望 %q", snapshot.Players[Seat1].Nickname, BotPlayer.Nickname)
	}
}

// simCalls 读假物理服务被调用了几次。
func simCalls(fake *fakeSim) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.inputs)
}
