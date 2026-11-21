package pool

import "forum-backend/internal/pool/sim"

// 规则按中式八球的简化版实现，与 pooltool 默认规则集有三处刻意的差异，
// 写在这里避免以后有人对照源码时困惑：
//
//  1. 开球把 8 号球打进按「重摆」处理，pooltool 按「重开整局」处理；
//  2. 第一版不做指袋，8 号球进哪个袋都算合法；
//  3. 不做「三库连续犯规判负」。
//
// 规则是纯函数：输入「上一份状态 + 这一杆的物理结果」，输出「结论」。
// 结论由 actor 应用，这里不碰任何状态，所以规则测试不需要起对局、不需要网络与数据库。

// breakRailBalls 是开球合法的门槛之一：至少这么多颗球碰过库。
// 另一条门槛是「至少进一颗球」，两条满足一条就算开球有效。
const breakRailBalls = 4

// TimeoutLossThreshold 是连续超时判负的阈值。
// 一次超时只警告并换人，第二次直接判负——否则挂机的人可以一直拖着不让对局结束。
const TimeoutLossThreshold = 2

// 犯规原因码。文案给玩家看，原因码给日志与前端分支用：
// 文案随时可能改，靠中文串判断类型是不可维护的。
const (
	FoulNoContact      = "no_contact"
	FoulBreakNoSpread  = "break_no_spread"
	FoulWrongBallFirst = "wrong_ball_first"
	FoulNoRailAfter    = "no_rail_after_contact"
	FoulCuePocketed    = "cue_pocketed"
)

// 获胜原因码。
const (
	WinEightLegal    = "eight_legal"     // 本方球清完后合法打进 8 号
	WinEightEarly    = "eight_early"     // 本方球没清完就打进 8 号
	WinEightWithFoul = "eight_with_foul" // 犯规同一杆里把 8 号打进
	WinTimeout       = "timeout"         // 对手连续超时
	WinLeave         = "leave"           // 对手主动退出
)

// RuleState 是判定一杆需要知道的对局状态，故意做得很窄：
// 谁在打、开球打过没、谁打哪组、击球前台面上还剩哪些球。
//
// 不把整个 MatchState 传进来，是为了让规则测试写起来只要几行结构体字面量。
type RuleState struct {
	// Shooter 是本杆击球方。
	Shooter Seat
	// BreakDone 表示开球那一杆已经打完。为 false 时本杆按开球规则判。
	BreakDone bool
	// Groups 是两个座位各自的球组，GroupNone 表示还没定组。
	Groups [2]BallGroup
	// OnTable 是击球前台面上的球号（击球前的状态，不是打完之后）。
	OnTable map[string]bool
}

// Verdict 是一杆打完的判定结论。
type Verdict struct {
	// Foul 表示本杆犯规，ReasonCode / Reason 说明原因。
	Foul       bool
	ReasonCode string
	Reason     string

	// Potted 是本杆落袋的球号，按落袋时间排序，含母球。
	Potted []string

	// NextSeat 是下一杆谁打。合法进球继续，否则换人。
	NextSeat Seat

	// BallInHand 表示下一杆开始前对手可以任意摆放母球。
	BallInHand bool

	// ReSpotEight 表示要把 8 号球放回台面（只在开球把 8 号打进时出现）。
	ReSpotEight bool

	// AssignedGroup 非空表示本杆定了组，击球方得到这一组。
	AssignedGroup BallGroup

	// Winner 非 nil 表示本杆结束了整局。
	Winner    *Seat
	WinReason string
}

// shotFacts 是从物理结果里抽出来的几个事实，只抽一次，后面反复用。
type shotFacts struct {
	// potted 按落袋时间顺序排列，含母球。
	potted []string
	// pottedSet 用于快速判断某一颗球有没有进。
	pottedSet map[string]bool
	// firstContact 是母球第一个碰到的球号。空串表示母球什么都没碰到（空杆）。
	firstContact string
	// cushionBalls 是碰过库的球号集合，开球的「四颗球碰库」按它数。
	cushionBalls map[string]bool
	// cushionAfterContact 表示首碰之后还有球碰过库。
	cushionAfterContact bool
}

