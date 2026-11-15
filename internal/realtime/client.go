package realtime

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 四种时长取自 gorilla/websocket 官方 chat 示例。
// pongWait 是对端必须发回 pong 的期限，pingPeriod 要比它短，
// 给网络抖动留出余量，否则正常连接也会被误判成掉线。
const (
	writeWait      = 10 * time.Second //防止网络阻塞导致服务端的 Goroutine 被挂起（死锁），超过 10 秒直接报错断开
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 1024 //防止黑客发超大文件，瞬间撑爆服务器内存（OOM 攻击）
	sendBuffer     = 16   //核心发件箱的容量
)

// Client 是一条 WebSocket 连接。
// 读和写各占一个 goroutine，send 是两者之间唯一的传递通道。
type Client struct {
	hub    *Hub            //大厅指针，方便注销时找组织
	conn   *websocket.Conn //底层的 TCP 连接对象
	userID uint

	// send 有界，满了就丢连接。容量决定能容忍多慢的客户端。
	send   chan []byte   //无锁发件箱（带缓冲的 Channel）
	closed chan struct{} //广播站，用来通知 WriteLoop 和 ReadLoop 赶紧停机
	once   sync.Once
	//保证 Close() 里面关连接的动作永远只执行一次。
	// 因为网络断开、Hub 踢人、服务重启可能会同时触发关闭，
	// 不加这个并发安全的锁，会导致 Go 崩溃
}

func NewClient(hub *Hub, conn *websocket.Conn, userID uint) *Client {
	return &Client{
		hub:    hub,
		conn:   conn,
		userID: userID,
		send:   make(chan []byte, sendBuffer), //send容量16
		closed: make(chan struct{}),
	}
}

// Send 把消息放进发件箱。返回 false 表示缓冲区已满，调用方应当断开连接。
func (c *Client) Send(msg PushMessage) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		// 序列化失败是服务端的代码问题，不该因此断掉客户端的连接。
		log.Printf("marshal push message: %v", err)
		return true
	}
	select {
	case c.send <- data:
		return true
	default:
		return false
	} //带 default 的 select，这叫非阻塞写入
}

// Close 幂等关闭。Hub 踢人、读泵退出、进程关停可能同时调用它。
func (c *Client) Close() {
	c.once.Do(func() {
		close(c.closed) //相当于拉响了防空警报，WriteLoop 接收到信号后会立刻退出
		// 单元测试不建立真实连接，conn 可能是 nil。
		if c.conn != nil {
			_ = c.conn.Close() //关闭底层的网络连接 c.conn.Close()
		}
	})
}

// WriteLoop 是唯一往这条连接写数据的 goroutine。
// gorilla/websocket 不允许并发写，所以所有出站数据都必须经过这里。
func (c *Client) WriteLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Close()
	}()

	for {
		select {
		case data := <-c.send: //发业务消息
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C: //发心跳 Ping
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			// 控制帧由 gorilla 自己排队，和上面的数据帧不会交错。
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.closed: //监听关闭信号
			return
		}
	}
}

// ReadLoop 什么业务消息都不处理，但它不能省。
// 三个作用：刷新读超时让连接活着、接收对端的 close 帧、发现断开后把连接摘掉。
func (c *Client) ReadLoop() {
	defer func() {
		c.hub.Unregister(c)
		c.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize) //防止恶意大包
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait)) //// 收到 Pong，续命 60 秒
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return //一旦断网或超时，立刻退出循环
		}
	}
}
