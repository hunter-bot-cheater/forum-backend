package pool

import (
	"testing"

	"forum-backend/internal/pool/sim"
)

// 造事件的小工具。规则只看事件流，不看轨迹，所以测试不需要帧与终局球位。
func hit(t float64, a, b string) sim.Event {
	return sim.Event{T: t, Type: sim.EventBallBall, A: a, B: b}
}

func rail(t float64, ballID string) sim.Event {
	return sim.Event{T: t, Type: sim.EventCushion, A: ballID, B: "lc"}
}

func pot(t float64, ballID string) sim.Event {
	return sim.Event{T: t, Type: sim.EventBallPocket, A: ballID, B: "lb"}
}

// shotOf 把事件串成一杆的结果。
func shotOf(events ...sim.Event) *sim.Shot {
	return &sim.Shot{Events: events}
}

// rackOnTable 返回满台 16 颗球的球号集合。
func rackOnTable() map[string]bool {
	onTable := make(map[string]bool, len(StandardRack))
	for _, item := range StandardRack {
		onTable[item.ID] = true
	}
	return onTable
}

// breakState 是开球前的状态：还没定组，16 颗球都在台面上。
func breakState() RuleState {
	return RuleState{Shooter: Seat0, BreakDone: false, OnTable: rackOnTable()}
}

// playState 造一个已经开过球、定了组的状态。own 是射手自己那一组。
func playState(shooter Seat, own BallGroup, onTable ...string) RuleState {
	state := RuleState{
		Shooter:   shooter,
		BreakDone: true,
		OnTable:   make(map[string]bool, len(onTable)),
	}
	state.OnTable[sim.CueBallID] = true
	for _, id := range onTable {
		state.OnTable[id] = true
	}
	if own != GroupNone {
		state.Groups[shooter] = own
		state.Groups[shooter.Other()] = own.Other()
	}
	return state
}

// 开球：进一颗球就够，继续打。
func TestBreakWithPotKeepsTurn(t *testing.T) {
	verdict := JudgeShot(breakState(), shotOf(
		hit(0.16, sim.CueBallID, "9"),
		hit(0.17, "9", "4"),
		pot(0.9, "3"),
	))

	if verdict.Foul {
		t.Fatalf("进球的开球不该判犯规：%s", verdict.Reason)
	}
	if verdict.NextSeat != Seat0 {
		t.Errorf("开球进球应当继续打，NextSeat = %d", verdict.NextSeat)
	}
	if verdict.AssignedGroup != GroupNone {
		t.Errorf("开球进球不定组，却定成了 %s", verdict.AssignedGroup)
	}
	if verdict.ReSpotEight {
		t.Error("8 号没进袋，不该触发重摆")
	}
}

// 开球：没进球但三颗球碰库，同样有效，换人。
//
// 三颗是 breakRailBalls 的取值，理由见那里的注释：本仿真满力只有 7 m/s，
// 按官方的四颗判，中档力度开球一律犯规。
func TestBreakWithThreeRailsIsLegal(t *testing.T) {
	verdict := JudgeShot(breakState(), shotOf(
		hit(0.16, sim.CueBallID, "9"),
		rail(0.5, "1"), rail(0.6, "2"), rail(0.7, "3"),
	))

	if verdict.Foul {
		t.Fatalf("三颗球碰库的开球不该判犯规：%s", verdict.Reason)
	}
	if verdict.NextSeat != Seat1 {
		t.Errorf("开球没进球应当换人，NextSeat = %d", verdict.NextSeat)
	}
}

// 开球：只有两颗球碰库且没进球，判犯规并给对手手中球。
func TestBreakWithTwoRailsIsFoul(t *testing.T) {
	verdict := JudgeShot(breakState(), shotOf(
		hit(0.16, sim.CueBallID, "9"),
		rail(0.5, "1"), rail(0.6, "2"),
	))

	if !verdict.Foul || verdict.ReasonCode != FoulBreakNoSpread {
		t.Fatalf("应当判开球未打开，实际 %+v", verdict)
	}
	if !verdict.BallInHand || verdict.NextSeat != Seat1 {
		t.Errorf("犯规后应当换人且给手中球，实际 NextSeat=%d BallInHand=%v", verdict.NextSeat, verdict.BallInHand)
	}
}

