// Package realtime 管理 WebSocket 长连接，并按用户投递实时消息。
// 它只管「这个用户现在有没有连接」，不关心消息从哪来。
package realtime

import (
	"log"
	"sync"
)

// PushMessage 是推给前端的一条消息。
// Type 让前端区分消息类别，Data 里放该类别自己的字段。
type PushMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// Hub 是全部在线连接的登记处。
// 一个用户可以有多个连接（多标签页、手机加电脑），所以是两层 map。
type Hub struct {
	mu      sync.RWMutex //读写锁
	clients map[uint]map[*Client]struct{}  //外层 map[uint]：Key 是 UserID
			//业务逻辑是  给文章作者（UserID=10）发通知   需要能通过 UserID 瞬间找到这个人
		//内层 map[*Client]struct{} 	一个人可能在公司电脑、家里电脑、手机三个设备同时打开网页。
		// 所以 UserID 不能对应单一的连接，必须对应一个连接集合
		//为什么不用切片 []*Client？ 因为切片删除元素的复杂度是 O(N)
		//用 Map，删除的复杂度是O(1)  防内存泄漏、踢人时极快
		//为什么用 struct{} 作为值？
		//  因为 struct{} 在 Go 中不占用任何内存空间。我们只关心 Key 在不在，不需要存任何值

} 

func NewHub() *Hub {
	return &Hub{clients: make(map[uint]map[*Client]struct{})}
}

// Register 把一个新连接登记到用户名下。
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set, ok := h.clients[c.userID]
	if !ok { //如果查不到，说明这用户是第一次连上来
		set = make(map[*Client]struct{}) //如果这个用户名下还没有登记册（或者他之前的设备全下线被清理了），就给他新建一个
		h.clients[c.userID] = set // 把空名册挂到该用户名下
	}//如果没有这个if 那么set是nil 直接当map操作会有问题 
	//go没有set数据结构 默认把map 的key存数据 value存空结构体 
	set[c] = struct{}{}
}

// Unregister 摘掉一个连接。重复调用是安全的，内部会判断它还在不在。
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set, ok := h.clients[c.userID] //把这个用户现有的所有连接（set）拿出来
	if !ok {
		return
	}
	if _, ok := set[c]; !ok {
		return
	}
	delete(set, c)
	if len(set) == 0 { //如果 len(set) == 0，说明这个用户把所有的网页都关了，一条连接都不剩了（彻底离线）
		delete(h.clients, c.userID)
//如果只删了内层 set 里的元素，外层 h.clients 里还会保留一个 userID -> 空的Map 会导致内存泄漏
	}
}

// Push 给某个用户的全部连接投递一条消息。
// 锁只用来拷一份连接清单，真正的网络写入在锁外做。
// 持锁写 socket 会让一个慢客户端卡住整个 Hub。
func (h *Hub) Push(userID uint, msg PushMessage) {
	h.mu.RLock() //读锁
	set, ok := h.clients[userID]
	if !ok {
		h.mu.RUnlock()
		return
	}
	targets := make([]*Client, 0, len(set)) 
	for c := range set {
		targets = append(targets, c)// 把连接拷贝出来
	}
	h.mu.RUnlock() //提前解锁

	for _, c := range targets {
		if c.Send(msg) { //走Send的default分支 返回false后 不continue 走下方收件箱满的逻辑
			continue
		}
		// 发件箱满了，说明对端长时间读不动。
		// 断开它，让浏览器重连后靠 HTTP 接口补齐数据，
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
// http.Server.Shutdown 不会处理已经升级成 WebSocket 的连接，这一句必须自己写。
func (h *Hub) CloseAll() {
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
		c.Close() //服务器会给前端发送 Close Frame（关闭帧），前端代码监听到后，会主动重新连接
	}
}