// factsOf 把事件流压成上面那几个事实。
//
// 事件是按时序排好的（pool-sim 按 pooltool 的事件顺序输出），所以「首碰」与
// 「碰库发生在首碰之后」都可以边扫边定，不需要二次遍历。
func factsOf(shot *sim.Shot) shotFacts {
	facts := shotFacts{
		pottedSet:    make(map[string]bool),
		cushionBalls: make(map[string]bool),
	}

	for _, event := range shot.Events {
		switch event.Type {
		case sim.EventBallBall:
			// 母球可能出现在 a 或 b 的位置，两个方向都要认。
			if facts.firstContact == "" {
				if event.A == sim.CueBallID && event.B != "" {
					facts.firstContact = event.B
				} else if event.B == sim.CueBallID && event.A != "" {
					facts.firstContact = event.A
				}
			}
		case sim.EventCushion:
			facts.cushionBalls[event.A] = true
			if facts.firstContact != "" {
				facts.cushionAfterContact = true
			}
		case sim.EventBallPocket:
			if facts.pottedSet[event.A] {
				continue
			}
			facts.pottedSet[event.A] = true
			facts.potted = append(facts.potted, event.A)
		}
	}
	return facts
}

// pottedBalls 返回落袋的非母球数量。
func (f shotFacts) pottedBalls() int {
	count := len(f.potted)
	if f.pottedSet[sim.CueBallID] {
		count--
	}
	return count
}

// JudgeShot 判定一杆。纯函数，同样的输入永远得到同样的结论。
func JudgeShot(state RuleState, shot *sim.Shot) Verdict {
	facts := factsOf(shot)

	// 空杆是唯一一个跨越阶段的犯规：开球与常规击球都要求母球先碰到球，
	// 所以它必须在分阶段之前判，否则开球空杆会被归到「球堆没打开」上去。
	if facts.firstContact == "" {
		return Verdict{
			Foul:       true,
			ReasonCode: FoulNoContact,
			Reason:     "母球没有碰到任何球",
			Potted:     facts.potted,
			NextSeat:   state.Shooter.Other(),
			BallInHand: true,
		}
	}

	// 开球单独一套规则：门槛不同，而且开场就定组会让后半局无法进行。
	if !state.BreakDone {
		return judgeBreak(state, facts)
	}

	verdict := Verdict{Potted: facts.potted, NextSeat: state.Shooter}

	// 先算出本杆有没有犯规、是什么犯规。三个判断互斥，按严重程度排先后：
	// 母球落袋最直观，其次是碰错球，最后才是「碰了但没走到位」。
	switch {
	case facts.pottedSet[sim.CueBallID]:
		verdict.Foul, verdict.ReasonCode, verdict.Reason = true, FoulCuePocketed, "母球落袋"
	case !contactLegal(state, facts.firstContact):
		verdict.Foul, verdict.ReasonCode, verdict.Reason = true, FoulWrongBallFirst, wrongBallReason(state, facts.firstContact)
	case facts.pottedBalls() == 0 && !facts.cushionAfterContact:
		verdict.Foul, verdict.ReasonCode, verdict.Reason = true, FoulNoRailAfter, "碰球之后既没有球进袋也没有球碰库"
	}
	if verdict.Foul {
		verdict.BallInHand = true
		verdict.NextSeat = state.Shooter.Other()
	}

	// 8 号球单独处理：它的结果只有「赢」和「输」两种，没有中间态。
	if facts.pottedSet[sim.EightBallID] {
		judgeEightBall(state, &verdict)
		return verdict
	}

	// 定组：开球之后的第一杆合法进球决定双方球组。
	// 同一杆进了两组球时，按落袋时间最早的那颗定——这是中式八球的通行做法，
	// 也是唯一不需要玩家额外确认的判法。
	ownGroup := state.Groups[state.Shooter]
	if ownGroup == GroupNone && !verdict.Foul {
		for _, id := range facts.potted {
			group := ballGroup(id)
			if group == GroupNone {
				continue
			}
			verdict.AssignedGroup = group
			break
		}
		ownGroup = verdict.AssignedGroup
	}

	// 换人：进了自己组的球才能接着打，其余一律换人。
	if !verdict.Foul && !pottedOwnGroup(facts, ownGroup) {
		verdict.NextSeat = state.Shooter.Other()
	}
	return verdict
}

