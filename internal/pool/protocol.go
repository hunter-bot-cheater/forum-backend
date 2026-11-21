package pool

import (
	"encoding/json"

	"forum-backend/internal/pool/sim"
)

// 对局 WebSocket 的上行与下行消息。
//
// 统一信封：type 定路由，seq 用于「请求—应答」配对，ts 让客户端算延迟。
// 不用裸结构体是因为将来加观战、加表情、加聊天都是同一个连接，
// 每加一种消息就多一个顶层字段的话，解析逻辑会变成一堆 if。

// 下行消息类型。
const (
	MsgJoined = "joined" // 进房成功，带座位与本局配置
	MsgState  = "state"  // 完整快照，进房、重连、观战统一用这一条
	MsgTurn   = "turn"   // 换人，带读秒截止时间
	MsgShot   = "shot"   // 一杆的结果：轨迹、事件、终局球位
	MsgFoul   = "foul"   // 犯规说明
	MsgResult = "result" // 结算
	MsgPeer   = "peer"   // 对手状态：joined / left / offline / declined
	MsgError  = "error"  // 业务错误，seq 与请求配对
)

// 上行消息类型。
const (
	CmdJoin     = "join"
	CmdShoot    = "shoot"
	CmdPlaceCue = "place_cue"
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
	// CloseSpectatorFull 表示观战名额在握手与进房之间被抢走了。
	CloseSpectatorFull = 4001
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
	Winner    Seat   `json:"winner"`
	Reason    string `json:"reason"`
	EloDelta0 int    `json:"elo_delta_0"`
	EloDelta1 int    `json:"elo_delta_1"`
}

// PeerData 是对手状态变化。
type PeerData struct {
	Event string `json:"event"`
	Seat  Seat   `json:"seat"`
}

// ErrorData 是业务错误。Code 是稳定标识，Message 是给玩家看的中文。
type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
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
