package pool

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"forum-backend/internal/pool/sim"
	"forum-backend/internal/ws"
	"forum-backend/pkg/jwt"
	"forum-backend/pkg/response"
)

// WSHandler 是 /api/ws/game 的入口：升级连接、认人、认座位，然后把上行消息转给 actor。
//
// 它不持有任何对局状态，也不做规则判断：那些都在 Match 的 actor 协程里。
type WSHandler struct {
	manager  *Manager
	jwt      *jwt.Manager
	upgrader websocket.Upgrader
}

// NewWSHandler 由装配层调用。origins 为空表示浏览器连接一律被拒。
func NewWSHandler(manager *Manager, jwtManager *jwt.Manager, origins []string) *WSHandler {
	return &WSHandler{
		manager: manager,
		jwt:     jwtManager,
		upgrader: websocket.Upgrader{
			// 一杆的响应有 60KB，读缓冲按上行消息大小给，
			// 写缓冲给到 4 KiB，路径够用又不至于每连接多占几十 KB。
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     ws.OriginChecker(origins),
		},
	}
}

// Serve 处理一次对局连接。
//
// 鉴权必须在 Upgrade 之前做完：升级之后响应头已经发出去了，
// 再想返回 401 已经来不及，浏览器只会看到一次莫名其妙的连接失败。
func (h *WSHandler) Serve(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}
	userID, err := h.jwt.ParseAccess(token)
	if err != nil {
		response.Error(c, http.StatusUnauthorized, "登录已过期，请重新登录")
		return
	}

	matchID, err := strconv.ParseUint(c.Query("match_id"), 10, 64)
	if err != nil || matchID == 0 {
		response.Error(c, http.StatusBadRequest, "缺少对局编号")
		return
	}

	match, ok := h.manager.Get(uint(matchID))
	if !ok {
		// 对局不在内存里：可能已经结束并被回收。前端据此回大厅而不是反复重连。
		response.Error(c, http.StatusNotFound, "对局已结束")
		return
	}

	seat, isPlayer := seatOf(match, userID)
	if !isPlayer {
		// 观战名额满了要在握手前就拒：WebSocket 里拿不到 HTTP 状态码，
		// 升级之后再发一条 error 帧再关闭，浏览器只会看到一次莫名其妙的断连。
		// 这里读到的是略旧的计数，真正的上限由 handleWatch 在 actor 里再判一次。
		if match.SpectatorCount() >= maxSpectators {
			response.Error(c, http.StatusConflict, "这场对局观战人数已满，稍后再来")
			return
		}
		seat = SeatSpectator
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade 失败时它自己已经写过响应，这里只能记日志。
		slog.ErrorContext(c.Request.Context(), "upgrade game websocket", "error", err)
		return
	}

	wsConn := newWSConn(conn)
	go wsConn.writeLoop()

	if isPlayer {
		if err := match.Join(seat, wsConn); err != nil {
			wsConn.Close()
			return
		}
	} else if err := match.Watch(wsConn); err != nil {
		// 预检通过、actor 里却又满了：并发抢位的结果，直接关掉。
		// 前端会看到一次「连接断开」，重连一次通常就有位置了。
		wsConn.Close()
		return
	}

	// 读循环占住当前协程，直到连接断开。
	wsConn.readLoop(func(msg ClientMessage) { h.dispatch(match, seat, wsConn, msg) })

	// 走到这里说明连接断了。玩家不调 Leave：网络抖动不该直接判负，
	// 对局继续按读秒走，重连上来会拿到一份完整快照。
	// 观战者只是从观战名单里摘掉（handleDisconnect 里按座位区分）。
	_ = match.NotifyDisconnect(seat, wsConn)
}

