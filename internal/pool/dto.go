package pool

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
