package ws

import (
	"log/slog"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"forum-backend/internal/realtime"
	"forum-backend/pkg/jwt"
	"forum-backend/pkg/response"
)

// onlineStatsResp 是在线状态接口 data 的形状。
// 用结构体而不是 gin.H，字段拼错由编译器拦住。
type onlineStatsResp struct {
	OnlineUsers int `json:"online_users"`
	OnlineConns int `json:"online_conns"`
}

// WSHandler 负责升级连接与在线状态查询。
// allowedOrigins 是启动时从配置算好的白名单，这里用 map 存是为了 O(1) 判断。
type WSHandler struct {
	hub            *realtime.Hub
	jwt            *jwt.Manager
	allowedOrigins map[string]bool
	upgrader       websocket.Upgrader
}

// NewWSHandler 由装配层调用，origins 为空表示谁都不放行（浏览器连接会被拒）。
func NewWSHandler(hub *realtime.Hub, jwtManager *jwt.Manager, origins []string) *WSHandler {
	h := &WSHandler{
		hub:            hub,
		jwt:            jwtManager,
		allowedOrigins: make(map[string]bool, len(origins)),
	}
	for _, origin := range origins {
		h.allowedOrigins[origin] = true
	}
	h.upgrader = websocket.Upgrader{
		// 这个连接只收心跳、发通知，1 KiB 读写缓冲够用。
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     h.checkOrigin,
	}
	return h
}

// 浏览器的同源策略管不住 WebSocket，握手是普通 HTTP 请求，
// 跨站页面可以带着 Cookie 发起，所以服务端必须自己校验 Origin。
func (h *WSHandler) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// curl、Go 客户端这类非浏览器调用不带 Origin。
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return h.allowedOrigins[u.Host]
}

// Notifications 把连接升级成 WebSocket，之后由 Hub 往这里推消息。
func (h *WSHandler) Notifications(c *gin.Context) {
	// 浏览器的 WebSocket 构造函数不接受自定义请求头，
	// 所以这个接口不挂 JWTAuth，token 从查询参数读。
	token := c.Query("token")
	if token == "" {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}
	userID, err := h.jwt.ParseAccess(token)
	if err != nil {
		// 必须在 Upgrade 之前把鉴权做完：
		// 升级之后响应头已经发出去了，再想返回 401 已经来不及。
		response.Error(c, http.StatusUnauthorized, "登录已过期，请重新登录")
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade 失败时它自己已经写过 HTTP 响应，这里只能记日志。
		slog.ErrorContext(c.Request.Context(), "upgrade websocket", "error", err)
		return
	}

	client := realtime.NewClient(h.hub, conn, userID)
	h.hub.Register(client)

	// 写泵必须单独起协程：gorilla/websocket 不支持并发写，
	// 由它独占处理发件箱和心跳，绝不能阻塞在这次请求里。
	go client.WriteLoop()

	// 读泵必须占住当前请求的协程（不要加 go），有两个原因：
	// 一是 Handler 一返回，net/http 就会回收底层连接；
	// 二是靠阻塞读来感知客户端断开，从而走到 ReadLoop 里 defer 的注销与清理。
	client.ReadLoop()
}

// Stats godoc
// @Summary      当前在线人数与连接数
// @Tags         realtime
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=ws.onlineStatsResp}
// @Router       /ws/stats [get]
// Stats 返回当前在线人数与连接数。
func (h *WSHandler) Stats(c *gin.Context) {
	response.OK(c, onlineStatsResp{
		OnlineUsers: h.hub.OnlineUsers(),
		OnlineConns: h.hub.OnlineConns(),
	})
}
