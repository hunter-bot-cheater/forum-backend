package realtime

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 四种时长取自 gorilla/websocket 官方 chat 示例，改动前先想清楚它们之间的约束：
//   - writeWait：一次写的超时，防止网络阻塞把写 goroutine 永久挂住；
//   - pongWait：对端必须在这个期限内回 pong，否则认为掉线；
//   - pingPeriod 必须明显小于 pongWait，给网络抖动留出余量，
//     否则正常连接也会被误判成掉线。这里取 pongWait 的 5/8；
//   - maxMessageSize：单条消息上限，防止超大包把内存撑爆。
//
// 两个值都比官方示例（60 / 54 秒）紧：手机切 WiFi/4G、运营商 NAT 回收会话
// 之后，连接会变成「半开」——两边内核都还以为连着，数据却发不过去。
// 60 秒才判掉线的话，对面已经盯着「对手掉线了」等了一分钟。
const (
	writeWait      = 10 * time.Second
	pongWait       = 40 * time.Second
	pingPeriod     = (pongWait * 5) / 8
	maxMessageSize = 1024
	sendBuffer     = 16 // 发件箱容量，决定能容忍多慢的客户端
)

// heartbeatFrame 是跟控制帧一起发下去的应用层心跳。
//
// websocket 的 ping 是控制帧，浏览器自动回 pong，页面里的 JS 根本看不到，
// 于是前端没有任何办法判断「我这条连接是不是还活着」。多发一条普通数据帧，
// 前端就能拿它当时钟：超过一定时间没收到任何帧，自己关连接重连，
// 不用干等服务端 40 秒后才把连接判死。
var heartbeatFrame = []byte(`{"type":"ping","data":{}}`)

// Client 是一条 WebSocket 连接。
//
// 读和写各占一个 goroutine（ReadLoop / WriteLoop），send 是两者之间唯一的传递通道。
// gorilla/websocket 不允许并发写同一个连接，所以所有出站数据都必须经过 WriteLoop。
type Client struct {
	hub    *Hub            // 反向引用，注销时要靠它找到登记表
	conn   *websocket.Conn // 底层连接，单元测试里可能为 nil
	userID uint

	// send 有界，满了就丢连接：宁可让前端重连，也不在内存里无限排队。
	send   chan []byte
	closed chan struct{} // 关闭广播，通知两个 goroutine 停机

	// once 保证 Close 里的关闭动作只执行一次。
	// 网络断开、Hub 踢人、进程重启可能同时触发关闭，
	// 重复 close 同一个 channel 会让进程直接 panic。
	once sync.Once
}

// NewClient 在 WebSocket 升级成功后调用，此时 userID 已经从 token 里取出来了。
func NewClient(hub *Hub, conn *websocket.Conn, userID uint) *Client {
	return &Client{
		hub:    hub,
		conn:   conn,
		userID: userID,
		send:   make(chan []byte, sendBuffer),
		closed: make(chan struct{}),
	}
}

// Send 把消息序列化后放进发件箱。
// 返回 false 表示缓冲区已满，调用方（Hub.Push）应当断开这条连接。
func (c *Client) Send(msg PushMessage) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		// 序列化失败是服务端的代码问题，不该因此断掉客户端的连接，当作已处理。
		slog.Error("marshal push message", "user_id", c.userID, "error", err)
		return true
	}

	// 带 default 的 select 就是非阻塞写：塞不进去立刻走 default 返回 false。
	select {
	case c.send <- data:
		return true
	default:
		return false
	}
}

// Close 幂等关闭。Hub 踢人、读循环退出、进程关停可能同时调用它。
func (c *Client) Close() {
	c.once.Do(func() {
		// 关 closed 相当于广播停机信号，WriteLoop 的 select 立刻命中并退出。
		close(c.closed)
		// 单元测试不建立真实连接，conn 可能是 nil。
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

// WriteLoop 是唯一往这条连接写数据的 goroutine，随连接建立而启动。
// 它负责发业务消息和定时心跳，任一写失败或收到关闭信号就退出，并顺手关连接。
func (c *Client) WriteLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Close()
	}()

	for {
		select {
		case data := <-c.send:
			// 每次写之前都重设超时，避免上一次写的截止时间累积影响这一次。
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			// 控制帧由 gorilla 自己排队，和上面的数据帧不会交错。
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
			// 紧跟一条应用层心跳，给前端的看门狗用（见 heartbeatFrame）。
			if err := c.conn.WriteMessage(websocket.TextMessage, heartbeatFrame); err != nil {
				return
			}
		case <-c.closed:
			return
		}
	}
}

// ReadLoop 什么业务消息都不处理，但它不能省。
//
// 三个作用：
//   - 不断读，才能触发 pong 处理器，把读超时往后推，让空闲连接不被误杀；
//   - 接收对端的 close 帧，感知对方主动断开；
//   - 读出错时把连接从 Hub 摘掉，避免留下死连接。
func (c *Client) ReadLoop() {
	defer func() {
		c.hub.Unregister(c)
		c.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		// 每收到一次 pong 就续命，空闲连接因此能一直活着。
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		// 服务端不消费客户端上行内容，读到的消息直接丢弃。
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return // 断网、超时、对端关闭都走这里
		}
	}
}
