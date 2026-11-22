package pool

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"forum-backend/internal/httputil"
	"forum-backend/internal/model"
	"forum-backend/internal/realtime"
	"forum-backend/pkg/response"
)

// RematchTTL 是一条「想再打一局」的声明能挂多久。
//
// 与邀请同一个量级：一分钟内对方没点，这条就该失效。挂太久的话，
// 一方早就回大厅干别的去了，另一方还停在一个永远等不到回应的按钮上。
const RematchTTL = 60 * time.Second

// rematchStarted 是双方都点了之后新开出来的那一局。
//
// breaker 一并给出：结算界面要能说清「这局谁先开球」，前端不必自己推算
// 「上一局谁开的、这局该轮到谁」——那种推算每个页面都要重写一遍，还容易错。
type rematchStarted struct {
	MatchID  uint   `json:"match_id"`
	RoomCode string `json:"room_code"`
	Breaker  Seat   `json:"breaker"`
}

// rematchWantResp 是「对方点了再约一局」那条推送。
//
// 带昵称与头像：收到推送的那一屏要立刻把按钮文案换成「{昵称} 想再打一局」，
// 只给一个 id 的话还得再请求一次用户资料，中间那一瞬会显示成「对方」。
type rematchWantResp struct {
	MatchID uint       `json:"match_id"`
	From    playerResp `json:"from"`
}

// rematchResp 是「再约一局」的响应。
//
// started 与 waiting 互斥：前者表示两个人都点过了、新的一局已经开起来，
// 后者表示只记下了我这一票。前端按这两个字段决定「跳过去」还是「显示等待」。
type rematchResp struct {
	Started *rematchStarted `json:"started,omitempty"`
	Waiting bool            `json:"waiting"`
}

// rematchEntry 是同一局上两个人各自的「再打一局」声明。
type rematchEntry struct {
	matchID   uint
	want      [2]bool
	players   [2]PlayerBrief
	updatedAt time.Time
	// started 是双方都点了之后建出来的那一局。两个请求都要从它拿同一份结果：
	// 后到的那个请求不能再建一局，否则两个人会被分到两张球台上。
	started *rematchStarted
}

// Rematches 是「再约一局」的登记表，进程内唯一，按上一局的 match_id 分组。
//
// 为什么不落库：这份声明只活一分钟，且只在两个还停在结算界面的人之间传递。
// 落库要额外做清理，还要处理「进程重启后旧声明突然生效」这种更难解释的情况。
type Rematches struct {
	mu      sync.Mutex
	byMatch map[uint]*rematchEntry
	ttl     time.Duration
	now     func() time.Time
}

// NewRematches 由 Handler 在装配期创建。
func NewRematches() *Rematches {
	return &Rematches{
		byMatch: make(map[uint]*rematchEntry),
		ttl:     RematchTTL,
		now:     time.Now,
	}
}

// Declare 记下某个座位「想再打一局」，返回新开的那一局（若已经开起来）与
// 「双方是否都点了」。
//
// 返回 started 而不是让调用方再查一次：两个人几乎同时点的时候，
// 后到的请求必须拿到先到者建出来的那一局，而不是看到「都点了」再建第二局。
func (r *Rematches) Declare(matchID uint, seat Seat, players [2]PlayerBrief) (*rematchStarted, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.byMatch[matchID]
	if !ok {
		entry = &rematchEntry{matchID: matchID}
		r.byMatch[matchID] = entry
	}
	entry.players = players
	entry.want[seat] = true
	entry.updatedAt = r.now()

	if entry.started != nil {
		return entry.started, true
	}
	return nil, entry.want[Seat0] && entry.want[Seat1]
}

// MarkStarted 记下新开出来的那一局，供另一侧的请求复用。
func (r *Rematches) MarkStarted(matchID uint, started *rematchStarted) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.byMatch[matchID]
	if !ok {
		entry = &rematchEntry{matchID: matchID}
		r.byMatch[matchID] = entry
	}
	entry.started = started
	entry.updatedAt = r.now()
}

// Clear 撤掉一局上的全部声明。
//
// 建新局失败时也要撤：留着的话两个人各自都点了，界面显示「等对方」，
// 而对方的票已经在手里了——他们再也点不动这个按钮，只能重开页面。
func (r *Rematches) Clear(matchID uint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byMatch, matchID)
}

// Sweep 清掉过期的声明，返回清掉的条数。
func (r *Rematches) Sweep() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	removed := 0
	for id, entry := range r.byMatch {
		if r.now().Sub(entry.updatedAt) > r.ttl {
			delete(r.byMatch, id)
			removed++
		}
	}
	return removed
}

