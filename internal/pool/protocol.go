package pool

import (
	"encoding/json"
	"time"

	"forum-backend/internal/pool/sim"
)

// 对局 WebSocket 的上行与下行消息。
//
// 统一信封：type 定路由，seq 用于「请求—应答」配对，ts 让客户端算延迟。
// 不用裸结构体是因为将来加观战、加表情、加聊天都是同一个连接，
// 每加一种消息就多一个顶层字段的话，解析逻辑会变成一堆 if。

// 聊天的两条硬约束：正文字符上限，与同一座位两次发言的最小间隔。
//
// 上限按**字符**算而不是字节：一句中文 20 个字是 60 字节，按字节限长
// 会把正常中文截成半句。120 字够说清一件事，也不至于把面板撑成小作文。
// 间隔取 700ms 是「手快连发两句没问题、按住回车刷不了屏」的量级。
const (
	chatMaxRunes = 120
	chatInterval = 700 * time.Millisecond
)

// 下行消息类型。
const (
	MsgJoined = "joined" // 进房成功，带座位与本局配置
	MsgState  = "state"  // 完整快照，进房、重连、观战统一用这一条
	MsgTurn   = "turn"   // 换人，带读秒截止时间
	MsgShot   = "shot"   // 一杆的结果：轨迹、事件、终局球位
	MsgFoul   = "foul"   // 犯规说明
	MsgResult = "result" // 结算
	MsgPeer   = "peer"   // 对手状态：joined / left / offline / declined
	MsgAim    = "aim"    // 一方正在瞄准：角度与力度，实时转给对面与观战者
	MsgChat   = "chat"   // 对局内的一条聊天（文字或表情）
	MsgError  = "error"  // 业务错误，seq 与请求配对
)

// 上行消息类型。
const (
	CmdJoin     = "join"
	CmdShoot    = "shoot"
	CmdPlaceCue = "place_cue"
	CmdAim      = "aim"
	CmdChat     = "chat"
	CmdLeave    = "leave"
	CmdResume   = "resume"
)

// 对手状态事件的取值。
const (
	PeerJoined  = "joined"
	PeerLeft    = "left"
	PeerOffline = "offline"
)

// 应用层关闭码。
//
// 浏览器的 WebSocket 读不到握手前的 HTTP 状态码（Upgrade 之前 409 也好
// 404 也好，一律只表现为一次连接失败），但读得到关闭码。
// 所以「重试也没用」的失败必须用关闭码说清楚，否则客户端会当成网络抖动
// 白白退避重试几十秒。
//
// 4000 以上归应用自己用（1000-2999 是协议保留，3000-3999 留给库）。
const (
	// CloseSpectatorFull 表示观战名额已经满了。
	CloseSpectatorFull = 4001
	// CloseMatchGone 表示这一局已经不在了（结束并被回收，或不是自己的局）。
	CloseMatchGone = 4004
)

// Message 是下行信封。
type Message struct {
	Type string `json:"type"`
	Seq  int    `json:"seq,omitempty"`
	TS   int64  `json:"ts"`
	Data any    `json:"data,omitempty"`
}

// ClientMessage 是上行信封。Data 交给各命令自己解，信封层不关心它的形状。
//
// Data 必须是 json.RawMessage 而不是 []byte：后者在 encoding/json 里
// 对应的是 base64 字符串，客户端发对象过来会直接解不动。
type ClientMessage struct {
	Type string          `json:"type"`
	Seq  int             `json:"seq"`
	TS   int64           `json:"ts"`
	Data json.RawMessage `json:"data,omitempty"`
}

// JoinedData 是进房成功的应答。
type JoinedData struct {
	MatchID  uint   `json:"match_id"`
	RoomCode string `json:"room_code"`
	Seat     Seat   `json:"seat"`
	// Practice 表示这是练习局（对手是本地假人，服务端不等待对方出杆）。
	Practice bool `json:"practice"`
}