// dispatch 把一条上行消息翻译成 actor 命令。
//
// 所有校验都在 actor 里做（轮次、参数区间、是否手中球），这里只解包：
// 两边都判一遍的话，规则一变就要改两处。
func (h *WSHandler) dispatch(match *Match, seat Seat, conn *wsConn, msg ClientMessage) {
	// 观战者只能看。所有上行命令一律回同一句，
	// 而不是放它进规则层被拒成「还没轮到你出杆」——观战者本来就没有回合。
	if seat == SeatSpectator {
		conn.Send(errorMessage(msg.Seq, ErrCodeNotYourTurn, "观战中不能操作"))
		return
	}

	switch msg.Type {
	case CmdShoot:
		var payload struct {
			Phi   float64 `json:"phi"`
			Power float64 `json:"power"`
			Theta float64 `json:"theta"`
			A     float64 `json:"a"`
			B     float64 `json:"b"`
		}
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			conn.Send(errorMessage(msg.Seq, ErrCodeBadParams, "杆法参数解析失败"))
			return
		}

		err := match.Shoot(seat, sim.CueParams{
			Phi:   payload.Phi,
			Power: payload.Power,
			Theta: payload.Theta,
			A:     payload.A,
			B:     payload.B,
		}, msg.Seq)
		if err != nil {
			conn.Send(errorMessage(msg.Seq, codeOf(err), messageOf(err)))
		}

	case CmdPlaceCue:
		var payload struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			conn.Send(errorMessage(msg.Seq, ErrCodeBadParams, "落点解析失败"))
			return
		}
		if err := match.PlaceCue(seat, payload.X, payload.Y); err != nil {
			conn.Send(errorMessage(msg.Seq, codeOf(err), messageOf(err)))
		}

	case CmdResume:
		// 重连就是重新进房：快照会重发一遍，客户端按快照对齐即可。
		// 这里不做任何「补发遗漏消息」的处理，快照本身就是收敛点。
		if err := match.Join(seat, conn); err != nil {
			conn.Send(errorMessage(msg.Seq, codeOf(err), messageOf(err)))
		}

	case CmdLeave:
		if err := match.Leave(seat); err != nil {
			conn.Send(errorMessage(msg.Seq, codeOf(err), messageOf(err)))
		}

	case CmdJoin:
		// 进房在握手时就做完了，客户端再发一次当重连处理。
		if err := match.Join(seat, conn); err != nil {
			conn.Send(errorMessage(msg.Seq, codeOf(err), messageOf(err)))
		}

	default:
		conn.Send(errorMessage(msg.Seq, ErrCodeBadParams, "不认识的消息类型"))
	}
}

// seatOf 判断这个用户坐在哪一边。
func seatOf(match *Match, userID uint) (Seat, bool) {
	snapshot, err := match.Snapshot(-1)
	if err != nil {
		return 0, false
	}
	for seat := Seat0; seat <= Seat1; seat++ {
		if snapshot.Players[seat].UserID == userID {
			return seat, true
		}
	}
	return 0, false
}

// codeOf 把错误映射成给前端的错误码。
func codeOf(err error) string {
	switch {
	case errors.Is(err, ErrNotYourTurn):
		return ErrCodeNotYourTurn
	case errors.Is(err, ErrNotPlaying):
		return ErrCodeNotPlaying
	case errors.Is(err, ErrBadParams):
		return ErrCodeBadParams
	case errors.Is(err, ErrMustPlaceCue), errors.Is(err, ErrNotBallInHand):
		return ErrCodeNotInHand
	case errors.Is(err, ErrSimFailed):
		return ErrCodeSimFailed
	default:
		return ErrCodeInternal
	}
}

// messageOf 给出能直接显示给玩家的说明。
func messageOf(err error) string {
	switch {
	case errors.Is(err, ErrNotYourTurn):
		return "还没轮到你出杆"
	case errors.Is(err, ErrNotPlaying):
		return "对局已经结束"
	case errors.Is(err, ErrBadParams):
		return "杆法参数超出范围"
	case errors.Is(err, ErrMustPlaceCue):
		return "请先把母球放到台面上"
	case errors.Is(err, ErrNotBallInHand):
		return "这一杆不能自由摆放母球"
	case errors.Is(err, ErrSimFailed):
		return "球桌暂时算不动，请稍后重试"
	default:
		return "操作失败，请重试"
	}
}

// errorMessage 造一条错误回执，seq 与请求配对，前端据此把提示挂到对应操作上。
func errorMessage(seq int, code, text string) Message {
	return Message{
		Type: MsgError,
		Seq:  seq,
		TS:   time.Now().UnixMilli(),
		Data: ErrorData{Code: code, Message: text},
	}
}
