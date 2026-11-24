package pool

import (
	"math"
	"sort"

	"forum-backend/internal/pool/sim"
)

// 机器人（人机对战）的球路计算。
//
// 这里只算「往哪打、多大力度」，不算轨迹：算出参数之后照样交给 pool-sim，
// 与玩家出杆走同一条路。这样机器人看到的球、玩家看到的球、规则判定的球
// 是同一份物理结果，不会出现「机器人打进了一颗玩家这边没进的球」。
//
// 水平定位是「练习搭子」而不是高手机器人：它会把能进的球打进、会避开母球落袋，
// 但不做走位，也不做防守。真要防住它，一颗贴库的球就够了。

// botPocket 是一个袋口中心，与前端 table.ts 的 POCKETS 逐项对齐。
//
// 三处（前端绘制、前端判定、这里）必须是同一组数：袋心挪一厘米，
// 机器人算的幽灵球就跟着偏，而它瞄准的那条线在玩家屏幕上会显得「打歪了」。
type botPocket struct {
	x float64
	y float64
}

// botPockets 顺序沿用前端的 lb / lc / lt / rb / rc / rt。
var botPockets = []botPocket{
	{x: -0.01796, y: -0.01796},
	{x: -0.032, y: 0.9906},
	{x: -0.01796, y: 1.99916},
	{x: 1.00856, y: -0.01796},
	{x: 1.0226, y: 0.9906},
	{x: 1.00856, y: 1.99916},
}

// 选杆时的几个阈值。
const (
	// botMinCut 是最小的「切球余弦」。0.34 约等于 70°：再薄下去目标球只拿到
	// 一小部分动量，球会滚不到位，看起来像机器人在瞎打。
	botMinCut = 0.34
	// botMaxPlans 是每次最多准备几套方案。每套都要真调一次物理服务
	// （一次几百毫秒到一秒半），准备多了玩家就只是在等。
	botMaxPlans = 3
	// botMinPower / botMaxPower 是力度区间。下限要能推动球并走完一两米，
	// 上限不拉满：满力那一杆在 pool-sim 里要算一秒多，而且容易把球打飞出台。
	botMinPower = 0.26
	botMaxPower = 0.62
	// botBreakPower 是开球力度。规则要求「至少三颗球碰库或进球」才算有效开球，
	// 实测在 pool-sim 里 0.9 是第一条能稳定凑够三颗的线。
	botBreakPower = 0.95
	// botBreakAimOffset 开球时故意偏一点，让球堆散得更开。
	// 正对顶点球打，球堆会沿直线推开，母球常常贴死在球堆里；
	// 偏 0.006 弧度（约 0.34°）看着还是直打，散球效果明显好一些。
	botBreakAimOffset = 0.006
)

// botShot 是候选的一杆。
type botShot struct {
	params sim.CueParams
	// score 只在排序时用：越大越容易进。
	score float64
}

// botPlans 给当前局面排出几套候选杆法，从「最容易进」到「勉强能打」。
//
// 返回空切片表示这颗母球没法打（不在台面上，或算不出任何一条线），
// 调用方据此决定是落位还是干脆等超时。
func (m *Match) botPlans() []botShot {
	cue, ok := m.state.ballByID(sim.CueBallID)
	if !ok || !cue.OnTable {
		return nil
	}
	// 开球单独一条：这时候还没有目标球可挑，把球堆打散就是唯一目标。
	if !m.state.BreakDone {
		return m.botBreakPlans(cue)
	}

	plans := make([]botShot, 0, 8)
	for _, id := range m.botTargets() {
		ball, ok := m.state.ballByID(id)
		if !ok || !ball.OnTable {
			continue
		}
		for _, pocket := range botPockets {
			if plan, ok := m.botAimAt(cue, ball, pocket); ok {
				plans = append(plans, plan)
			}
		}
	}

	// 一条线都算不出来时退化成「朝最近的己方球轻推」：
	// 空杆（母球什么都没碰到）是犯规，而碰到一颗自己的球起码不送自由球。
	if len(plans) == 0 {
		return m.botSafetyPlans(cue)
	}

	sort.Slice(plans, func(i, j int) bool { return plans[i].score > plans[j].score })
	if len(plans) > botMaxPlans {
		plans = plans[:botMaxPlans]
	}
	return plans
}