// TurnData 是换人通知。DeadlineMS 是这一杆还剩多少毫秒。
type TurnData struct {
	Seat       Seat `json:"seat"`
	DeadlineMS int  `json:"deadline_ms"`
}

// ShotData 是一杆的结果，广播给双方与观战者。
//
// Frames 原样透传 pool-sim 的输出（[球号, x, y, z] 的混合数组），
// 前端 animator 直接拿它插值，服务端不做任何加工。
type ShotData struct {
	Seq      int              `json:"seq"`
	Seat     Seat             `json:"seat"`
	Params   sim.CueParams    `json:"params"`
	Duration float64          `json:"duration"`
	Events   []sim.Event      `json:"events"`
	Frames   []sim.Frame      `json:"frames"`
	Final    []sim.ResultBall `json:"final"`
	SimMS    int              `json:"sim_ms"`
}

// FoulData 是犯规说明。文案由服务端给，前端不要自己拼。
type FoulData struct {
	Code       string `json:"code"`
	Reason     string `json:"reason"`
	BallInHand bool   `json:"ball_in_hand"`
	Seat       Seat   `json:"seat"`
}

// ResultData 是结算。
type ResultData struct {
	Winner Seat   `json:"winner"`
	Reason string `json:"reason"`
	// Series 是结算之后这一轮连续对局的比分，按座位排（含刚打完这一局）。
	//
	// 单独带一份而不是等下一次快照：判负与离场这两条路径不会再补快照，
	// 只靠快照的话，顶上的比分要等到下一局开始才会跳。
	Series    [2]int `json:"series"`
	EloDelta0 int    `json:"elo_delta_0"`
	EloDelta1 int    `json:"elo_delta_1"`
}

// PeerData 是对手状态变化。
type PeerData struct {
	Event string `json:"event"`
	Seat  Seat   `json:"seat"`
}

// AimData 是某个人此刻的瞄准姿态，转给对面与观战者用。
//
// 带角度、力度与击球点：对面要看到的不只是杆子朝哪，还有力度条推到了哪、
// 这一杆加了什么塞。三者都由客户端自己算外观，服务端只负责搬运。
// 这条消息不进状态机，丢了也无所谓——下一帧马上就到。
//
// A 是左右塞、B 是高低杆，都在 -1 到 1 之间，口径与 Shoot 的上行参数一致。
type AimData struct {
	Seat  Seat    `json:"seat"`
	Phi   float64 `json:"phi"`
	Power float64 `json:"power"`
	A     float64 `json:"a"`
	B     float64 `json:"b"`
}

// ErrorData 是业务错误。Code 是稳定标识，Message 是给玩家看的中文。
type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ChatData 是对局内的一条聊天。
//
// 只做实时转发，不落库：对局内的聊天是「打这一局时说的话」，
// 局散了就没有回看的意义，存下来反而要处理一堆隐私与清理的事。
// 观战者收得到（他们看的是同一块屏），但发不了。
//
// ID 由服务端单调发号（actor 里自增），客户端拿它当列表的 key：
// 重连时快照不带历史消息，只有在线那几条，用序号做 key 不会错位。
type ChatData struct {
	Seat Seat   `json:"seat"`
	Text string `json:"text"`
	ID   int64  `json:"id"`
	TS   int64  `json:"ts"`
}

// 错误码。前端按码决定是提示、回滚到上一次快照，还是直接断开重连。
const (
	ErrCodeNotYourTurn  = "not_your_turn"
	ErrCodeBadParams    = "bad_params"
	ErrCodeNotPlaying   = "not_playing"
	ErrCodeSimFailed    = "sim_failed"
	ErrCodeSeatTaken    = "seat_taken"
	ErrCodeNotInHand    = "not_ball_in_hand"
	ErrCodeIllegalPlace = "illegal_placement"
	ErrCodeInternal     = "internal"
)
