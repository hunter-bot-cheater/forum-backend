package pool

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/httputil"
	"forum-backend/internal/model"
	"forum-backend/internal/realtime"
	"forum-backend/internal/user"
	"forum-backend/pkg/response"
)

// InvitePusher 是邀请到达时用的实时推送能力。抽成接口只为测试时能塞假实现。
type InvitePusher interface {
	Push(userID uint, msg realtime.PushMessage)
}

// UserBriefs 是「按 id 批量取展示资料」的读接口。
// pool 只需要昵称与头像，不需要用户模块的其它能力。
type UserBriefs interface {
	Briefs(ctx context.Context, ids []uint) (map[uint]user.Brief, error)
}

// Handler 是台球模块的 HTTP 入口：邀请、房间、准备。
//
// 对局过程中的出杆不走 HTTP，全在 /api/ws/game 上。
type Handler struct {
	manager *Manager
	invites *Invites
	store   *Store
	users   UserBriefs
	pusher  InvitePusher
}

// NewHandler 由 router 在装配期调用。
func NewHandler(manager *Manager, invites *Invites, store *Store, users UserBriefs, pusher InvitePusher) *Handler {
	return &Handler{manager: manager, invites: invites, store: store, users: users, pusher: pusher}
}

// CreateInvite godoc
// @Summary      邀请关注的人打一局
// @Description  只能邀请自己关注的人；同一个人同时只能挂一条待接受的邀请。
// @Tags         pool
// @Accept       json
// @Produce      json
// @Param        body  body  pool.inviteCreateReq  true  "被邀请者"
// @Success      200   {object}  httputil.SwaggerEnvelope{data=pool.inviteResp}
// @Failure      400   {string}  string  "参数不合法或对方不在你的关注列表里"
// @Failure      401   {string}  string  "未登录"
// @Failure      409   {string}  string  "已有一条待接受的邀请"
// @Security     Bearer
// @Router       /pool/invites [post]
// CreateInvite 处理 POST /api/pool/invites。
func (h *Handler) CreateInvite(c *gin.Context) {
	ctx := c.Request.Context()
	fromID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	var req inviteCreateReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ToUserID == 0 {
		response.Error(c, http.StatusBadRequest, "请选择要邀请的人")
		return
	}
	if req.ToUserID == fromID {
		response.Error(c, http.StatusBadRequest, "不能邀请自己")
		return
	}

	following, err := h.store.IsFollowing(ctx, fromID, req.ToUserID)
	if err != nil {
		httputil.LogRequestError(c, "check following", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}
	if !following {
		response.Error(c, http.StatusBadRequest, "只能邀请你关注的人")
		return
	}

	briefs, err := h.users.Briefs(ctx, []uint{fromID, req.ToUserID})
	if err != nil {
		httputil.LogRequestError(c, "load player briefs", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	match, err := h.createRoom(ctx, fromID, req.ToUserID)
	if err != nil {
		if errors.Is(err, ErrInviteBusy) {
			response.Error(c, http.StatusConflict, "你已有一条待接受的邀请")
			return
		}
		httputil.LogRequestError(c, "create pool room", err)
		response.Error(c, http.StatusInternalServerError, "建房失败，请重试")
		return
	}

	players := [2]PlayerBrief{
		briefOf(briefs, fromID),
		briefOf(briefs, req.ToUserID),
	}
	h.manager.Create(newRoomState(match.ID, match.RoomCode, players))

	invite, err := h.invites.Create(fromID, req.ToUserID, match.ID, match.RoomCode)
	if err != nil {
		// 建房已经成功但邀请发不出去（并发重复邀请），把房间收掉再报 409，
		// 否则会留下一个没人能进的房间。库里那行也标成过期，免得列表里挂着。
		h.manager.Remove(match.ID)
		if expireErr := h.store.ExpireMatch(ctx, match.ID); expireErr != nil {
			slog.ErrorContext(ctx, "expire pool match", "match_id", match.ID, "error", expireErr)
		}
		if errors.Is(err, ErrInviteBusy) {
			response.Error(c, http.StatusConflict, "你已有一条待接受的邀请")
			return
		}
		httputil.LogRequestError(c, "create invite", err)
		response.Error(c, http.StatusInternalServerError, "邀请发送失败")
		return
	}

	// 邀请到达时推一条实时消息，对方的任意页面都会弹出请求卡片。
	// 推送失败不影响邀请本身：对方刷新一下仍能从 pending 列表里看到。
	h.pusher.Push(req.ToUserID, realtime.PushMessage{
		Type: "pool_invite",
		Data: pendingInviteResp{
			InviteID:  invite.ID,
			MatchID:   match.ID,
			RoomCode:  match.RoomCode,
			From:      toPlayerResp(players[0]),
			ExpiresIn: int(InviteTTL / time.Second),
		},
	})

	response.OK(c, inviteResp{
		InviteID:  invite.ID,
		MatchID:   match.ID,
		RoomCode:  match.RoomCode,
		ExpiresIn: int(InviteTTL / time.Second),
	})
}

// createRoom 建房。房号撞唯一索引就换一个重试。
//
// 重试而不是先查后用：查与插之间有窗口，唯一索引是唯一可靠的判据。
func (h *Handler) createRoom(ctx context.Context, fromID, toID uint) (*matchRow, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		code, err := NewRoomCode()
		if err != nil {
			return nil, err
		}
		created, err := h.store.CreateMatch(ctx, fromID, toID, code)
		if err == nil {
			return &matchRow{ID: created.ID, RoomCode: created.RoomCode}, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// matchRow 是建房结果的内部表示，避免 handler 其它地方依赖 GORM 模型。
type matchRow struct {
	ID       uint
	RoomCode string
}

// PendingInvites godoc
// @Summary      我收到的待处理邀请
// @Tags         pool
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=[]pool.pendingInviteResp}
// @Failure      401  {string}  string  "未登录"
// @Security     Bearer
// @Router       /pool/invites/pending [get]
// PendingInvites 处理 GET /api/pool/invites/pending。
func (h *Handler) PendingInvites(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	pending := h.invites.PendingFor(userID)
	items := make([]pendingInviteResp, 0, len(pending))
	if len(pending) == 0 {
		response.OK(c, items)
		return
	}

	fromIDs := make([]uint, 0, len(pending))
	for _, invite := range pending {
		fromIDs = append(fromIDs, invite.FromID)
	}
	briefs, err := h.users.Briefs(ctx, fromIDs)
	if err != nil {
		httputil.LogRequestError(c, "load inviter briefs", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	for _, invite := range pending {
		items = append(items, pendingInviteResp{
			InviteID:  invite.ID,
			MatchID:   invite.MatchID,
			RoomCode:  invite.RoomCode,
			From:      toPlayerResp(briefOf(briefs, invite.FromID)),
			ExpiresIn: remainingSeconds(invite.CreatedAt),
		})
	}
	response.OK(c, items)
}

// AcceptInvite godoc
// @Summary      接受邀请
// @Description  返回 match_id 与房号，前端据此进房间页。
// @Tags         pool
// @Produce      json
// @Param        id   path  string  true  "邀请 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=pool.acceptInviteResp}
// @Failure      401  {string}  string  "未登录"
// @Failure      404  {string}  string  "邀请不存在或已失效"
// @Security     Bearer
// @Router       /pool/invites/{id}/accept [post]
// AcceptInvite 处理 POST /api/pool/invites/:id/accept。
func (h *Handler) AcceptInvite(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	invite, ok := h.usableInvite(c, userID)
	if !ok {
		return
	}
	if !h.invites.SetStatus(invite.ID, InviteAccepted) {
		response.Error(c, http.StatusNotFound, "邀请已失效")
		return
	}

	// 通知邀请人：对方接受了，快进房间。
	// 邀请人此时可能还停在大厅，这条消息让页面直接跳到房间页。
	h.pusher.Push(invite.FromID, realtime.PushMessage{
		Type: "pool_invite_accepted",
		Data: acceptInviteResp{MatchID: invite.MatchID, RoomCode: invite.RoomCode},
	})

	response.OK(c, acceptInviteResp{MatchID: invite.MatchID, RoomCode: invite.RoomCode})
}

// DeclineInvite godoc
// @Summary      拒绝邀请
// @Tags         pool
// @Produce      json
// @Param        id   path  string  true  "邀请 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=map[string]bool}
// @Failure      401  {string}  string  "未登录"
// @Failure      404  {string}  string  "邀请不存在或已失效"
// @Security     Bearer
// @Router       /pool/invites/{id}/decline [post]
// DeclineInvite 处理 POST /api/pool/invites/:id/decline。
func (h *Handler) DeclineInvite(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	invite, ok := h.usableInvite(c, userID)
	if !ok {
		return
	}
	h.invites.SetStatus(invite.ID, InviteDeclined)

	// 房间没人来了，直接收掉，房号随之释放。
	h.manager.Remove(invite.MatchID)

	h.pusher.Push(invite.FromID, realtime.PushMessage{
		Type: "pool_invite_declined",
		Data: acceptInviteResp{MatchID: invite.MatchID, RoomCode: invite.RoomCode},
	})
	response.OK(c, map[string]bool{"declined": true})
}

// Room godoc
// @Summary      房间快照
// @Description  房间页每 1.5 秒轮询一次；status 变成 playing 就跳对局页。
// @Tags         pool
// @Produce      json
// @Param        id   path  int  true  "对局 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=pool.roomResp}
// @Failure      401  {string}  string  "未登录"
// @Failure      403  {string}  string  "你不在这个房间里"
// @Failure      404  {string}  string  "房间不存在或已结束"
// @Security     Bearer
// @Router       /pool/rooms/{id} [get]
// Room 处理 GET /api/pool/rooms/:id。
func (h *Handler) Room(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	match, seat, ok := h.matchFor(c, userID)
	if !ok {
		return
	}

	snapshot, err := match.Snapshot(seat)
	if err != nil {
		response.Error(c, http.StatusNotFound, "房间已关闭")
		return
	}
	response.OK(c, toRoomResp(snapshot))
}

// Ready godoc
// @Summary      准备 / 取消准备
// @Description  双方都准备好后 status 变成 playing，房间页据此跳对局页。
// @Tags         pool
// @Accept       json
// @Produce      json
// @Param        id    path  int             true  "对局 ID"
// @Param        body  body  pool.readyReq   true  "准备状态"
// @Success      200   {object}  httputil.SwaggerEnvelope{data=pool.roomResp}
// @Failure      401   {string}  string  "未登录"
// @Failure      409   {string}  string  "对局已经开始"
// @Security     Bearer
// @Router       /pool/rooms/{id}/ready [post]
// Ready 处理 POST /api/pool/rooms/:id/ready。
func (h *Handler) Ready(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	var req readyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "参数不合法")
		return
	}

	match, seat, ok := h.matchFor(c, userID)
	if !ok {
		return
	}

	if err := match.SetReady(seat, req.Ready); err != nil {
		if errors.Is(err, ErrNotPlaying) {
			response.Error(c, http.StatusConflict, "对局已经开始")
			return
		}
		response.Error(c, http.StatusBadRequest, "操作失败")
		return
	}

	snapshot, err := match.Snapshot(seat)
	if err != nil {
		response.Error(c, http.StatusNotFound, "房间已关闭")
		return
	}

	// 双方都准备好时状态翻成 playing，这里顺手把库里那行也推进到进行中。
	// 落库失败不回滚内存状态：对局已经在跑了，让它停下来的代价更大。
	if snapshot.Status == model.PoolStatusPlaying {
		if err := h.store.MarkPlaying(ctx, snapshot.MatchID); err != nil {
			slog.ErrorContext(ctx, "mark pool match playing", "match_id", snapshot.MatchID, "error", err)
		}
	}
	response.OK(c, toRoomResp(snapshot))
}

// usableInvite 读出当前用户能处理的邀请，不能处理时已写好响应。
func (h *Handler) usableInvite(c *gin.Context, userID uint) (*Invite, bool) {
	invite, ok := h.invites.Get(c.Param("id"))
	if !ok || invite.ToID != userID || invite.Status != InvitePending {
		// 不区分「不存在」与「不是发给你的」：说什么都会让人知道这个 id 存在。
		response.Error(c, http.StatusNotFound, "邀请已失效")
		return nil, false
	}
	return invite, true
}

// matchFor 按路径里的 id 找出对局与当前用户的座位。
//
// 房间页与准备接口都要走这一步：先确认这局还在内存里，再确认这个人是局中人。
func (h *Handler) matchFor(c *gin.Context, userID uint) (*Match, Seat, bool) {
	matchID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || matchID == 0 {
		response.Error(c, http.StatusBadRequest, "对局编号不合法")
		return nil, 0, false
	}

	match, ok := h.manager.Get(uint(matchID))
	if !ok {
		response.Error(c, http.StatusNotFound, "房间不存在或已结束")
		return nil, 0, false
	}

	seat, ok := seatOf(match, userID)
	if !ok {
		response.Error(c, http.StatusForbidden, "你不在这个房间里")
		return nil, 0, false
	}
	return match, seat, true
}

// briefOf 从批量结果里取一个人，取不到就返回一个只带 id 的空资料。
// 取不到的情况只可能是用户刚被删，界面显示「用户已注销」比整页报错好。
func briefOf(briefs map[uint]user.Brief, userID uint) PlayerBrief {
	brief, ok := briefs[userID]
	if !ok {
		return PlayerBrief{UserID: userID}
	}
	return PlayerBrief{UserID: userID, Nickname: brief.DisplayName(), Avatar: brief.AvatarURL}
}

// toPlayerResp 把内部资料转成对外结构。
func toPlayerResp(brief PlayerBrief) playerResp {
	return playerResp{UserID: brief.UserID, Nickname: brief.Nickname, Avatar: brief.Avatar}
}

// remainingSeconds 算一条邀请还剩多少秒，最小 0。
func remainingSeconds(createdAt time.Time) int {
	left := InviteTTL - time.Since(createdAt)
	if left < 0 {
		return 0
	}
	return int(left / time.Second)
}