// 开球：母球什么都没碰到。
func TestBreakWithoutContactIsFoul(t *testing.T) {
	verdict := JudgeShot(breakState(), shotOf())

	if !verdict.Foul || verdict.ReasonCode != FoulNoContact {
		t.Fatalf("应当判空杆，实际 %+v", verdict)
	}
	if !verdict.BallInHand {
		t.Error("空杆应当给对手手中球")
	}
}

// 开球把 8 号打进：重摆，不判负。
func TestBreakPocketingEightRespots(t *testing.T) {
	verdict := JudgeShot(breakState(), shotOf(
		hit(0.16, sim.CueBallID, "9"),
		pot(1.1, sim.EightBallID),
	))

	if verdict.Winner != nil {
		t.Fatalf("开球进 8 号不该判负，赢家 = %v", *verdict.Winner)
	}
	if !verdict.ReSpotEight {
		t.Error("开球进 8 号必须重摆")
	}
	if verdict.Foul {
		t.Errorf("重摆不该同时算犯规：%s", verdict.Reason)
	}
}

// 开球母球落袋：犯规，给对手手中球。
func TestBreakScratchingCueIsFoul(t *testing.T) {
	verdict := JudgeShot(breakState(), shotOf(
		hit(0.16, sim.CueBallID, "9"),
		pot(1.2, sim.CueBallID),
	))

	if !verdict.Foul || verdict.ReasonCode != FoulCuePocketed {
		t.Fatalf("母球落袋应当犯规，实际 %+v", verdict)
	}
	if verdict.NextSeat != Seat1 || !verdict.BallInHand {
		t.Errorf("应当换人并给手中球，实际 %+v", verdict)
	}
}

// 定组：开球之后第一次合法进球按该球定组，并且继续打。
func TestFirstLegalPotAssignsGroup(t *testing.T) {
	state := playState(Seat0, GroupNone, "1", "2", "3", "9", "10", "11", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, "3"),
		pot(0.9, "3"),
	))

	if verdict.AssignedGroup != GroupSolids {
		t.Fatalf("应当定全色组，实际 %s", verdict.AssignedGroup)
	}
	if verdict.NextSeat != Seat0 {
		t.Errorf("打进自己组的球应当继续打，NextSeat = %d", verdict.NextSeat)
	}
}

// 定组：同一杆进了两组球，按最先落袋的那颗定。
func TestGroupAssignedByFirstPottedBall(t *testing.T) {
	state := playState(Seat0, GroupNone, "1", "3", "11", "13", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, "11"),
		hit(0.3, "11", "13"),
		pot(0.8, "11"),
		pot(0.9, "3"),
	))

	if verdict.AssignedGroup != GroupStripes {
		t.Fatalf("应当按先落袋的 11 号定花色组，实际 %s", verdict.AssignedGroup)
	}
	if verdict.NextSeat != Seat0 {
		t.Errorf("进了自己组应当继续打，NextSeat = %d", verdict.NextSeat)
	}
}

// 犯规的那一杆即使进了球也不定组。
func TestFoulShotDoesNotAssignGroup(t *testing.T) {
	state := playState(Seat0, GroupNone, "1", "3", "9", "11", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, "3"),
		pot(0.8, "3"),
		pot(1.0, sim.CueBallID),
	))

	if !verdict.Foul {
		t.Fatal("母球落袋却没判犯规")
	}
	if verdict.AssignedGroup != GroupNone {
		t.Errorf("犯规杆不该定组，实际定成了 %s", verdict.AssignedGroup)
	}
}