// botTargets 返回机器人这一杆该打的球号。
//
// 三种情况：还没定组（谁都可能打）、已经定组（打自己那组）、
// 自己那组清完了（打 8 号）。未定组时 8 号不算目标——提前打进 8 号直接判负。
func (m *Match) botTargets() []string {
	mine := m.state.Groups[m.state.Bot]
	if mine == GroupNone {
		// 对手已经定组而我还空着，说明自己上一杆没进球，那就打另一组。
		if other := m.state.Groups[m.state.Bot.Other()]; other != GroupNone {
			mine = other.Other()
		}
	}
	if mine == GroupNone {
		ids := make([]string, 0, 15)
		for _, ball := range m.state.Balls {
			if !ball.OnTable || ball.ID == sim.CueBallID || ball.ID == sim.EightBallID {
				continue
			}
			ids = append(ids, ball.ID)
		}
		return ids
	}

	ids := make([]string, 0, 7)
	for _, id := range groupBalls(mine) {
		if ball, ok := m.state.ballByID(id); ok && ball.OnTable {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return []string{sim.EightBallID}
	}
	return ids
}

// botBreakPlans 是开球那一杆：朝球堆顶点的方向打满力。
//
// 顶点球就是离母球最近的那颗。不写死坐标：球堆被人摆过（重开、重摆）之后
// 坐标会变，按「离母球最近」挑永远是对的。
func (m *Match) botBreakPlans(cue sim.ResultBall) []botShot {
	best := sim.ResultBall{}
	bestDist := math.Inf(1)
	for _, ball := range m.state.Balls {
		if !ball.OnTable || ball.ID == sim.CueBallID {
			continue
		}
		dist := math.Hypot(ball.X-cue.X, ball.Y-cue.Y)
		if dist < bestDist {
			best, bestDist = ball, dist
		}
	}
	if bestDist == math.Inf(1) {
		return nil
	}
	phi := math.Atan2(best.Y-cue.Y, best.X-cue.X) + botBreakAimOffset
	return []botShot{{params: sim.CueParams{Phi: phi, Power: botBreakPower}, score: 1}}
}

// botAimAt 算「母球先撞到 ghost 点、把 ball 送进 pocket」这一杆。
//
// 第二个返回值为 false 表示这条线不可行：袋口在球后面、切得太薄、
// 或者路上有球挡着。
func (m *Match) botAimAt(cue, ball sim.ResultBall, pocket botPocket) (botShot, bool) {
	// 目标球到袋口的方向。长度趋近 0 说明球就压在袋心，没有方向可言。
	px := pocket.x - ball.X
	py := pocket.y - ball.Y
	pocketDist := math.Hypot(px, py)
	if pocketDist < botBallGap() {
		return botShot{}, false
	}
	ux, uy := px/pocketDist, py/pocketDist

	// ghost 点：两颗球相切时母球球心应在的位置。
	// 从目标球球心沿「袋口反方向」退一个球直径——这是最基本的台球几何，
	// 也是前端瞄准线画的那个虚影球。
	gx := ball.X - ux*2*ballRadius
	gy := ball.Y - uy*2*ballRadius

	cx := gx - cue.X
	cy := gy - cue.Y
	cueDist := math.Hypot(cx, cy)
	// 母球已经在 ghost 点上（贴球）：这时任何角度都是「推」，
	// 按 ghost 方向算出来的 cos 会失真，直接跳过。
	if cueDist < botBallGap() {
		return botShot{}, false
	}

	// 切角余弦：母球来向与目标球去向的夹角。1 是正着打（最好），0 是擦边。
	cosCut := (cx*ux + cy*uy) / cueDist
	if cosCut < botMinCut {
		return botShot{}, false
	}

	// 两条路上都不能有别的球：母球到 ghost、目标球到袋口。
	// 目标球本身要排除——母球路线的终点离它就只有两个半径。
	if m.botBlocked(cue.X, cue.Y, gx, gy, ball.ID) {
		return botShot{}, false
	}
	if m.botBlocked(ball.X, ball.Y, pocket.x, pocket.y, ball.ID) {
		return botShot{}, false
	}

	// 打分：切得越正越好、两段路越短越好。袋口那一段乘 0.5：
	// 目标球被撞之后只带走一部分动量，走得太远容易停袋口。
	score := cosCut / (cueDist + pocketDist*0.5 + 0.05)

	return botShot{
		params: sim.CueParams{Phi: math.Atan2(cy, cx), Power: botPowerFor(cueDist + pocketDist)},
		score:  score,
	}, true
}

// botSafetyPlans 是「一条能进的线都没有」时的保底：朝最近的己方球轻推。
//
// 力度压到 0.3 左右：这一杆的目的只是不空杆，打太狠反而容易把球撞到
// 对手的好位置上。
func (m *Match) botSafetyPlans(cue sim.ResultBall) []botShot {
	targets := m.botTargets()
	if len(targets) == 0 {
		return nil
	}
	best := sim.ResultBall{}
	bestDist := math.Inf(1)
	for _, id := range targets {
		ball, ok := m.state.ballByID(id)
		if !ok || !ball.OnTable {
			continue
		}
		dist := math.Hypot(ball.X-cue.X, ball.Y-cue.Y)
		if dist < bestDist {
			best, bestDist = ball, dist
		}
	}
	if bestDist == math.Inf(1) {
		return nil
	}
	return []botShot{{
		params: sim.CueParams{
			Phi:   math.Atan2(best.Y-cue.Y, best.X-cue.X),
			Power: botMinPower,
		},
		score: 0,
	}}
}

// botPowerFor 按「母球要走的路 + 目标球要走的距离」估力度。
//
// 系数取自前端 predict.ts 的实测表：台呢粘性阻尼下滚动距离几乎正比于初速，
// 0.3 力度能滚 3.2 米。这里按 1 米对应 0.11 的力度往上加，
// 再加上碰撞传递的那部分损耗，落在 0.26~0.62 之间。
func botPowerFor(travel float64) float64 {
	power := botMinPower + travel*0.11
	if power > botMaxPower {
		return botMaxPower
	}
	return power
}

// botBlocked 判断一条线段会不会撞上别的球。
//
// 判据是「球心到线段的距离小于两个球半径」：台球是有体积的，
// 贴着线过也不行，母球会先碰到那颗球。
func (m *Match) botBlocked(fromX, fromY, toX, toY float64, ignore string) bool {
	limit := 2*ballRadius - ballRadiusHair
	for _, ball := range m.state.Balls {
		if !ball.OnTable || ball.ID == sim.CueBallID || ball.ID == ignore {
			continue
		}
		if segmentDistance(ball.X, ball.Y, fromX, fromY, toX, toY) < limit {
			return true
		}
	}
	return false
}

// segmentDistance 返回点到线段的最短距离。
//
// 先投影到线段上并把参数夹到 [0,1]，再算距离：
// 只算「到直线」的距离会把线段延长线后面的球也当成挡路。
func segmentDistance(px, py, ax, ay, bx, by float64) float64 {
	dx := bx - ax
	dy := by - ay
	lengthSq := dx*dx + dy*dy
	if lengthSq == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / lengthSq
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// botBallGap 是「两个球心至少要隔开多少」才不叫贴在一起。
func botBallGap() float64 {
	return 2*ballRadius - ballRadiusHair
}

// botCueSpot 给母球挑一个可以摆的位置（手中球时用）。
//
// 先沿中线从开球端往远端找：这条线上摆出来的球位看着最自然，
// 也不会一上来就贴着库。都不行再全台面扫描——那种情况只会在球挤成一团时出现。
func (m *Match) botCueSpot() (float64, float64, bool) {
	for _, y := range []float64{0.16, 0.24, 0.32, 0.40, 0.56, 0.72, 0.88, 1.04, 1.20, 1.36, 1.52, 1.68, 1.84} {
		if m.botSpotOK(tableWidth/2, y) {
			return tableWidth / 2, y, true
		}
	}
	const step = 0.06
	for x := ballRadius + 0.02; x < tableWidth-ballRadius; x += step {
		for y := ballRadius + 0.02; y < tableLength-ballRadius; y += step {
			if m.botSpotOK(x, y) {
				return x, y, true
			}
		}
	}
	return 0, 0, false
}

// botSpotOK 判断母球摆在 (x, y) 合不合法。
//
// 与 handlePlaceCue 的两道检查一致：在台内、不压别的球；
// 开球那一杆还要在开球线之后。
func (m *Match) botSpotOK(x, y float64) bool {
	if !insideTable(x, y) {
		return false
	}
	if m.state.PlaceZone == PlaceBehindHead && y > cueHeadString+0.001 {
		return false
	}
	return !m.state.overlapsBall(x, y)
}
