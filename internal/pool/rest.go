package pool

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"forum-backend/internal/httputil"
	"forum-backend/internal/model"
	"forum-backend/internal/realtime"
	"forum-backend/internal/user"
	"forum-backend/pkg/response"
)

// Realtime 是台球要用到的那部分实时能力：推一条消息，以及查谁现在在线。
//
// 抽成接口只为测试时能塞假实现；生产里就是全进程唯一的 *realtime.Hub。
type Realtime interface {
	Push(userID uint, msg realtime.PushMessage)
	IsOnline(userID uint) bool
	OnlineUserIDs() []uint
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
	hub     Realtime

	// roomLocks 是开房用的一组分片锁，见 lockRoom。
	roomLocks [roomLockStripes]sync.Mutex

	// rematches 是「再约一局」的声明登记表。
	rematches *Rematches

	// rematchMu 串行化「双方都点了 → 建新局」这一段，理由见 Rematch。
	rematchMu sync.Mutex

	// acceptMu 串行化「接受邀请」。
	//
	// 两个人互相邀请时，两个 accept 会并发跑：A 进 B 的房、B 进 A 的房，
	// 各自成功之后再各自把「自己开的房」作废，正好把对方刚进去的那间也作废了，
	// 最后两个人各坐在一间已作废的房里，谁都看不到谁。
	// 这个接口一分钟也用不了几次，一把全局锁比按用户对加锁简单得多。
	acceptMu sync.Mutex
}

// NewHandler 由 router 在装配期调用。
func NewHandler(manager *Manager, invites *Invites, store *Store, users UserBriefs, hub Realtime) *Handler {
	return &Handler{
		manager:   manager,
		invites:   invites,
		store:     store,
		users:     users,
		hub:       hub,
		rematches: NewRematches(),
	}
}

