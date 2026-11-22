package pool

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"forum-backend/internal/httputil"
	"forum-backend/internal/model"
	"forum-backend/internal/realtime"
	"forum-backend/pkg/response"
)

// 房间相关的三个入口：我的房间、按房号进房、以及「现在谁在线」。
//
// 这三种动作是同一个界面的三个方向：
//   - 我点开台球 → 我有自己的房间，房号摆在屏幕上；
//   - 我把房号告诉别人 → 他输进来，坐到空着的那个座位上；
//   - 我懒得报房号 → 从在线名单里点一个人邀请他，走推送把他叫过来。
//
// 房间就是 pool_match 里 status=waiting 的那一行，座位 1 可以空着（p1_id 为 NULL）。

// roomCodePattern 是房号的形状：六位数字。与 NewRoomCode 生成的一致。
var roomCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

// 房间相关的错误说明。
//
// 这些文案要直接给用户看，所以写在这里而不是散在各个 handler 里：
// 「房号输错了」与「房间已经开始了」对用户来说是完全不同的两件事。
const (
	msgRoomNotFound = "房号不对，或者这个房间已经开始了"
	msgRoomTaken    = "这个房间已经有人了"
)

// MyRoom godoc
// @Summary      我的房间
// @Description  没有就开一个，有就返回它。房间页每 1.5 秒轮询一次，status 变成 playing 就跳对局页。
// @Tags         pool
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=pool.roomResp}
// @Failure      401  {string}  string  "未登录"
// @Security     Bearer
// @Router       /pool/rooms/my [post]
// MyRoom 处理 POST /api/pool/rooms/my。
//
// 用 POST 而不是 GET：它可能建一条新房间（写操作），
// 而 GET 在半路被浏览器/代理重试时留下两行房间是个很难查的 bug。
func (h *Handler) MyRoom(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	match, seat, err := h.myRoom(ctx, userID)
	if err != nil {
		httputil.LogRequestError(c, "open my room", err)
		response.Error(c, http.StatusInternalServerError, "开房失败，请重试")
		return
	}

	snapshot, err := match.Snapshot(seat)
	if err != nil {
		response.Error(c, http.StatusNotFound, "房间已关闭，刷新一下再试")
		return
	}
	response.OK(c, toRoomResp(snapshot))
}

// myRoom 拿到我当前所在的那一局，没有就开一间新房。
//
// 「我当前的」优先取库里已有的：页面每次刷新都调它，
// 每次都新建的话房号会一直变，刚报给别人的那个立刻作废。
//
// 正在打的对局也算「我的房间」：大厅每两秒问一次，若这里只认 waiting，
// 开局那一刻就会另开一间新房，房主永远进不了自己的对局。
func (h *Handler) myRoom(ctx context.Context, userID uint) (*Match, Seat, error) {
	row, err := h.store.CurrentMatchFor(ctx, userID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, err
	}
	if err == nil {
		if row.Status != model.PoolStatusPlaying {
			return h.attachRoom(ctx, row, userID)
		}
		// 已经在打：内存里那一局直接给他，快照里的 status 是 playing，
		// 前端看到就会跳回球台。
		if match, ok := h.manager.Get(row.ID); ok {
			return match, seatOfRow(row, userID), nil
		}
		// 内存里没有这一局（进程重启过）：球位已经找不回来，给他一间新房，
		// 总比让他卡在一句「对局不存在」上强。
	}
	return h.openRoom(ctx, userID)
}

// seatOfRow 按库里的那条记录算出某人坐哪边。房主恒为座位 0。
func seatOfRow(row *model.PoolMatch, userID uint) Seat {
	if row.P1ID != nil && *row.P1ID == userID {
		return Seat1
	}
	return Seat0
}