// Rematch godoc
// @Summary      再约一局
// @Description  两个人都点过就直接开一局新的，开球方与上一局轮换；只点了一个人时只是记下来。
// @Tags         pool
// @Produce      json
// @Param        id   path  int  true  "上一局的 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=pool.rematchResp}
// @Failure      401  {string}  string  "未登录"
// @Failure      403  {string}  string  "你不在这一局里"
// @Failure      404  {string}  string  "对局不存在"
// @Failure      409  {string}  string  "还没打完 / 对手不在了"
// @Security     Bearer
// @Router       /pool/matches/{id}/rematch [post]
// Rematch 处理 POST /api/pool/matches/:id/rematch。
//
// 这条接口替代了原来「点再约 → 回大厅 → 发一条新邀请 → 等对方接受 → 双方准备」
// 那一整串：双方都已经表态要再打一局，再让人回房间点一次准备纯属多走三步。
func (h *Handler) Rematch(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	raw, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || raw == 0 {
		response.Error(c, http.StatusBadRequest, "对局 id 不合法")
		return
	}

	row, err := h.store.Match(ctx, uint(raw))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.Error(c, http.StatusNotFound, "这一局已经不在了")
			return
		}
		httputil.LogRequestError(c, "load match for rematch", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}
	if !row.HasPlayer(userID) {
		response.Error(c, http.StatusForbidden, "你不在这一局里")
		return
	}
	if row.Status != model.PoolStatusFinished {
		response.Error(c, http.StatusConflict, "这一局还没打完")
		return
	}
	if row.P1ID == nil {
		response.Error(c, http.StatusConflict, "对手不在了，回大厅重新约一个")
		return
	}

	// 两个人资料一次取齐：新一局的 actor 要用它们，推送里也要带昵称。
	ids := []uint{row.P0ID, *row.P1ID}
	briefs, err := h.users.Briefs(ctx, ids)
	if err != nil {
		httputil.LogRequestError(c, "load rematch players", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}
	players := [2]PlayerBrief{briefOf(briefs, row.P0ID), briefOf(briefs, *row.P1ID)}

	// 「查有没有都点了 → 建新局」必须串行：两个人同时点的时候，
	// 两个请求都会看到 want 都是 true，不串行就会各建一局。
	h.rematchMu.Lock()
	defer h.rematchMu.Unlock()

	seat := seatOfRow(row, userID)
	started, both := h.rematches.Declare(row.ID, seat, players)
	if started != nil {
		// 对方那个请求已经把新局建好了：回同一份结果，不再建第二局。
		response.OK(c, rematchResp{Started: started})
		return
	}

	// 先告诉对方「他点了」。对方可能正停在结算界面，这张卡就是他那边的确认键。
	h.hub.Push(players[seat.Other()].UserID, realtime.PushMessage{
		Type: "pool_rematch",
		Data: rematchWantResp{MatchID: row.ID, From: toPlayerResp(players[seat])},
	})

	if !both {
		response.OK(c, rematchResp{Waiting: true})
		return
	}

	started, err = h.startRematch(ctx, row, players)
	if err != nil {
		h.rematches.Clear(row.ID)
		httputil.LogRequestError(c, "start rematch", err)
		response.Error(c, http.StatusInternalServerError, "再开一局失败，请重试")
		return
	}
	h.rematches.MarkStarted(row.ID, started)

	// 两边都要跳过去：后点的那个人能从 HTTP 响应里拿到，先点的那个只能靠推送。
	for player := Seat0; player <= Seat1; player++ {
		h.hub.Push(players[player].UserID, realtime.PushMessage{
			Type: "pool_rematch_started",
			Data: started,
		})
	}
	response.OK(c, rematchResp{Started: started})
}

// startRematch 用同一对玩家直接开一局新的，跳过房间与准备。
//
// 库里另起一行而不是把上一行改回进行中：战绩、回放、Elo 都按行记，
// 复用同一行会让「这一局打了两次」在数据上分不开。
func (h *Handler) startRematch(ctx context.Context, prev *model.PoolMatch, players [2]PlayerBrief) (*rematchStarted, error) {
	// 上一局谁开的球，这一局就换另一个人：连续约战时开球优势不能总落在同一侧。
	breaker := h.breakerOf(prev.ID).Other()

	created, err := h.createMatchRow(ctx, prev.P0ID, *prev.P1ID)
	if err != nil {
		return nil, err
	}

	// 库里的状态推进失败不阻塞开局：内存里已经在打了，大厅按 CurrentMatchFor
	// 仍然查得到这一行（waiting 也在它的查询范围内）。多一条日志，少一次开局失败。
	if err := h.store.MarkPlaying(ctx, created.ID); err != nil {
		slog.ErrorContext(ctx, "mark rematch playing", "match_id", created.ID, "error", err)
	}

	state := newMatchState(created.ID, created.RoomCode, players)
	state.Breaker = breaker
	state.Turn = breaker
	// 开球这一杆给开球方手中球，但只能摆在开球线之后，与房间开局完全一致。
	state.BallInHand = true
	state.PlaceZone = PlaceBehindHead
	state.CuePlaced = false
	h.manager.Create(state)

	return &rematchStarted{MatchID: created.ID, RoomCode: created.RoomCode, Breaker: breaker}, nil
}

// createMatchRow 建一条双方都已就位的对局行，房号撞车时换一个重试。
//
// 撞号判据交给唯一索引：查与插之间有窗口，先查再插挡不住并发。
func (h *Handler) createMatchRow(ctx context.Context, p0, p1 uint) (*model.PoolMatch, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		code, err := NewRoomCode()
		if err != nil {
			return nil, err
		}
		created, err := h.store.CreateMatch(ctx, p0, &p1, code)
		if err == nil {
			return created, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// breakerOf 报告某一局是谁开的球。
//
// actor 已经被清扫掉时退回座位 0：那种情况下「轮流开球」只能从这一轮重新开始，
// 总比让「再约」直接失败强——玩家在意的是能不能马上再打一局。
func (h *Handler) breakerOf(matchID uint) Seat {
	if match, ok := h.manager.Get(matchID); ok {
		if info, ok := match.Live(); ok {
			return info.Breaker
		}
	}
	return Seat0
}
