package handler

import (
	"log"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"forum-backend/internal/realtime"
	"forum-backend/pkg/jwt"
	"forum-backend/pkg/response"
)

var allowedOrigins = map[string]bool{
	"app.forum.com":   true, // 正式环境前端域名
	"127.0.0.1:8080":  true, // 本地测试
}

// upgrader 把普通 HTTP 请求升级成 WebSocket。
// 只分配 1 KiB 读写缓冲区：这个连接只收心跳、发通知，不需要大块内存。
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// 浏览器的同源策略管不住 WebSocket，握手是普通 HTTP 请求，
	// 跨站页面可以带着 Cookie 发起。所以服务端必须自己校验 Origin。
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// curl、Go 客户端这类非浏览器调用不带 Origin，放行。
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return allowedOrigins[u.Host]
	},
}

// WSHandler 负责升级连接与在线状态查询。
type WSHandler struct {
	hub *realtime.Hub
	jwt *jwt.Manager
}

func NewWSHandler(hub *realtime.Hub, jwtManager *jwt.Manager) *WSHandler {
	return &WSHandler{hub: hub, jwt: jwtManager}
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
	userID, err := h.jwt.Parse(token)
	if err != nil {
		// 必须在 Upgrade 之前把鉴权做完：
		// 升级之后响应头已经发出去了，再想返回 401 已经来不及。
		response.Error(c, http.StatusUnauthorized, "登录已过期，请重新登录")
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade 失败时它自己已经写过 HTTP 响应，这里只能记日志。
		log.Printf("upgrade websocket: %v", err)
		return
	}

	client := realtime.NewClient(h.hub, conn, userID)
	h.hub.Register(client)

	// 写泵单独一个 goroutine，读泵占住本次请求的 goroutine，
	// 它返回就代表连接结束，defer 里会把连接摘掉。
	go client.WriteLoop()
	// 写泵：必须单独起协程。因为 gorilla/websocket 不支持并发写，
	// 必须由它独占处理发件箱 (send channel) 和心跳 (Ping)，严禁阻塞。
	client.ReadLoop()
	// 读泵：必须占住当前请求的协程（不要加 go）。
	// 一是防止 Handler 返回导致底层 TCP 被 net/http 回收；
	// 二是靠阻塞读来感知客户端断开，触发 defer 里的注销与资源清理。
}


// Stats godoc
// @Summary      当前在线人数与连接数
// @Tags         realtime
// @Produce      json
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.onlineStatsResp}
// @Router       /ws/stats [get]
// Stats 返回当前在线人数与连接数。
func (h *WSHandler) Stats(c *gin.Context) {
	response.OK(c, onlineStatsResp{
		OnlineUsers: h.hub.OnlineUsers(),
		OnlineConns: h.hub.OnlineConns(),
	})
}