// attachRoom 保证库里这条房间在内存里有对应的 actor，并算出我坐哪。
//
// 进程重启过（或者这局刚被清扫掉）时库里还有行、内存里却没有 actor：
// 这时按库里记的两个人把房间重建出来，否则刷新一下页面就成「房间不存在」。
func (h *Handler) attachRoom(ctx context.Context, row *model.PoolMatch, userID uint) (*Match, Seat, error) {
	seat := seatOfRow(row, userID)

	if match, ok := h.manager.Get(row.ID); ok {
		return match, seat, nil
	}

	ids := make([]uint, 0, 2)
	ids = append(ids, row.P0ID)
	if row.P1ID != nil {
		ids = append(ids, *row.P1ID)
	}
	briefs, err := h.users.Briefs(ctx, ids)
	if err != nil {
		return nil, 0, err
	}

	players := [2]PlayerBrief{briefOf(briefs, row.P0ID)}
	if row.P1ID != nil {
		players[1] = briefOf(briefs, *row.P1ID)
	}
	return h.manager.Create(newRoomState(row.ID, row.RoomCode, players)), seat, nil
}

// openRoom 开一个只有房主的新房间：座位 1 空着，等别人输房号或接受邀请进来。
func (h *Handler) openRoom(ctx context.Context, userID uint) (*Match, Seat, error) {
	briefs, err := h.users.Briefs(ctx, []uint{userID})
	if err != nil {
		return nil, 0, err
	}
	players := [2]PlayerBrief{briefOf(briefs, userID)}

	var lastErr error
	// 房号撞唯一索引就换一个重试。查与插之间有窗口，唯一索引才是可靠判据。
	for attempt := 0; attempt < 5; attempt++ {
		code, err := NewRoomCode()
		if err != nil {
			return nil, 0, err
		}
		created, err := h.store.CreateMatch(ctx, userID, nil, code)
		if err != nil {
			lastErr = err
			continue
		}
		return h.manager.Create(newRoomState(created.ID, created.RoomCode, players)), Seat0, nil
	}
	return nil, 0, lastErr
}

