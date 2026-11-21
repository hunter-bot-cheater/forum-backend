package pool

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 对局连接的四个时长与通知连接（internal/realtime）取值一致，理由也一致：
// 写超时防止网络阻塞挂住写协程，pong 超时判断掉线，ping 周期要明显小于 pong 超时。
const (
	gameWriteWait  = 10 * time.Second
	gamePongWait   = 60 * time.Second
	gamePingPeriod = (gamePongWait * 9) / 10
	// 上行只有杆法参数与坐标，4 KiB 足够；上限存在的意义是挡住异常大包。
	gameMaxMessage = 4 << 10
	// 发件箱容量。一杆会连发 shot（60KB）、foul、turn 三条，
	// 对手的客户端偶尔读慢一点，32 条足够撑过一次出杆。
	gameSendBuffer = 32
)

// wsConn 是对局用的一条 WebSocket 连接。
//
// 读写各占一个协程，send 是两者之间唯一的通道：
// gorilla/websocket 不允许并发写，所有出站数据都必须经过 writeLoop。
type wsConn struct {
	conn   *websocket.Conn
	send   chan []byte
	closed chan struct{}
	once   sync.Once
}

// newWSConn 在升级成功后调用。
func newWSConn(conn *websocket.Conn) *wsConn {
	return &wsConn{
		conn:   conn,
		send:   make(chan []byte, gameSendBuffer),
		closed: make(chan struct{}),
	}
}

// Send 把消息序列化后放进发件箱。返回 false 表示队列已满（对方读不动）。
func (c *wsConn) Send(msg Message) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		// 序列化失败是服务端的代码问题，不该因此断掉玩家连接。
		slog.Error("marshal game message", "type", msg.Type, "error", err)
		return true
	}

	select {
	case c.send <- data:
		return true
	default:
		return false
	}
}

// Close 幂等关闭。
func (c *wsConn) Close() {
	c.once.Do(func() {
		close(c.closed)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

// CloseWith 先发一条带业务关闭码的控制帧，再关连接。
//
// 用来区分「重试也没用」的失败：握手前的 HTTP 状态码在浏览器里读不到，
// 但关闭码读得到。不发这一帧的话，观战名额被抢这种事在客户端看来
// 就是一次普通断网，会白白退避重试四十七秒。
//
// gorilla 明确允许 WriteControl 与 WriteMessage 并发，不会和 writeLoop 抢写。
func (c *wsConn) CloseWith(code int, text string) {
	if c.conn != nil {
		_ = c.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(code, text),
			time.Now().Add(time.Second),
		)
	}
	c.Close()
}

// writeLoop 是唯一往这条连接写数据的协程。
func (c *wsConn) writeLoop() {
	ticker := time.NewTicker(gamePingPeriod)
	defer func() {
		ticker.Stop()
		c.Close()
	}()

	for {
		select {
		case data := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(gameWriteWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(gameWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.closed:
			return
		}
	}
}

// readLoop 读客户端上行消息并逐条交给 dispatch。
//
// 它必须占住处理这次请求的协程：Handler 一返回，net/http 就会回收底层连接。
// 读出错（断网、对端关闭、超时）就返回，由调用方决定是否要通知对手。
func (c *wsConn) readLoop(dispatch func(ClientMessage)) {
	c.conn.SetReadLimit(gameMaxMessage)
	_ = c.conn.SetReadDeadline(time.Now().Add(gamePongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(gamePongWait))
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}

		var msg ClientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			// 收到脏包不踢人：前端版本不一致时偶尔会多发一条旧格式消息，
			// 断开重连只会让玩家更困惑，回一条 error 让它自己收敛。
			slog.Warn("bad client message", "error", err)
			c.Send(Message{Type: MsgError, TS: time.Now().UnixMilli(), Data: ErrorData{
				Code:    ErrCodeInternal,
				Message: "消息格式不对",
			}})
			continue
		}
		dispatch(msg)
	}
}
