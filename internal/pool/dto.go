package pool

import (
	"time"

	"forum-backend/internal/pool/sim"
)

// 这一层是对外的 HTTP 结构体，字段名与前端 web/src/api/pool.ts 逐字对齐。
// 给内部用的结构（Snapshot / MatchState）不直接往外吐：
// 它们会随实现变动，而契约一旦发出去就得兼容。

// inviteCreateReq 是发起邀请的请求体。
type inviteCreateReq struct {
	ToUserID uint `json:"to_user_id" binding:"required"`
}

// inviteResp 是发起邀请的响应。room_code 给前端展示，match_id 是进房要用的。
type inviteResp struct {
	InviteID  string `json:"invite_id"`
	MatchID   uint   `json:"match_id"`
	RoomCode  string `json:"room_code"`
	ExpiresIn int    `json:"expires_in"`
}

// playerResp 是玩家在房间与对局里的展示信息。
type playerResp struct {
	UserID   uint   `json:"user_id"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
}

// pendingInviteResp 是「我收到的邀请」列表里的一条。
type pendingInviteResp struct {
	InviteID  string     `json:"invite_id"`
	MatchID   uint       `json:"match_id"`
	RoomCode  string     `json:"room_code"`
	From      playerResp `json:"from"`
	ExpiresIn int        `json:"expires_in"`
}

// acceptInviteResp 是接受邀请后的响应，前端拿它直接跳房间页。
type acceptInviteResp struct {
	MatchID  uint   `json:"match_id"`
	RoomCode string `json:"room_code"`
}

// roomResp 是房间页轮询的快照。
//
// ready 是两个座位的准备标记，seat 是「你坐哪」，前端据此把自己放在左边。
// 不把整份对局快照吐出来：房间页只需要这几项，多给的字段将来都改不动。
type roomResp struct {
	MatchID  uint          `json:"match_id"`
	RoomCode string        `json:"room_code"`
	Status   string        `json:"status"`
	Seat     Seat          `json:"seat"`
	Ready    [2]bool       `json:"ready"`
	Players  [2]playerResp `json:"players"`
}

// readyReq 是准备 / 取消准备的请求体。
type readyReq struct {
	Ready bool `json:"ready"`
}

// joinRoomReq 是「按房号进房」的请求体。
//
// Ready 只由分享链接带上来：点链接这个动作本身就等于「我要打」，
// 进房后服务端顺手替他按下准备，房主那边点一次就能开局。
// 手动输房号的入口不传它——那种场景里两个人常常还要先说两句。
type joinRoomReq struct {
	RoomCode string `json:"room_code"`
	Ready    bool   `json:"ready"`
}

// roomJoinedResp 是「有人进了我的房间」那条推送。
//
// 带上进房人的昵称与头像：房主那一屏要立刻把空位换成人，
// 只给一个 id 的话还得再请求一次用户资料。
type roomJoinedResp struct {
	MatchID  uint       `json:"match_id"`
	RoomCode string     `json:"room_code"`
	Player   playerResp `json:"player"`
}

// onlineUserResp 是「现在在线的人」里的一条。
//
// following 只用来分组与排序（关注的排前面），不作为「能不能邀请」的条件：
// 台球不该是只有互相关注才能玩的游戏。
type onlineUserResp struct {
	UserID    uint   `json:"user_id"`
	Nickname  string `json:"nickname"`
	Avatar    string `json:"avatar"`
	Following bool   `json:"following"`
}

// ratingResp 是战绩汇总。
//
// 不发胜率：它是一个派生值，发出去就有「后端算的还是前端算的」两种口径。
// 前端拿 wins / losses 自己除，页面上那一个数字不可能和列表对不上。
type ratingResp struct {
	Elo    int `json:"elo"`
	Wins   int `json:"wins"`
	Losses int `json:"losses"`
}

// recordResp 是战绩列表里的一行。
//
// 带 opponent 而不是两个玩家：列表是「我的战绩」，另一方永远是对手，
// 给两份资料只会让前端每次再判断一次哪份是自己。
type recordResp struct {
	MatchID   uint       `json:"match_id"`
	Opponent  playerResp `json:"opponent"`
	Won       bool       `json:"won"`
	WinReason string     `json:"win_reason"`
	ShotCount int        `json:"shot_count"`
	EndedAt   *time.Time `json:"ended_at"`
}

// recordsResp 是「我的战绩」整页数据。
type recordsResp struct {
	Rating ratingResp   `json:"rating"`
	List   []recordResp `json:"list"`
	Total  int64        `json:"total"`
	Page   int          `json:"page"`
	Size   int          `json:"size"`
}

// replayShotResp 是回放里的一杆。frames 的形状与对局时的 shot 帧完全一致，
// 前端用同一个 animator 播，不需要第二套解码。
type replayShotResp struct {
	Seq      int         `json:"seq"`
	Seat     Seat        `json:"seat"`
	Duration float64     `json:"duration"`
	Frames   []sim.Frame `json:"frames"`
}

// replayResp 是一局的回放。
type replayResp struct {
	MatchID uint             `json:"match_id"`
	Players [2]playerResp    `json:"players"`
	Winner  Seat             `json:"winner"`
	Reason  string           `json:"win_reason"`
	Shots   []replayShotResp `json:"shots"`
}

// liveMatchResp 是观战列表里的一条。
//
// 只带「要不要点进去看」需要的信息：谁在打、打到第几杆、还有没有位置。
// 不带球位——那是点进去之后由 WebSocket 快照给的，
// 列表每次刷新都拖着 16 颗球的坐标没有意义，还会让人误以为列表是实时的。
type liveMatchResp struct {
	MatchID       uint          `json:"match_id"`
	Status        string        `json:"status"`
	Turn          Seat          `json:"turn"`
	Players       [2]playerResp `json:"players"`
	ShotSeq       int           `json:"shot_seq"`
	Spectators    int           `json:"spectators"`
	SpectatorsMax int           `json:"spectators_max"`
}

// toRoomResp 把对局状态裁成房间页需要的形状。
func toRoomResp(snapshot Snapshot) roomResp {
	resp := roomResp{
		MatchID:  snapshot.MatchID,
		Status:   snapshot.Status,
		Seat:     snapshot.Seat,
		Ready:    snapshot.Ready,
		RoomCode: snapshot.RoomCode,
	}
	for seat := Seat0; seat <= Seat1; seat++ {
		resp.Players[seat] = playerResp{
			UserID:   snapshot.Players[seat].UserID,
			Nickname: snapshot.Players[seat].Nickname,
			Avatar:   snapshot.Players[seat].Avatar,
		}
	}
	return resp
}