// CreateInvite godoc
// @Summary      邀请一个人打一局
// @Description  对方必须在线；房间就是我当前的那个房间，邀请只是把房号推给他。
// @Tags         pool
// @Accept       json
// @Produce      json
// @Param        body  body  pool.inviteCreateReq  true  "被邀请者"
// @Success      200   {object}  httputil.SwaggerEnvelope{data=pool.inviteResp}
// @Failure      400   {string}  string  "参数不合法或对方不在线"
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

	// 不再要求关注：大厅里能看到全部在线的人，谁都可以约。
	// 但要求在线——离线的邀请只能干等一分钟，对方根本不会知道。
	if !h.hub.IsOnline(req.ToUserID) {
		response.Error(c, http.StatusBadRequest, "对方不在线，把房号发给他更快")
		return
	}

	// 邀请指向我自己的房间：房号在我这一屏上，他接受之后进的就是这个房间。
	match, seat, err := h.myRoom(ctx, fromID)
	if err != nil {
		httputil.LogRequestError(c, "open my room", err)
		response.Error(c, http.StatusInternalServerError, "开房失败，请重试")
		return
	}
	// myRoom 也会把「正在打的那一局」还回来，那种情况下邀请没有意义。
	if info, ok := match.Live(); !ok || info.Status != model.PoolStatusWaiting {
		response.Error(c, http.StatusConflict, "你正在对局中，打完再约")
		return
	}
	if seat != Seat0 {
		response.Error(c, http.StatusConflict, "你已经在别人的房间里了，先退出来")
		return
	}

	roomCode := match.RoomCode()
	invite, err := h.invites.Create(fromID, req.ToUserID, match.ID(), roomCode)
	if err != nil {
		if errors.Is(err, ErrInviteBusy) {
			response.Error(c, http.StatusConflict, "你已有一条待接受的邀请")
			return
		}
		// 互相邀请：拦下来让他去接受对方那条，别两边各开一间房。
		if errors.Is(err, ErrInviteMutual) {
			response.Error(c, http.StatusConflict, "对方已经邀请你了，接受他那条邀请即可")
			return
		}
		httputil.LogRequestError(c, "create invite", err)
		response.Error(c, http.StatusInternalServerError, "邀请发送失败")
		return
	}

	briefs, err := h.users.Briefs(ctx, []uint{fromID})
	if err != nil {
		httputil.LogRequestError(c, "load player briefs", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	// 邀请到达时推一条实时消息，对方的任意页面都会弹出请求卡片。
	// 推送失败不影响邀请本身：对方刷新一下仍能从 pending 列表里看到。
	h.hub.Push(req.ToUserID, realtime.PushMessage{
		Type: "pool_invite",
		Data: pendingInviteResp{
			InviteID:  invite.ID,
			MatchID:   match.ID(),
			RoomCode:  roomCode,
			From:      toPlayerResp(briefOf(briefs, fromID)),
			ExpiresIn: int(InviteTTL / time.Second),
		},
	})

	response.OK(c, inviteResp{
		InviteID:  invite.ID,
		MatchID:   match.ID(),
		RoomCode:  roomCode,
		ExpiresIn: int(InviteTTL / time.Second),
	})
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
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 整个接受过程串起来跑，理由见 Handler.acceptMu。
	h.acceptMu.Lock()
	defer h.acceptMu.Unlock()

	invite, ok := h.usableInvite(c, userID)
	if !ok {
		return
	}

	// 先把座位占下来再改邀请状态：房间可能已经被别人用房号进了，
	// 那时候这条邀请就该算失效，而不是把人放进一个满员的房间。
	joined, err := h.store.JoinRoom(ctx, invite.MatchID, userID)
	if err != nil {
		httputil.LogRequestError(c, "join room by invite", err)
		response.Error(c, http.StatusInternalServerError, "进房失败，请重试")
		return
	}
	if !joined {
		// 座位已经有人了（大概是房主直接把房号发给了别人）。
		row, err := h.store.Match(ctx, invite.MatchID)
		if err == nil && row.HasPlayer(userID) {
			// 自己早就在房间里了：当成接受成功，重复点也只会到这里。
			h.invites.SetStatus(invite.ID, InviteAccepted)
			response.OK(c, acceptInviteResp{MatchID: invite.MatchID, RoomCode: invite.RoomCode})
			return
		}
		h.invites.SetStatus(invite.ID, InviteExpired)
		// 和按房号进房走同一个错误码：前端对「人满」的处理只写一处。
		response.ErrorWithCode(c, http.StatusConflict, response.ErrCodeRoomFull, msgRoomFull)
		return
	}

	if !h.invites.SetStatus(invite.ID, InviteAccepted) {
		// 抢到座位之后才发现这条邀请已经被别处处理掉（另一端的标签页先点了、
		// 或者已经过期）。座位得退回去，否则对方的房间里坐着一个不会点准备的人。
		h.rollbackSeat(ctx, invite.MatchID, userID)
		response.Error(c, http.StatusNotFound, "邀请已失效")
		return
	}

	if err := h.fillJoinedSeat(ctx, invite.MatchID, userID); err != nil {
		httputil.LogRequestError(c, "fill seat after invite", err)
		h.rollbackSeat(ctx, invite.MatchID, userID)
		response.Error(c, http.StatusInternalServerError, "进房失败，请重试")
		return
	}

	// 接受邀请的人原来那间房也要作废：一个人同时只能在一间房里等。
	h.retireOwnRooms(ctx, userID, invite.MatchID)
	// 他发出去、还没被回应的邀请跟着作废：那间房已经没了，
	// 对方再点接受只会看到「房间已被占用」。
	h.invites.CancelOutgoing(userID)
	// 以及「我曾以客人身份坐在别的房间」那种情况：房留着，座位要腾出来。
	h.vacateGuestSeats(ctx, userID, invite.MatchID)

	// 通知邀请人：对方接受了，房间页会立刻把空位换成人。
	h.hub.Push(invite.FromID, realtime.PushMessage{
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

	// 房间不跟着收：邀请人还在自己那间房里等着，房号还要留着给别人用。
	// 收掉反而是错的——他报给第三个人的房号会突然失效。
	h.hub.Push(invite.FromID, realtime.PushMessage{
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

// Records godoc
// @Summary      我的战绩
// @Description  返回积分、胜负总数与最近的对局，最近的在前。
// @Tags         pool
// @Produce      json
// @Param        page  query  int  false  "页码，从 1 开始"
// @Param        size  query  int  false  "每页条数，默认 10，上限 50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=pool.recordsResp}
// @Failure      401  {string}  string  "未登录"
// @Security     Bearer
// @Router       /pool/records [get]
// Records 处理 GET /api/pool/records。
func (h *Handler) Records(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	page, size := pageParams(c)
	rating, err := h.store.Rating(ctx, userID)
	if err != nil {
		httputil.LogRequestError(c, "load pool rating", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	matches, total, err := h.store.RecentMatches(ctx, userID, (page-1)*size, size)
	if err != nil {
		httputil.LogRequestError(c, "list pool matches", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	// 对手 id 一次凑齐批量取资料，不按行发请求：一页十行就是十次往返。
	opponents := make([]uint, 0, len(matches))
	for _, match := range matches {
		opponents = append(opponents, opponentOf(match, userID))
	}
	briefs := map[uint]user.Brief{}
	if len(opponents) > 0 {
		briefs, err = h.users.Briefs(ctx, opponents)
		if err != nil {
			// 取不到昵称不该让整页失败：战绩本身是数据，昵称只是装饰。
			slog.WarnContext(ctx, "load opponent briefs", "error", err)
			briefs = map[uint]user.Brief{}
		}
	}

	resp := recordsResp{
		Rating: ratingResp{Elo: rating.Elo, Wins: rating.Wins, Losses: rating.Losses},
		List:   make([]recordResp, 0, len(matches)),
		Total:  total,
		Page:   page,
		Size:   size,
	}
	for _, match := range matches {
		opponentID := opponentOf(match, userID)
		won := match.WinnerID != nil && *match.WinnerID == userID
		resp.List = append(resp.List, recordResp{
			MatchID:   match.ID,
			Opponent:  toPlayerResp(briefOf(briefs, opponentID)),
			Won:       won,
			WinReason: match.WinReason,
			ShotCount: match.ShotCount,
			EndedAt:   match.EndedAt,
		})
	}
	response.OK(c, resp)
}

// Replay godoc
// @Summary      对局回放
// @Description  只有这局的两位玩家能看，且必须已经结算。frames 与对局时的 shot 帧同形。
// @Tags         pool
// @Produce      json
// @Param        id   path  int  true  "对局 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=object}  "结构见 docs/12 的 12.6：players + shots[]，每杆带 frames"
// @Failure      401  {string}  string  "未登录"
// @Failure      403  {string}  string  "你不在这一局里"
// @Failure      404  {string}  string  "对局不存在"
// @Failure      409  {string}  string  "对局还没结束"
// @Security     Bearer
// @Router       /pool/matches/{id}/replay [get]
// Replay 处理 GET /api/pool/matches/:id/replay。
func (h *Handler) Replay(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	matchID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || matchID == 0 {
		response.Error(c, http.StatusBadRequest, "对局编号不合法")
		return
	}

	// 回放读库而不是读内存：对局结束后 actor 会被回收，
	// 五分钟后再点进战绩页，内存里早就没有这一局了。
	match, err := h.store.Match(ctx, uint(matchID))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.Error(c, http.StatusNotFound, "对局不存在")
			return
		}
		httputil.LogRequestError(c, "load pool match", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	// 不在这一局里的人不给看。
	//
	// 观战是开放的，回放却只给当事人：两者的入口不同——
	// 观战从「正在进行的对局」进，回放只从「我的战绩」进。
	// 前端没有第二个入口，接口这边就按当事人限定，免得对局 id 可枚举之后
	// 有人把别人的历史对局一条条翻出来。
	if _, ok := seatInMatch(match, userID); !ok {
		response.Error(c, http.StatusForbidden, "你不在这一局里")
		return
	}

	if match.Status != model.PoolStatusFinished || match.WinnerID == nil {
		response.Error(c, http.StatusConflict, "对局还没结束")
		return
	}

	shots, err := h.store.MatchShots(ctx, uint(matchID))
	if err != nil {
		httputil.LogRequestError(c, "load pool shots", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	// 能回放的对局一定结算过，座位 1 上一定有人；OpponentOf 的 0 分支
	// 只是让这里不必写 nil 判断。
	opponentID := match.OpponentOf(userID)
	briefs, err := h.users.Briefs(ctx, []uint{match.P0ID, opponentID})
	if err != nil {
		slog.WarnContext(ctx, "load replay briefs", "error", err)
		briefs = map[uint]user.Brief{}
	}

	winner := Seat0
	if *match.WinnerID == opponentID {
		winner = Seat1
	}

	resp := replayResp{
		MatchID: match.ID,
		// 两个座位按库里的顺序摆：座位号同时是「谁开的球」，
		// 按观众翻转会让同一条回放在两个人眼里座位号不一致。
		Players: [2]playerResp{
			toPlayerResp(briefOf(briefs, match.P0ID)),
			toPlayerResp(briefOf(briefs, opponentID)),
		},
		Winner: winner,
		Reason: match.WinReason,
		Shots:  make([]replayShotResp, 0, len(shots)),
	}
	for _, shot := range shots {
		resp.Shots = append(resp.Shots, replayShotResp{
			Seq:      shot.Seq,
			Seat:     shot.Seat,
			Duration: shot.Duration,
			Frames:   shot.Frames,
		})
	}
	response.OK(c, resp)
}

// Live godoc
// @Summary      正在进行的对局
// @Description  观战入口用。只列内存里正在打的对局，房间与已结束的不出现。
// @Tags         pool
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=[]pool.liveMatchResp}
// @Failure      401  {string}  string  "未登录"
// @Security     Bearer
// @Router       /pool/live [get]
// Live 处理 GET /api/pool/live。
func (h *Handler) Live(c *gin.Context) {
	matches := h.manager.List()
	list := make([]liveMatchResp, 0, len(matches))

	// 读的是每局自己发布的摘要，不进 actor 的命令队列。
	//
	// 之前是逐局问快照：一局正在算杆时 actor 被占着一两秒，那一局就取不到，
	// 于是「正在进行的对局」整块每十秒闪一下。摘要由 actor 在每条命令
	// 处理完之后发布，读它永远是 O(1)，慢也就慢在「比真实状态晚一杆」。
	for _, match := range matches {
		info, ok := match.Live()
		if !ok {
			// 已经关掉的对局：下一轮 List 里也就没有了。
			continue
		}
		if info.Status != model.PoolStatusPlaying {
			continue
		}
		entry := liveMatchResp{
			MatchID:       info.MatchID,
			Status:        info.Status,
			Turn:          info.Turn,
			ShotSeq:       info.ShotSeq,
			Spectators:    match.SpectatorCount(),
			SpectatorsMax: maxSpectators,
		}
		for seat := Seat0; seat <= Seat1; seat++ {
			entry.Players[seat] = toPlayerResp(info.Players[seat])
		}
		list = append(list, entry)
	}

	// 新开的局排前面：观战的人多半想看刚开始的，中途进去看不懂局面。
	sort.Slice(list, func(i, j int) bool { return list[i].MatchID > list[j].MatchID })
	response.OK(c, list)
}

// pageParams 读分页参数，非法值一律退回默认，不报错。
//
// 列表页的分页参数是用户手改或旧链接带过来的，为一个 page=abc 返回 400
// 不如直接给第一页——用户看到的东西才是他想要的。
func pageParams(c *gin.Context) (int, int) {
	page := 1
	size := 10
	if value, err := strconv.Atoi(c.DefaultQuery("page", "1")); err == nil && value > 0 {
		page = value
	}
	if value, err := strconv.Atoi(c.DefaultQuery("size", "10")); err == nil && value > 0 && value <= 50 {
		size = value
	}
	return page, size
}

// opponentOf 返回这条对局里「不是 userID」的那一方，不在局里返回 0。
func opponentOf(match model.PoolMatch, userID uint) uint {
	return match.OpponentOf(userID)
}

// seatInMatch 判断这个用户在库里那条对局中坐哪边。
func seatInMatch(match *model.PoolMatch, userID uint) (Seat, bool) {
	if match.P0ID == userID {
		return Seat0, true
	}
	if match.HasPlayer(userID) && match.P0ID != userID {
		return Seat1, true
	}
	return 0, false
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