// judgeBreak 判开球那一杆。
//
// 开球不判负也不定组：8 号进袋重摆，母球进袋算犯规，球堆没打开算犯规。
// 「开球进球不定组」是中式八球的规则，目的是防止开球运气球直接把整局锁死。
func judgeBreak(state RuleState, facts shotFacts) Verdict {
	verdict := Verdict{
		Potted:   facts.potted,
		NextSeat: state.Shooter,
		// 开球把 8 号打进按重摆处理，不判负（设计稿 12.7 的三处差异之一）。
		ReSpotEight: facts.pottedSet[sim.EightBallID],
	}

	// 母球落袋先报：它会让「进球数」归零，两条原因一起报只会让人以为进了两颗。
	if facts.pottedSet[sim.CueBallID] {
		verdict.Foul, verdict.ReasonCode, verdict.Reason = true, FoulCuePocketed, "母球落袋"
		verdict.BallInHand = true
		verdict.NextSeat = state.Shooter.Other()
		return verdict
	}

	// 开球有效的两条门槛，满足一条即可：进球，或至少四颗球碰库。
	if facts.pottedBalls() == 0 && len(facts.cushionBalls) < breakRailBalls {
		verdict.Foul = true
		verdict.ReasonCode = FoulBreakNoSpread
		verdict.Reason = "开球没有把球打开（至少要有球进袋或四颗球碰库）"
		verdict.BallInHand = true
		verdict.NextSeat = state.Shooter.Other()
		return verdict
	}

	// 开球进了球继续打，没进换人。开球方没有「进球必须是自己组」的约束，因为还没定组。
	if facts.pottedBalls() == 0 {
		verdict.NextSeat = state.Shooter.Other()
	}
	return verdict
}

// judgeEightBall 填上 8 号球进袋之后的胜负，原地修改 verdict。
//
// 判负的两种情形优先于判胜：只要这一杆本身犯规，或者本方球还没清完，
// 进了 8 号都是输，不管它进得多漂亮。
func judgeEightBall(state RuleState, verdict *Verdict) {
	if verdict.Foul {
		winner := state.Shooter.Other()
		verdict.Winner, verdict.WinReason = &winner, WinEightWithFoul
		return
	}
	if !clearedGroup(state, state.Shooter) {
		winner := state.Shooter.Other()
		verdict.Winner, verdict.WinReason = &winner, WinEightEarly
		return
	}
	winner := state.Shooter
	verdict.Winner, verdict.WinReason = &winner, WinEightLegal
}

// contactLegal 判断母球第一个碰到的球是不是允许的。
func contactLegal(state RuleState, ballID string) bool {
	// 8 号球只有在本方球全部清完之后才能打，否则碰一下就犯规。
	if ballID == sim.EightBallID {
		return clearedGroup(state, state.Shooter)
	}

	ownGroup := state.Groups[state.Shooter]
	if ownGroup == GroupNone {
		// 还没定组的阶段（开球之后的过渡杆），先碰哪一组都合法。
		return true
	}
	return ballGroup(ballID) == ownGroup
}

// wrongBallReason 给出「碰错球」的具体说法。两种情况的处置一样，但提示要分清。
func wrongBallReason(state RuleState, ballID string) string {
	if ballID == sim.EightBallID {
		return "本方球还没清完就打 8 号球"
	}
	return "母球先碰到的不是自己的球"
}

// clearedGroup 判断某个座位的球是不是已经全部离台（不含 8 号）。
func clearedGroup(state RuleState, seat Seat) bool {
	group := state.Groups[seat]
	if group == GroupNone {
		// 还没定组，谈不上清完。
		return false
	}
	for id := range state.OnTable {
		if id == sim.CueBallID || id == sim.EightBallID {
			continue
		}
		if ballGroup(id) == group {
			return false
		}
	}
	return true
}

// pottedOwnGroup 判断本杆有没有打进自己组的球。
// 未定组时一律算 false：定组那一杆由 AssignedGroup 决定归属，这里不重复判断。
func pottedOwnGroup(facts shotFacts, ownGroup BallGroup) bool {
	if ownGroup == GroupNone {
		return false
	}
	for _, id := range facts.potted {
		if ballGroup(id) == ownGroup {
			return true
		}
	}
	return false
}
