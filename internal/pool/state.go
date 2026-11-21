package pool

import (
	"strconv"

	"forum-backend/internal/pool/sim"
)

// Seat 是座位号。0 是邀请发起人（也是开球方），1 是被邀请人。
//
// 用座位而不是用户 id 来表达「轮到谁」：规则里有大量「另一方」的推导，
// 座位只有两个值，取反就是对手；用 user id 的话每次都要去查这张桌子上的另一个人。
type Seat int

const (
	Seat0 Seat = 0
	Seat1 Seat = 1
)

// Other 返回对面的座位。
func (s Seat) Other() Seat {
	if s == Seat0 {
		return Seat1
	}
	return Seat0
}

// Valid 判断座位号是不是 0 或 1。外部传进来的 int 要先过这一关。
func (s Seat) Valid() bool {
	return s == Seat0 || s == Seat1
}

// BallGroup 是球组。空串表示还没定组。
type BallGroup string

const (
	GroupNone    BallGroup = ""        // 未定组，或这颗球不属于任何组（母球、8 号）
	GroupSolids  BallGroup = "solids"  // 1-7 全色
	GroupStripes BallGroup = "stripes" // 9-15 花色
)

// Other 返回另一组。未定组时返回未定组，调用方要先判空。
func (g BallGroup) Other() BallGroup {
	if g == GroupSolids {
		return GroupStripes
	}
	if g == GroupStripes {
		return GroupSolids
	}
	return GroupNone
}

// ballNumber 把球号字符串转成数字：母球返回 0，非法球号返回 -1。
//
// 球号在整条链路里都是字符串（pooltool 的 id、JSON 的键、前端贴图的索引），
// 只有需要判断「属于哪一组」时才转数字，且只在这一处转。
func ballNumber(id string) int {
	if id == sim.CueBallID {
		return 0
	}
	number, err := strconv.Atoi(id)
	if err != nil || number < 1 || number > 15 {
		return -1
	}
	return number
}

// ballGroup 返回球号所属的组。1-7 全色，9-15 花色，8 号与母球不属于任何组。
func ballGroup(id string) BallGroup {
	number := ballNumber(id)
	if number >= 1 && number <= 7 {
		return GroupSolids
	}
	if number >= 9 && number <= 15 {
		return GroupStripes
	}
	return GroupNone
}

// groupBalls 返回某一组的全部球号，用于摆球与「还剩几颗」的展示。
func groupBalls(group BallGroup) []string {
	if group == GroupSolids {
		return []string{"1", "2", "3", "4", "5", "6", "7"}
	}
	if group == GroupStripes {
		return []string{"9", "10", "11", "12", "13", "14", "15"}
	}
	return nil
}

// RackBall 是一颗摆球。坐标取自七尺台的真实开球位置，与前端 RACK_EIGHTBALL、
// pool-sim 预热用的那份是同一组数。
type RackBall struct {
	ID string
	X  float64
	Y  float64
}

// StandardRack 是八球开局的 16 颗球。
//
// 球号是按八球规则自己排的：8 号在三角形中心，顶点球摆在置球点上。
// pooltool 每次 get_rack 都重新洗牌，所以「逐颗对齐」只可能指坐标，不可能指编号。
var StandardRack = []RackBall{
	{ID: sim.CueBallID, X: 0.594371, Y: 0.455658},
	{ID: "4", X: 0.495301, Y: 1.485928},
	{ID: "12", X: 0.466705, Y: 1.535447},
	{ID: "5", X: 0.523904, Y: 1.535439},
	{ID: "9", X: 0.552501, Y: 1.584991},
	{ID: sim.EightBallID, X: 0.495308, Y: 1.585009},
	{ID: "7", X: 0.438092, Y: 1.584971},
	{ID: "15", X: 0.409471, Y: 1.634507},
	{ID: "1", X: 0.466716, Y: 1.634516},
	{ID: "13", X: 0.523894, Y: 1.634524},
	{ID: "3", X: 0.581132, Y: 1.634539},
	{ID: "10", X: 0.609732, Y: 1.684068},
	{ID: "2", X: 0.552511, Y: 1.684074},
	{ID: "11", X: 0.495296, Y: 1.684069},
	{ID: "14", X: 0.438113, Y: 1.684064},
	{ID: "6", X: 0.380883, Y: 1.68409},
}
