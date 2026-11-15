// Package realtime 管理 WebSocket 长连接，并按用户投递实时消息。
// 它只管「这个用户现在有没有连接」，不关心消息从哪来。
package realtime

import (
	"log"
	"sync"
)

// PushMessage 是推给前端的一条消息。
// Type 让前端区分消息类别（如 notification.created），Data 里放该类别自己的字段。
type PushMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// Hub 是全部在线连接的登记处。
//
// 结构是两层 map：外层键是用户 ID，内层是该用户名下的连接集合。
// 之所以不是一人一条连接，是因为同一个人可能同时开三个标签页、手机加电脑都在线，
// 发通知时必须给这个人名下的每条连接都推一份。
//
// 内层用 map[*Client]struct{} 而不是切片，理由有两条：
//   - 断开连接时要按指针精确删除，map 删除是 O(1)，切片要 O(n) 遍历；
//   - 值用空结构体，Go 里它不占内存，这层只关心「键在不在」。
//
// 所有读写都过 mu：Push 用读锁（并发读），Register/Unregister/CloseAll 用写锁。
type Hub struct {
	mu      sync.RWMutex
	clients map[uint]map[*Client]struct{}
}

// NewHub 创建一个空 Hub，进程内通常只有一个实例，由 main 装配后注入各模块。
func NewHub() *Hub {
	return &Hub{clients: make(map[uint]map[*Client]struct{})}
}

// Register 把一个新连接登记到用户名下。
// 由 WebSocket 升级成功后调用一次，同一用户的第一条连接会顺手建好内层 map。
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 该用户名下还没有任何连接（第一次上线，或上次全部下线后被清掉）时先建内层 map。
	// 少了这一步，下面往 nil map 里写会直接 panic。
	set, ok := h.clients[c.userID]
	if !ok {
		set = make(map[*Client]struct{})
		h.clients[c.userID] = set
	}
	set[c] = struct{}{}
}

// Unregister 摘掉一个连接。重复调用是安全的，内部会判断它还在不在。
// 由 ReadLoop 退出时调用，Hub 踢慢客户端时也会调用。
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	set, ok := h.clients[c.userID]
	if !ok {
		return
	}
	if _, ok := set[c]; !ok {
		return
	}
	delete(set, c)

	// 这个人的最后一条连接也断了，把外层键一起删掉。
	// 只删内层元素会留下 userID -> 空 map 的条目，长跑下来就是内存泄漏。
	if len(set) == 0 {
		delete(h.clients, c.userID)
	}
}

// Push 给某个用户的全部连接投递一条消息。
// 锁只用来拷一份连接清单，真正的网络写入在锁外做：
// 持锁写 socket 会让一个慢客户端把整个 Hub 卡住。
func (h *Hub) Push(userID uint, msg PushMessage) {
	h.mu.RLock()
	set, ok := h.clients[userID]
	if !ok {
		h.mu.RUnlock()
		return
	}
	targets := make([]*Client, 0, len(set))
	for c := range set {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		if c.Send(msg) {
			continue
		}
		// 发件箱满了，说明对端长时间读不动。
		// 直接断开它，让浏览器重连后靠 HTTP 接口补齐数据，
		// 比在内存里给它无限排队安全得多。
		log.Printf("drop slow websocket client user=%d", c.userID)
		c.Close()
		h.Unregister(c)
	}
}

// OnlineUsers 返回当前有连接的用户数。
func (h *Hub) OnlineUsers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// OnlineConns 返回当前连接总数。同一用户多开标签页会算多个。
func (h *Hub) OnlineConns() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, set := range h.clients {
		n += len(set)
	}
	return n
}

// CloseAll 断开全部连接，供进程退出时调用。
// http.Server.Shutdown 不会处理已经升级成 WebSocket 的连接，这一步必须自己写。
func (h *Hub) CloseAll() {
	// 先在锁内把清单拷出来并把登记表清空，再在锁外逐条关，
	// 避免关闭动作（可能阻塞）占着写锁。
	h.mu.Lock()
	targets := make([]*Client, 0, len(h.clients))
	for _, set := range h.clients {
		for c := range set {
			targets = append(targets, c)
		}
	}
	h.clients = make(map[uint]map[*Client]struct{})
	h.mu.Unlock()

	for _, c := range targets {
		// Close 会发 WebSocket 关闭帧，前端收到后可以按自己的策略重连。
		c.Close()
	}
}
