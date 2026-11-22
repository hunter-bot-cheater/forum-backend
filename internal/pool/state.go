package pool

import (
	"strconv"
	"time"

	"forum-backend/internal/model"
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
	// SeatSpectator 是不占座位的观战者。快照里的 seat 为它时，
	// 客户端应当只渲染不出杆：观战者没有回合，也不参与结算。
	SeatSpectator Seat = -1
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

// PlayerBrief 是一名玩家在快照里的公开信息。
// 只带展示需要的三样，不把整份用户资料塞进每一条消息——一局要广播几十次快照。
type PlayerBrief struct {
	UserID   uint   `json:"user_id"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
}

// Snapshot 是给前端的完整对局快照，字段与 web/src/game/types.ts 的 MatchSnapshot 一致。
//
// Balls 用切片而不是 map：前端按 id 找球，而 map 的遍历顺序随机，
// 每次快照的球顺序都不一样会让 reconcile 无谓地抖动。
type Snapshot struct {
	MatchID    uint             `json:"match_id"`
	RoomCode   string           `json:"room_code"`
	Status     string           `json:"status"`
	Seat       Seat             `json:"seat"`
	Turn       Seat             `json:"turn"`
	DeadlineMS int              `json:"deadline_ms"`
	Balls      []sim.ResultBall `json:"balls"`
	Group      [2]BallGroup     `json:"group"`
	Ready      [2]bool          `json:"ready"`
	BallInHand bool             `json:"ball_in_hand"`
	CuePlaced  bool             `json:"cue_placed"`
	ShotSeq    int              `json:"shot_seq"`
	Players    [2]PlayerBrief   `json:"players"`
	Winner     *Seat            `json:"winner"`
	WinReason  string           `json:"win_reason"`
}

// MatchState 是一局对局的全部状态。字段只在 actor 协程里改。
type MatchState struct {
	MatchID  uint
	RoomCode string
	Status   string
	Players  [2]PlayerBrief

	Turn      Seat
	BreakDone bool
	Groups    [2]BallGroup
	Balls     []sim.ResultBall

	Ready [2]bool
	// BallInHand 表示本回合可以自由摆放母球，直到出杆之前都有效。
	//
	// 落位一次不清它：玩家摆下去之后往往要再挪两次才满意，
	// 收回权限就只能靠出杆前的那一次落位碰运气。
	BallInHand bool
	// CuePlaced 表示这次手中球已经摆过至少一次。
	// 出杆的闸门看的是它，而不是 BallInHand——母球在袋里时直接开算，
	// pooltool 会抛 argmin of an empty sequence。
	CuePlaced bool
	ShotSeq   int
	// Timeouts 记录每个座位连续超时的次数。
	// 该座位每成功击球一次就清零（见 handleShoot），
	// 所以「两次超时之间只要打过一杆」就不再算连续。
	Timeouts [2]int

	// Deadline 是本杆的读秒截止时间，零值表示不计时（房间还没开局）。
	Deadline  time.Time
	Winner    *Seat
	WinReason string
}

// newMatchState 按开局摆球造一份初始状态。
//
// 母球也在 Balls 里：把它单独拎出来会让「hand in ball 时母球不在台面上」这类
// 中间态需要两套表示，而规则与渲染都只需要「一颗在台面上的球」。
func newMatchState(matchID uint, roomCode string, players [2]PlayerBrief) MatchState {
	balls := make([]sim.ResultBall, 0, len(StandardRack))
	for _, item := range StandardRack {
		balls = append(balls, sim.ResultBall{ID: item.ID, X: item.X, Y: item.Y, OnTable: true})
	}

	return MatchState{
		MatchID:  matchID,
		RoomCode: roomCode,
		Status:   model.PoolStatusPlaying,
		Players:  players,
		Turn:     Seat0,
		Balls:    balls,
		Ready:    [2]bool{true, true},
	}
}

// newRoomState 造一个还没开局的房间：双方都没准备，不计时。
//
// 房间与对局共用同一个 actor：准备状态要能被两边同时观察，
// 分开存会让「我刚点准备、对手正好点准备」这类交错出现两份真相。
func newRoomState(matchID uint, roomCode string, players [2]PlayerBrief) MatchState {
	state := newMatchState(matchID, roomCode, players)
	state.Status = model.PoolStatusWaiting
	state.Ready = [2]bool{false, false}
	state.Deadline = time.Time{}
	return state
}

// ballByID 按球号找球。找不到返回 ok=false，调用方决定是忽略还是报错。
func (s *MatchState) ballByID(id string) (sim.ResultBall, bool) {
	for _, item := range s.Balls {
		if item.ID == id {
			return item, true
		}
	}
	return sim.ResultBall{}, false
}

// setBall 覆盖一颗球的球位。不存在的球号直接忽略：
// 能走到这里的球号都来自 pool-sim 的终局列表，与我们的球集合同源。
func (s *MatchState) setBall(ball sim.ResultBall) {
	for index := range s.Balls {
		if s.Balls[index].ID == ball.ID {
			s.Balls[index] = ball
			return
		}
	}
}

// onTable 返回击球前台面上的球号集合，给规则判定用。
func (s *MatchState) onTable() map[string]bool {
	set := make(map[string]bool, len(s.Balls))
	for _, item := range s.Balls {
		if item.OnTable {
			set[item.ID] = true
		}
	}
	return set
}

// ruleState 把对局状态裁成规则需要的那几项。
func (s *MatchState) ruleState() RuleState {
	return RuleState{
		Shooter:   s.Turn,
		BreakDone: s.BreakDone,
		Groups:    s.Groups,
		OnTable:   s.onTable(),
	}
}

// simBalls 造出发给 pool-sim 的球位：只有还在台面上的球参与计算。
func (s *MatchState) simBalls() []sim.RequestBall {
	balls := make([]sim.RequestBall, 0, len(s.Balls))
	for _, item := range s.Balls {
		if !item.OnTable {
			continue
		}
		balls = append(balls, sim.RequestBall{ID: item.ID, X: item.X, Y: item.Y, Z: item.Z})
	}
	return balls
}

// snapshot 生成给某个视角的快照。Seat 是「看的人坐在哪」，观战时传 -1。
func (s *MatchState) snapshot(viewer Seat, now time.Time) Snapshot {
	snapshot := Snapshot{
		MatchID:    s.MatchID,
		RoomCode:   s.RoomCode,
		Status:     s.Status,
		Seat:       viewer,
		Turn:       s.Turn,
		Balls:      append([]sim.ResultBall(nil), s.Balls...),
		Group:      s.Groups,
		Ready:      s.Ready,
		BallInHand: s.BallInHand,
		CuePlaced:  s.CuePlaced,
		ShotSeq:    s.ShotSeq,
		Players:    s.Players,
		Winner:     s.Winner,
		WinReason:  s.WinReason,
	}
	if !s.Deadline.IsZero() {
		left := int(s.Deadline.Sub(now) / time.Millisecond)
		if left < 0 {
			left = 0
		}
		snapshot.DeadlineMS = left
	}
	return snapshot
}