// JoinRoom godoc
// @Summary      按房号进房
// @Description  把房号输进来，坐到空着的那个座位上。
// @Tags         pool
// @Accept       json
// @Produce      json
// @Param        body  body  pool.joinRoomReq  true  "房号"
// @Success      200   {object}  httputil.SwaggerEnvelope{data=pool.roomResp}
// @Failure      400   {string}  string  "房号格式不对"
// @Failure      401   {string}  string  "未登录"
// @Failure      404   {string}  string  "房号不对或房间已开始"
// @Failure      409   {string}  string  "房间已经有人了"
// @Security     Bearer
// @Router       /pool/rooms/join [post]
// JoinRoom 处理 POST /api/pool/rooms/join。
func (h *Handler) JoinRoom(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	var req joinRoomReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "请填房号")
		return
	}
	code := strings.TrimSpace(req.RoomCode)
	if !roomCodePattern.MatchString(code) {
		response.Error(c, http.StatusBadRequest, "房号是六位数字")
		return
	}

	row, err := h.store.MatchByRoomCode(ctx, code)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.Error(c, http.StatusNotFound, msgRoomNotFound)
			return
		}
		httputil.LogRequestError(c, "find room by code", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	// 输的是自己的房号：不报错，直接把他带回自己的房间。
	// 报错只会让人以为「我的房间坏了」。
	if row.HasPlayer(userID) {
		match, seat, err := h.attachRoom(ctx, row, userID)
		if err != nil {
			httputil.LogRequestError(c, "attach room", err)
			response.Error(c, http.StatusInternalServerError, "进房失败，请重试")
			return
		}
		h.writeRoom(c, match, seat)
		return
	}

	// 判据交给带条件的 UPDATE：两个人几乎同时输同一个房号时，
	// 「先查空位再写」会让两个人都以为自己抢到了。
	joined, err := h.store.JoinRoom(ctx, row.ID, userID)
	if err != nil {
		httputil.LogRequestError(c, "join room", err)
		response.Error(c, http.StatusInternalServerError, "进房失败，请重试")
		return
	}
	if !joined {
		response.Error(c, http.StatusConflict, msgRoomTaken)
		return
	}

	briefs, err := h.users.Briefs(ctx, []uint{userID})
	if err != nil {
		httputil.LogRequestError(c, "load player briefs", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}
	brief := briefOf(briefs, userID)

	// 内存里已经有 actor 就把座位补上；没有（房间是在别的请求里建好、但被清扫过）
	// 就按库里现在的两个人重建一份。
	row.P1ID = &userID
	var match *Match
	if existing, ok := h.manager.Get(row.ID); ok {
		match = existing
		if err := match.FillSeat(Seat1, brief); err != nil {
			httputil.LogRequestError(c, "fill room seat", err)
			response.Error(c, http.StatusNotFound, msgRoomNotFound)
			return
		}
	} else {
		match, _, err = h.attachRoom(ctx, row, userID)
		if err != nil {
			httputil.LogRequestError(c, "attach room", err)
			response.Error(c, http.StatusInternalServerError, "进房失败，请重试")
			return
		}
	}

	// 房主那边不用刷新就能看到「人来了」。推送失败不要紧，房间页 1.5 秒轮询一次。
	h.hub.Push(row.P0ID, realtime.PushMessage{
		Type: "pool_room_joined",
		Data: roomJoinedResp{MatchID: row.ID, RoomCode: row.RoomCode, Player: toPlayerResp(brief)},
	})

	// 我自己原来那间房没用了：一个人同时只能在一间房里等。
	h.retireOwnRooms(ctx, userID, row.ID)

	h.writeRoom(c, match, Seat1)
}

// retireOwnRooms 把「我自己开的那几间房」标成过期，并摘掉内存里的 actor。
//
// 一个人同时只能在一间房里等：进了别人的房间之后，自己原来那间还留着的话，
// 「我的房间」会在两行之间来回跳（按 id 取最新的一条），
// 表现为「刚报给别人的房号突然变成了一个空房间的房号」。
func (h *Handler) retireOwnRooms(ctx context.Context, userID, keepID uint) {
	ids, err := h.store.ExpireOwnRooms(ctx, userID, keepID)
	if err != nil {
		// 收不掉只是多留一间空房，等清扫兜底；不影响他已经在别人的房间里。
		slog.WarnContext(ctx, "expire own rooms", "user_id", userID, "error", err)
		return
	}
	for _, id := range ids {
		h.manager.Remove(id)
	}
}

// LeaveRoom godoc
// @Summary      退出别人的房间
// @Description  只有坐在别人房间里的那一方能退；自己的房间不适用（关掉页面等它过期即可）。
// @Tags         pool
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=map[string]bool}
// @Failure      400  {string}  string  "这是你自己的房间"
// @Failure      401  {string}  string  "未登录"
// @Failure      404  {string}  string  "你不在任何房间里"
// @Security     Bearer
// @Router       /pool/rooms/leave [post]
// LeaveRoom 处理 POST /api/pool/rooms/leave。
//
// 有这个入口是因为邀请失败时会说「你已经在别人的房间里了，先退出来」：
// 说了却没法退，那句话就成了死胡同。
func (h *Handler) LeaveRoom(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	row, err := h.store.GuestRoomFor(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.Error(c, http.StatusNotFound, "你没有在别人的房间里")
			return
		}
		httputil.LogRequestError(c, "find guest room", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	left, err := h.store.LeaveRoom(ctx, row.ID, userID)
	if err != nil {
		httputil.LogRequestError(c, "leave room", err)
		response.Error(c, http.StatusInternalServerError, "退出失败，请重试")
		return
	}
	if !left {
		// 座位已经不是他的了（房主换了人，或者房间刚开打）。
		response.Error(c, http.StatusNotFound, "你不在这个房间里了")
		return
	}

	// 内存里把那个座位清空：房主那一屏两秒内就会看到空位。
	// 这里不发推送——房间页本来就在轮询，为一次退房多一条协议不值得。
	if match, ok := h.manager.Get(row.ID); ok {
		if err := match.FillSeat(Seat1, PlayerBrief{}); err != nil {
			slog.WarnContext(ctx, "clear room seat", "match_id", row.ID, "error", err)
		}
	}

	response.OK(c, map[string]bool{"left": true})
}

// writeRoom 取一份房间快照写进响应。取不到（房间刚被关掉）时回 404。
func (h *Handler) writeRoom(c *gin.Context, match *Match, seat Seat) {
	snapshot, err := match.Snapshot(seat)
	if err != nil {
		response.Error(c, http.StatusNotFound, "房间已关闭，刷新一下再试")
		return
	}
	response.OK(c, toRoomResp(snapshot))
}

// fillJoinedSeat 把刚进房的人补到座位 1 上。
//
// 邀请接受与房号进房两条路都会走到这里：库里那行已经写好了 p1_id，
// 内存里那份房间状态还得知道「这个人叫什么、头像是什么」，
// 否则房主看到的是一个空名字的对手，广播出去的快照也是空的。
func (h *Handler) fillJoinedSeat(ctx context.Context, matchID, userID uint) error {
	briefs, err := h.users.Briefs(ctx, []uint{userID})
	if err != nil {
		return err
	}
	brief := briefOf(briefs, userID)

	match, ok := h.manager.Get(matchID)
	if !ok {
		// 内存里没有这局（进程重启过，或房主那页早就关了又回来）：
		// 按库里的两个人重建一份，进房的那个人看到的就是完整房间。
		row, err := h.store.Match(ctx, matchID)
		if err != nil {
			return err
		}
		_, _, err = h.attachRoom(ctx, row, userID)
		return err
	}
	return match.FillSeat(Seat1, brief)
}

// Online godoc
// @Summary      现在在线的人
// @Description  邀请面板用。排除自己与正在对局里的人，关注的排在前面。
// @Tags         pool
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=[]pool.onlineUserResp}
// @Failure      401  {string}  string  "未登录"
// @Security     Bearer
// @Router       /pool/online [get]
// Online 处理 GET /api/pool/online。
func (h *Handler) Online(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 正在对局里的人不列出来：邀请他只会得到一个「我在打」。
	busy := make(map[uint]struct{})
	for _, match := range h.manager.List() {
		info, ok := match.Live()
		if !ok {
			continue
		}
		for seat := Seat0; seat <= Seat1; seat++ {
			if info.Players[seat].UserID != 0 {
				busy[info.Players[seat].UserID] = struct{}{}
			}
		}
	}

	candidates := make([]uint, 0, len(h.hub.OnlineUserIDs()))
	for _, id := range h.hub.OnlineUserIDs() {
		if id == userID {
			continue
		}
		if _, playing := busy[id]; playing {
			continue
		}
		candidates = append(candidates, id)
	}

	// 上限按一屏能看的人数给：大厅是手机竖屏，再多也只是让人划半天。
	const limit = 30
	const maxCandidates = 200
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	briefs, err := h.users.Briefs(ctx, candidates)
	if err != nil {
		httputil.LogRequestError(c, "load online briefs", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	following, err := h.store.FollowingSet(ctx, userID, candidates)
	if err != nil {
		// 关注关系只是用来排序与打标的，查不到就当都没关注，不影响「能不能邀请」。
		following = map[uint]bool{}
	}

	items := make([]onlineUserResp, 0, len(candidates))
	for _, id := range candidates {
		brief := briefOf(briefs, id)
		items = append(items, onlineUserResp{
			UserID:    id,
			Nickname:  brief.Nickname,
			Avatar:    brief.Avatar,
			Following: following[id],
		})
	}

	// 关注的排前面，同组里按昵称排：每次刷新顺序都一样，
	// 否则点邀请时列表在换行，很容易点错人。
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Following != items[j].Following {
			return items[i].Following
		}
		return items[i].Nickname < items[j].Nickname
	})
	if len(items) > limit {
		items = items[:limit]
	}
	response.OK(c, items)
}