// 正常击球：进了自己组的球继续打。
func TestLegalPotKeepsTurn(t *testing.T) {
	state := playState(Seat0, GroupSolids, "1", "3", "9", "11", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, "1"),
		pot(0.9, "1"),
	))

	if verdict.Foul {
		t.Fatalf("合法进球不该判犯规：%s", verdict.Reason)
	}
	if verdict.NextSeat != Seat0 {
		t.Errorf("NextSeat = %d，期望继续打", verdict.NextSeat)
	}
}

// 正常击球：碰对了球但什么都没发生，换人但不犯规。
func TestLegalMissSwitchesTurn(t *testing.T) {
	state := playState(Seat0, GroupSolids, "1", "9", "11", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, "1"),
		rail(0.7, "1"),
	))

	if verdict.Foul {
		t.Errorf("碰库了就不该判犯规：%s", verdict.Reason)
	}
	if verdict.NextSeat != Seat1 {
		t.Errorf("NextSeat = %d，期望换人", verdict.NextSeat)
	}
	if verdict.BallInHand {
		t.Error("没犯规不该给手中球")
	}
}

// 首碰对方球：犯规。
func TestHittingOpponentBallIsFoul(t *testing.T) {
	state := playState(Seat0, GroupSolids, "1", "9", "11", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, "9"),
		rail(0.7, "9"),
	))

	if !verdict.Foul || verdict.ReasonCode != FoulWrongBallFirst {
		t.Fatalf("应当判碰错球，实际 %+v", verdict)
	}
	if !verdict.BallInHand || verdict.NextSeat != Seat1 {
		t.Errorf("应当换人并给手中球，实际 %+v", verdict)
	}
}

// 本方球没清完就碰 8 号：犯规。
func TestHittingEightEarlyIsFoul(t *testing.T) {
	state := playState(Seat0, GroupSolids, "1", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, sim.EightBallID),
		rail(0.7, sim.EightBallID),
	))

	if !verdict.Foul || verdict.ReasonCode != FoulWrongBallFirst {
		t.Fatalf("应当判提前碰 8 号，实际 %+v", verdict)
	}
	if verdict.Winner != nil {
		t.Error("只是碰到 8 号、没进袋，不该直接判负")
	}
}

// 碰了球但既没进球也没碰库：犯规。
func TestContactWithoutRailOrPotIsFoul(t *testing.T) {
	state := playState(Seat0, GroupSolids, "1", "9", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, "1"),
	))

	if !verdict.Foul || verdict.ReasonCode != FoulNoRailAfter {
		t.Fatalf("应当判碰球后没走到位，实际 %+v", verdict)
	}
}

// 还没定组时，先碰哪一组都合法。
func TestAnyGroupContactIsLegalBeforeAssignment(t *testing.T) {
	state := playState(Seat0, GroupNone, "1", "3", "9", "11", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, "9"),
		pot(0.9, "9"),
	))

	if verdict.Foul {
		t.Fatalf("未定组时碰花色球不该犯规：%s", verdict.Reason)
	}
	if verdict.AssignedGroup != GroupStripes {
		t.Errorf("应当定花色组，实际 %s", verdict.AssignedGroup)
	}
}

// 本方球没清完就把 8 号打进：判负。
func TestPocketingEightEarlyLoses(t *testing.T) {
	state := playState(Seat0, GroupSolids, "1", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, "1"),
		pot(0.9, sim.EightBallID),
	))

	if verdict.Winner == nil || *verdict.Winner != Seat1 {
		t.Fatalf("应当判 0 号输，实际 %+v", verdict)
	}
	if verdict.WinReason != WinEightEarly {
		t.Errorf("WinReason = %s，期望 %s", verdict.WinReason, WinEightEarly)
	}
}

// 本方球清完后打进 8 号：判胜。
func TestPocketingEightAfterClearingWins(t *testing.T) {
	state := playState(Seat0, GroupSolids, sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, sim.EightBallID),
		pot(0.9, sim.EightBallID),
	))

	if verdict.Winner == nil || *verdict.Winner != Seat0 {
		t.Fatalf("应当判 0 号赢，实际 %+v", verdict)
	}
	if verdict.WinReason != WinEightLegal {
		t.Errorf("WinReason = %s，期望 %s", verdict.WinReason, WinEightLegal)
	}
}

// 犯规同一杆里把 8 号打进：判负，哪怕本方球已经清完。
func TestPocketingEightWithFoulLoses(t *testing.T) {
	state := playState(Seat0, GroupSolids, sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, sim.EightBallID),
		pot(0.5, sim.EightBallID),
		pot(0.6, sim.CueBallID),
	))

	if verdict.Winner == nil || *verdict.Winner != Seat1 {
		t.Fatalf("犯规进 8 号应当判负，实际 %+v", verdict)
	}
	if verdict.WinReason != WinEightWithFoul {
		t.Errorf("WinReason = %s，期望 %s", verdict.WinReason, WinEightWithFoul)
	}
}

// 清台判定只算自己那一组：对方的球还在台面上不影响「清完」。
func TestClearedGroupIgnoresOpponentBalls(t *testing.T) {
	state := playState(Seat0, GroupSolids, "9", "11", "13", sim.EightBallID)
	if !clearedGroup(state, Seat0) {
		t.Error("全色球都不在台面上，应当算清完")
	}
	if clearedGroup(state, Seat1) {
		t.Error("花色球还在台面上，不该算清完")
	}
}

// 未定组时任何座位都谈不上清完，否则「未清台碰 8 号」会被误判成合法。
func TestClearedGroupIsFalseBeforeAssignment(t *testing.T) {
	state := playState(Seat0, GroupNone, sim.EightBallID)
	if clearedGroup(state, Seat0) {
		t.Error("未定组不该算清完")
	}
}

// 换边击球时射手是一号位，规则要把「本方组」换成 1 号位的组。
func TestRulesFollowTheSeat(t *testing.T) {
	// 座位 1 打花色，全色是第一颗球 —— 碰全色就是碰错球。
	state := playState(Seat1, GroupStripes, "1", "9", "11", sim.EightBallID)
	verdict := JudgeShot(state, shotOf(
		hit(0.2, sim.CueBallID, "1"),
		pot(0.9, "1"),
	))

	if !verdict.Foul || verdict.ReasonCode != FoulWrongBallFirst {
		t.Fatalf("座位 1 打花色时碰全色应当犯规，实际 %+v", verdict)
	}
	if verdict.NextSeat != Seat0 {
		t.Errorf("犯规后应当换到座位 0，实际 %d", verdict.NextSeat)
	}
}

// 犯规结论必须带一句能直接给玩家看的中文，不能是空串。
func TestFoulAlwaysCarriesAMessage(t *testing.T) {
	cases := []struct {
		name  string
		state RuleState
		shot  *sim.Shot
	}{
		{"空杆", breakState(), shotOf()},
		{"开球没打开", breakState(), shotOf(hit(0.1, sim.CueBallID, "9"), rail(0.5, "1"))},
		{"母球落袋", playState(Seat0, GroupSolids, "1", sim.EightBallID), shotOf(hit(0.1, sim.CueBallID, "1"), pot(0.5, sim.CueBallID))},
		{"碰错球", playState(Seat0, GroupSolids, "1", "9", sim.EightBallID), shotOf(hit(0.1, sim.CueBallID, "9"), rail(0.5, "9"))},
		{"没走到位", playState(Seat0, GroupSolids, "1", "9", sim.EightBallID), shotOf(hit(0.1, sim.CueBallID, "1"))},
	}

	for _, item := range cases {
		verdict := JudgeShot(item.state, item.shot)
		if !verdict.Foul {
			t.Errorf("%s：应当判犯规，实际没判", item.name)
			continue
		}
		if verdict.Reason == "" {
			t.Errorf("%s：犯规没有说明文案", item.name)
		}
		if verdict.ReasonCode == "" {
			t.Errorf("%s：犯规没有原因码", item.name)
		}
	}
}
