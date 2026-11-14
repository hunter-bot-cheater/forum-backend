# 第 3 周进阶 C：WebSocket 实时通知

04a 章把通知做成了异步：点赞后由 worker 写库，作者下次请求 `/api/notifications` 才看得到红点。功能是对的，体验差一截。本章把这段距离从「下次刷新」压到「当场」。

## 本章交付物

| 交付物 | 位置 | 说明 |
| --- | --- | --- |
| 连接登记中心 | `internal/realtime/hub.go` | 按用户索引连接，支持一人多端 |
| 单连接生命周期 | `internal/realtime/client.go` | 读写分离、心跳、超时、幂等关闭 |
| 升级与鉴权 | `internal/handler/ws.go` | 握手前校验 token，Origin 同源校验 |
| 在线统计 DTO | `internal/handler/dto.go` | 追加 `onlineStatsResp` |
| 推送接入 | `internal/worker/notification.go` | 通知落库成功后推给在线收件人 |
| 装配改造 | `internal/router/router.go`、`cmd/server/main.go` | 引入 `Deps`，关停时主动断连 |
| 单测 | `internal/realtime/hub_test.go` | 六个用例，含真实 socket 一条 |

## 0. 为什么要加这一章

### 0.1 现在的体验差在哪

把时序摊开看，问题很清楚。

```text
A 给 B 的文章点赞
  │
  ├─ LikeService 写 article_likes，写 outbox
  ├─ Relay 200ms 内把事件投进 Stream
  ├─ NotificationWorker 消费，写 notifications 一行
  └─ 到这里服务端该做的都做完了
        │
        └─ B 的浏览器此刻完全不知道
              │
              └─ 直到 B 手动刷新，或前端定时轮询 30 秒一次
```

服务端到落库是毫秒级，用户感知却是「下次刷新」。中间这段空白就是本章要填的。

### 0.2 实时消息解决什么，不解决什么

这一节先划清边界，后面的设计才不会跑偏。

| 问题 | WebSocket 能不能解决 | 原因 |
| --- | --- | --- |
| 作者要刷新才看见红点 | 能 | 服务端主动推，浏览器收到就更新 UI |
| 通知会不会丢 | 不能 | 连接断开期间推的东西就是没了 |
| 用户离线时发生了什么 | 不能 | 没有连接，推给谁 |
| 消息顺序有保证吗 | 不能 | 单连接内有序，跨连接无意义 |
| 多实例部署时推到别的机器上的人 | 不能 | 本章的 Hub 只认本进程的连接 |

有一个结论要记住：**可靠性的真相源始终是 MySQL 里那行 `notifications`**。WebSocket 只是把「已经有了」这件事提前告诉浏览器。断线重连后前端仍然要调 `GET /api/notifications` 把落下的补上。把这个前提写进代码注释，比事后解释省事。

### 0.3 验收合同

| 编号 | 条件 | 验证方式 |
| --- | --- | --- |
| C1 | 点赞后 1 秒内，被赞作者的长连接收到 `notification.created` | 手工：两个浏览器窗口 |
| C2 | 同一用户开两个标签页，两边都收到 | 手工：同一账号开两个窗口 |
| C3 | 不带 token 或 token 过期时握手被拒，返回 401 而不是升级成功 | `curl -i` |
| C4 | 慢客户端不会拖住其他用户，缓冲满了自己断开 | 单测 `TestSlowClientGetsDropped` |
| C5 | `GET /api/ws/stats` 返回的在线数与实际连接数一致 | 手工：连三个断一个 |
| C6 | 进程收到 SIGTERM 后连接被主动关闭，不残留 goroutine | 手工：Ctrl+C 看日志 |

### 0.4 前置条件与进度快照

| 项 | 要求 |
| --- | --- |
| 04a 章 | 已完成。`NotificationWorker` 在跑，`notifications` 表已建 |
| 04 章 4.10 | 可选。它与本章无耦合 |
| 依赖 | `github.com/gorilla/websocket v1.5.3`。当前 `go.mod` 里还没有这一行，必须自己 `go get`，否则 `client.go` 一写就报缺包 |
| 路由签名 | 本章会把 `router.New(db, jwt, rdb)` 改成 `router.New(Deps)`，是一次破坏性改动 |

拉依赖：

```bash
go get github.com/gorilla/websocket@v1.5.3
```

工作树里已经落盘的 04a 代码，本章只改 `NotificationWorker` 的构造函数签名与推送那几行，读取循环部分第 5 章再动。

### 0.5 文件放哪，先写哪个，什么时候能编译

本章新增的文件都在下面这棵树里，`[改]` 标记的是已有的文件，只需要动几行。

```text
forum-backend/
├── internal/realtime/          新建目录
│   ├── hub.go                  Hub 与 PushMessage
│   ├── client.go               Client，读写两个泵
│   └── hub_test.go             单测
├── internal/handler/
│   ├── ws.go                   升级与鉴权
│   └── dto.go                  [改] 追加 onlineStatsResp
├── internal/worker/
│   └── notification.go         [改] 推送接入
├── internal/router/
│   └── router.go               [改] Deps 与两条路由
└── cmd/server/
    └── main.go                 [改] 装配与关停
```

`internal/realtime/` 这个目录要自己建：

```bash
mkdir -p internal/realtime
```

**`hub.go` 与 `client.go` 必须成对落盘。** 两者都是 `package realtime`，且互相引用：`hub.go` 的 `map[uint]map[*Client]struct{}` 用到 `Client`，`client.go` 的 `Client.hub` 字段用到 `Hub`。Go 里同一 package 内的声明顺序不影响编译，所以 `hub.go` 写在 `client.go` 前面没有关系，编译器看的是整个包。但只落盘其中一个时包不完整，构建一定失败：

| 落盘状态 | `go build ./internal/realtime/` 的结果 | 结论 |
| --- | --- | --- |
| 只有 `hub.go` | `undefined: Client`，报 7 处 | 正常，3.2 还没写 |
| 只有 `client.go` | `undefined: Hub`、`undefined: PushMessage` | 正常，2.2 还没写 |
| 两个都在 | 通过 | 可以继续 |

上面三种状态都在本机实测过，报错原文就是那几行。

看到那些 `undefined` 不是哪里写错了，是同一个包的另一半还没落地。2.2 与 3.2 要连着做，中间不必跑构建。

**空文件也会报错。** 如果 `internal/realtime/hub.go` 只是 `touch` 出来还没填内容，`go build ./internal/realtime/` 会报：

```text
internal\realtime\hub.go:1:1: expected 'package', found 'EOF'
```

Go 的每个 `.go` 文件都必须以 `package` 声明开头，0 字节的文件不满足。把 2.2 的代码整段贴进去就好。同理，写到一半的文件也会报各种语法错，所以写的过程中不必急着构建，一个文件写完再跑一次。

整章的落盘顺序按「能不能编译」切成六段：

| 段 | 落盘内容 | 本段结束能编译吗 |
| --- | --- | --- |
| 1 | `realtime/hub.go` + `realtime/client.go`（2.2、3.2） | 能。还没有任何调用方 |
| 2 | `handler/ws.go` + `dto.go` 追加 + 两条路由（4.3、4.4） | 不能。路由里引用的 `Hub` 还没注入进 router，处理办法见 4.4 那一段 |
| 3 | `router.go` 的 `Deps` 改造 + `main.go` 装配（7.1 到 7.3） | 能 |
| 4 | `notification.go` 推送（5.2） | 能 |
| 5 | 统计接口（6.2） | 能 |
| 6 | `hub_test.go`（8.1） | 能，`go test ./...` 全绿 |

只有第 2 段到第 3 段之间必然编不过，因为 4.4 的路由要拿 `Hub`，而 `router.New` 还只收三个位置参数。两条走法：

- **接受这段红着**：把 4.3、4.4、7.1、7.2、7.3 一口气写完再构建。好处是不用来回翻。
- **全程可编译**（推荐）：先写 4.3 的 `ws.go`，接着做 7.1 的 `Deps` 结构体与 7.2 的 router 改造，再回来写 4.4 的路由，最后做 7.3 的 `main.go`。代价是阅读顺序要跳一次。

其余每一段都可以跑一次 `go build ./...` 确认再往下。

依赖要在第 1 段之前装好，`hub.go` 与 `client.go` 都 import `gorilla/websocket`。

## 1. 先把「实时」的语义定清楚

### 1.1 三种做法的对比

| 做法 | 服务端成本 | 延迟 | 断线恢复 | 适用场景 |
| --- | --- | --- | --- | --- |
| 短轮询（每 10 秒请求一次） | 每个在线用户每分钟 6 次请求，绝大多数返回空 | 平均 5 秒 | 天然有，下次请求就补上 | 通知量极低、不想加长连接 |
| SSE（Server-Sent Events） | 一条 HTTP 长响应，单向推送 | 毫秒级 | 靠 `Last-Event-ID` 补 | 只需要服务端推 |
| WebSocket | 一条双向长连接，占一个 goroutine | 毫秒级 | 要自己做 | 需要双向，或将来要复用这条通道 |

短轮询的账要先算清楚：1000 个在线用户，10 秒轮询一次，就是每秒 100 次请求。这些请求每一发都要过限流中间件、查 MySQL 数未读数。为了一个红点，代价太大。

SSE 其实更省事，浏览器原生支持，纯 HTTP，不用改协议。它的问题在单向：客户端想发心跳、想标已读，还得另开接口。本项目现在只需要服务端推，SSE 完全够用。

选 WebSocket 的理由是下一步：这条通道建起来后，评论区的实时消息、在线状态、正在输入都能复用。用 SSE 做这些要开好几条连接。

### 1.2 为什么选 WebSocket

三条：

1. 全双工。客户端可以发心跳、发已读回执，不用另外开接口。
2. 复用同一条 TCP，后续加功能不用再谈一次协议。
3. 简历上这个词的分量比 SSE 重，而实现成本只高一点点。

还有一条现实原因：`gorilla/websocket` 的 API 稳定、文档齐、示例就是官方 chat demo，学习曲线平缓。

### 1.3 一次推送要经过哪些环节

```text
A 点赞
  │
  ▼
LikeService ──事务──► article_likes + outbox_events
                            │
                     Relay 每 200ms 扫表
                            │
                            ▼
                    XADD forum:events:v1
                            │
                            ▼
        NotificationWorker（消费组 forum:notify:v1）
                            │
              ① 解析事件  ② 回查文章作者
              ③ INSERT notifications（唯一索引去重）
                            │
                            ▼
              ④ hub.Push(作者ID, 消息)  
                            │
                  ┌─────────┴─────────┐
                  │                   │
            作者在线               作者离线
                  │                   │
         写 send 通道 → 断线        直接返回，什么都不做
                  │
            写泵发到 socket
                  │
                  ▼
            浏览器 onmessage
                  │
                  ├─ 红点 +1
                  └─ 可选：拉一次 /api/notifications 对齐数据
```

第 ④ 步是本章唯一新增的环节。其余五步一行都不用改，这是 04a 那套架构的红利。

## 2. Hub：连接怎么管

### 2.1 三个要回答的问题

写代码之前先把问题列出来，设计就是回答这些问题。

| 问题 | 答案 | 落在哪 |
| --- | --- | --- |
| 怎么按用户找到连接 | `map[uint]map[*Client]struct{}`，外层 key 是 userID | `Hub.clients` |
| 一个用户开五个标签页怎么办 | 内层是集合，推送时遍历全部连接 | `Hub.Push` |
| 并发改 map 会不会 panic | 用 `sync.RWMutex` 保护，读多写少 | `Hub.mu` |

还有一个隐藏问题：推送要写 socket，写 socket 可能阻塞。如果持锁写，一个卡住的连接会让所有推送排队。所以锁只用来拷一份连接清单，真正的发送在锁外做。这是本章最重要的一个设计决定。

### 2.2 完整文件：`internal/realtime/hub.go`

新建文件。目录不存在就先 `mkdir -p internal/realtime`，包名写 `package realtime`。

写完这个文件 `go build ./...` 会报 `undefined: Client`，因为 `Client` 在 3.2 才定义。两个文件同属 `package realtime`，要一起写完才能编译，顺序见 0.5。

```go
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
	mu      sync.RWMutex
	clients map[uint]map[*Client]struct{}
}

func NewHub() *Hub {
	return &Hub{clients: make(map[uint]map[*Client]struct{})}
}

// Register 把一个新连接登记到用户名下。
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set, ok := h.clients[c.userID]
	if !ok {
		set = make(map[*Client]struct{})
		h.clients[c.userID] = set
	}
	set[c] = struct{}{}
}

// Unregister 摘掉一个连接。重复调用是安全的，内部会判断它还在不在。
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
	if len(set) == 0 {
		delete(h.clients, c.userID)
	}
}

// Push 给某个用户的全部连接投递一条消息。
// 锁只用来拷一份连接清单，真正的网络写入在锁外做。
// 持锁写 socket 会让一个慢客户端卡住整个 Hub。
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
		c.Close()
	}
}
```

### 2.3 逐行拆解

**`map[uint]map[*Client]struct{}` 而不是 `map[uint][]*Client`**

切片删除要遍历找下标，连接数一多就是 O(n)。集合删除是 O(1)。内层用 `struct{}` 作值，不占内存。

**`Unregister` 的两层判断**

第一层防「这个用户一个连接都没有」，第二层防「这个连接已经被摘过了」。读泵退出、Hub 踢人、进程关停三处都可能调它，没有这两层会重复删除，虽然 `delete` 对不存在的 key 是安全的，但 `len(set) == 0` 后的外层 `delete` 会误删刚被别的 goroutine 加进来的用户。加锁之后这个风险其实已经没了，两层判断是为了让语义明确。

**`Push` 里的快照**

```go
targets := make([]*Client, 0, len(set))
for c := range set {
	targets = append(targets, c)
}
h.mu.RUnlock()
```

这三行的意义是「锁的持有时间只到拷完为止」。真正的 `c.Send(msg)` 在锁外，即使某个连接的 `send` 通道满了导致 `Send` 慢一点，其他用户也不受影响。

如果直接在 `RLock` 里循环发送，会有一个更难查的问题：`Send` 失败时会调 `h.Unregister(c)`，而它要拿写锁。`RLock` 里再要写锁，`sync.RWMutex` 会直接死锁。这个坑比性能问题严重得多。

**`OnlineUsers` 与 `OnlineConns` 分开**

这两个数不是一回事。三个用户，其中一人开了两个标签页，就是 3 个用户、4 条连接。混在一起报会让人误判。

**`CloseAll` 里重建 map**

```go
h.clients = make(map[uint]map[*Client]struct{})
```

不用逐条 delete。关停时连接已经不收新消息了，直接把整个表换掉更快，也顺便避免了「边遍历边删」的写法问题。

### 2.4 为什么不用 channel 版 Hub

`gorilla/websocket` 官方 chat 示例里的 Hub 是 channel 版：

```go
type Hub struct {
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	broadcast  chan []byte
}
```

所有变更都发给一个 goroutine 串行处理。好处是完全不用锁，坏处有三个：

1. 多一个常驻 goroutine 和一个 `select` 循环，读代码时要跳来跳去。
2. 关停时要额外发一个信号让这个 goroutine 退出，容易漏。
3. `Push` 变成「发一条消息然后等结果」，要么加回复通道，要么接受它只是投递不保证处理。

本项目用读写锁只有 40 行，逻辑一眼看完，且 `Push` 是同步语义，调用方知道消息入队了没有。用锁的代价是理论上多一点竞争，实际连接数在几千以内，读锁几乎不阻塞。

选择标准很简单：**并发安全问题用最少的机制解决，能一眼看懂优先**。channel 版适合连接数上万、需要精细调度的场景，学习项目不属于这种。

## 3. 第 1 步：一条连接的生命周期

### 3.1 读泵与写泵

`gorilla/websocket` 有一个硬约束：**同一个连接不允许并发写**。两个 goroutine 同时调 `WriteMessage` 会 panic。

所以一条连接固定配两个 goroutine：

| goroutine | 职责 | 出口条件 |
| --- | --- | --- |
| 写泵 `WriteLoop` | 从 `send` 通道取数据写到 socket，定时发 ping | 写失败、`closed` 被关 |
| 读泵 `ReadLoop` | 阻塞读，只为了发现断开、续期超时 | 读出错 |

两者之间只有一条有界通道 `send`。Hub 把消息塞进通道，写泵负责落地。这样「并发推送」变成了「并发写通道」，天然安全。

### 3.2 完整文件：`internal/realtime/client.go`

和 `hub.go` 同目录、同包，文件名不同即可。写完这两个文件，`go build ./internal/realtime/` 才会从 `undefined` 转成通过。

这个文件 import `gorilla/websocket`。如果构建报 `no required module provides package github.com/gorilla/websocket`，说明 0.4 那条 `go get` 还没跑，先把依赖装上。`hub.go` 只用标准库 `log` 与 `sync`，所以它单独报错只会是 `undefined: Client`，不会报缺包。

```go
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
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 1024
	sendBuffer     = 16
)

// Client 是一条 WebSocket 连接。
// 读和写各占一个 goroutine，send 是两者之间唯一的传递通道。
type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	userID uint

	// send 有界，满了就丢连接。容量决定能容忍多慢的客户端。
	send   chan []byte
	closed chan struct{}
	once   sync.Once
}

func NewClient(hub *Hub, conn *websocket.Conn, userID uint) *Client {
	return &Client{
		hub:    hub,
		conn:   conn,
		userID: userID,
		send:   make(chan []byte, sendBuffer),
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
	}
}

// Close 幂等关闭。Hub 踢人、读泵退出、进程关停可能同时调用它。
func (c *Client) Close() {
	c.once.Do(func() {
		close(c.closed)
		// 单元测试不建立真实连接，conn 可能是 nil。
		if c.conn != nil {
			_ = c.conn.Close()
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
		case data := <-c.send:
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
		case <-c.closed:
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

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}
```

### 3.3 逐行拆解

**`Send` 返回 bool 而不是 error**

调用方只需要知道「能不能入队」。序列化失败确实是错误，但处理方式不是断连接，所以返回 `true` 让消息被丢弃、连接保留。如果返回 error，Hub 要么多一个分支判断错误类型，要么把所有错误都当成「踢人」，两种都不对。

**`select` 的 `default`**

```go
select {
case c.send <- data:
	return true
default:
	return false
}
```

非阻塞发送。没有 `default` 的话，通道满时 `Send` 会一直等写泵取走。写泵卡住（对端不读、TCP 窗口满）时，`Send` 就会卡在这个连接上，而 `Push` 是串行遍历所有连接的，一个卡住全部卡住。`default` 把「等」变成「立刻放弃」。

**`sendBuffer` 取 16**

这个数字决定能容忍多慢的客户端。16 条消息，每条几百字节，一个连接的缓冲上限就是几 KB。正常浏览器处理消息是毫秒级，16 足够扛住一次 GC 停顿。取 1024 也能工作，但一个卡死的客户端会占几百 KB，乘以连接数就不可控了。

**`sync.Once` 的用途**

`Close` 有三个调用方：Hub 踢慢客户端、`WriteLoop` 的 defer、`ReadLoop` 的 defer。没有 `Once`，`close(c.closed)` 会被执行两次，第二次直接 panic（关一个已关闭的 channel 是运行时错误，不是返回 error）。

**`conn != nil` 的判断**

这个判断只服务单元测试。`hub_test.go` 里造 Client 时不建立真实 socket，测完调 `Close` 会碰到 nil。为测试留一个判断，比让每个测试都起一个 httptest 服务省事得多。

**`ReadLoop` 为什么必须有**

新手最容易觉得「服务端只推不收，读循环没用」。省掉它会有三个后果：

1. 不会调用 `SetPongHandler`，60 秒后读超时不会触发，连接看起来一直活着。
2. 对端发来 close 帧没人处理，`WriteMessage` 要等到下一次写才发现断了。
3. 没有 goroutine 阻塞在这个连接上，`Unregister` 永远不会被调用，Hub 里留下幽灵连接，推送一直往里塞。

三个后果里第三个最隐蔽：`OnlineUsers` 的数字虚高，推送一直成功入队，但没人读。

**`SetReadLimit(maxMessageSize)`**

限制单条消息 1024 字节。这个项目客户端只发心跳，不需要大消息。不设上限的话，恶意客户端可以发一条几百 MB 的消息把内存撑爆。

### 3.4 四个超时值的来历

| 常量 | 值 | 依据 |
| --- | --- | --- |
| `writeWait` | 10 秒 | 写一次消息的期限。超过说明对端 TCP 窗口满了，应当断开 |
| `pongWait` | 60 秒 | 对端发回 pong 的期限。官方 chat 示例值 |
| `pingPeriod` | 54 秒 | `pongWait * 9 / 10`，比 pong 期限短，避免边界抖动误判 |
| `maxMessageSize` | 1024 字节 | 客户端只需要发心跳，不需要大消息 |

`pingPeriod` 必须严格小于 `pongWait`，否则 ping 和 pong 的判定会打架。取 90% 是给网络留一次重传的时间。

有一个实际操作要知道：中间有 Nginx 这类反向代理时，默认 `proxy_read_timeout` 是 60 秒。连接长时间没数据会被代理掐断。所以心跳不能省，54 秒的周期刚好能续上。如果运维把代理超时调小了，这里的 `pingPeriod` 要跟着调小。

## 4. 第 2 步：升级与鉴权

### 4.1 浏览器不能设请求头

这是一个必须提前知道的限制。浏览器提供的构造函数只有两个参数：

```javascript
new WebSocket(url, protocols)
```

没有第三个参数放 headers。所以 `Authorization: Bearer xxx` 这条路在浏览器里走不通，而现有的 `JWTAuth` 中间件恰恰只认这个头。

服务端没有这个问题，Go 或 Node 的客户端可以随便设头。所以这个限制只影响浏览器，但它决定了接口怎么设计。

### 4.2 三种传 token 的方案对比

| 方案 | 做法 | 优点 | 缺点 |
| --- | --- | --- | --- |
| 查询参数 | `wss://host/api/ws/notifications?token=xxx` | 浏览器原生支持，代码最少 | token 会进 Nginx access log、进浏览器历史、进 Referer |
| 子协议 | `new WebSocket(url, ["bearer", token])` | 不进 URL，相对干净 | 服务端要读 `Sec-WebSocket-Protocol` 并回写选中的子协议，实现绕 |
| 临时票据 | 先用正常 HTTP 换一个 30 秒有效的一次性 ticket，再拿 ticket 连 | 泄露的窗口极短，最安全 | 多一个接口，多一次往返 |

本章选**查询参数**，理由是这个项目跑在本机、token 有效期 24 小时、日志归自己管。选它的同时要说清代价，不能假装没这回事。

生产环境要改的话，按这个顺序：先上临时票据，工程量大但最正确；退一步用子协议，至少 token 不进日志。

### 4.3 完整文件：`internal/handler/ws.go`

新建文件，落在已有的 `internal/handler/` 目录下，包名 `package handler`。这个文件引用了 `internal/realtime`，所以必须在 2.2 与 3.2 之后写。

```go
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
		return u.Host == r.Host
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
	client.ReadLoop()
}

// Stats 返回当前在线人数与连接数。
func (h *WSHandler) Stats(c *gin.Context) {
	response.OK(c, onlineStatsResp{
		OnlineUsers: h.hub.OnlineUsers(),
		OnlineConns: h.hub.OnlineConns(),
	})
}
```

### 4.4 增量修改：DTO 与路由

**增量修改**：`internal/handler/dto.go` 末尾追加一个结构体。

```go
// onlineStatsResp 是在线状态接口 data 的形状。
// 用结构体而不是 gin.H，字段拼错由编译器拦住。
type onlineStatsResp struct {
	OnlineUsers int `json:"online_users"`
	OnlineConns int `json:"online_conns"`
}
```

**增量修改**：`internal/router/router.go` 里创建 handler 的位置，放在 `notificationHandler` 之后。

```go
	wsHandler := handler.NewWSHandler(deps.Hub, deps.JWT)
```

这里写的是 `deps.Hub`，但 `Deps` 要到 7.1 才引入。按小节顺序跟做会在这一步断掉：`router.New` 现在只收 `(db, jwt, rdb)` 三个位置参数，`deps` 这个名字还不存在。

两种处理，选一种：

1. **先跳后补**（推荐）。先把 7.1 的 `Deps` 结构体与 7.2 的 router 改造做完，回到这里写 `deps.Hub`，再往下做 7.3 的 `main.go`。全程可编译。
2. **先写其余，最后回来**。跳过这里的 handler 创建与两条路由，把 7.1 到 7.3 全部做完，最后回来补这两行。

不要试图「先传一个 `hub` 局部变量进去，之后再改成 `deps.Hub`」。那等于临时改一次 `New` 的签名，改完还得再改一次，中间还要顺手改 `main` 的调用处，比直接做 7.1 更绕。

**增量修改**：同一个文件里加两行路由。位置在公开接口区，不是 `auth` 组。

```go
		// WebSocket 握手带不了自定义头，token 由 handler 自己从查询参数读，
		// 所以这两条放在公开组里，不能挂 JWTAuth。
		api.GET("/ws/notifications", wsHandler.Notifications)
		api.GET("/ws/stats", wsHandler.Stats)
```

### 4.5 Origin 校验为什么必须自己写

`Upgrader.CheckOrigin` 为 nil 时，gorilla 有一个「安全默认」：Origin 头存在且与 Host 不一致就拒绝。

这个默认值对纯 HTTP 够用，对 WebSocket 不够。原因在于 WebSocket 握手是普通 HTTP 请求，浏览器会带上目标站点的 Cookie。一个恶意页面可以这样写：

```javascript
const ws = new WebSocket("wss://your-forum.com/api/ws/notifications?token=...");
```

同源策略不拦这个。如果 token 存在 Cookie 里，浏览器自动带上，服务端就会认为这是合法用户。这叫跨站 WebSocket 劫持（CSWSH）。

源码写的是显式白名单。`allowedOrigins` 里列出允许的 host，解析 Origin 后取 `u.Host` 查表。目前只放了两个值，正式域名 `app.forum.com` 与本机 `127.0.0.1:8080`。curl 与 Go 客户端这类非浏览器调用不带 Origin，源码直接放行，因为它们本来就绕不过 token 校验。

本地前端跑在 `localhost:5173` 时，Origin 的 host 是 `localhost:5173`，白名单里没有，握手会被拒 403。代理转发不会改写 Origin 头。处理办法见 `docs/08` 第 2.3 节的方案 A。

生产环境前后端分开部署（前端在 `app.example.com`，接口在 `api.example.com`）时，同样要把前端域名加进白名单。退化成 `return true` 等于把 CSWSH 的门打开。

## 5. 第 3 步：worker 里推送

### 5.1 改哪个位置

推送点只有一个：`NotificationWorker.handle` 里 `Create` 成功之后。

为什么在这里，不在 `LikeService` 里：

| 位置 | 问题 |
| --- | --- |
| `LikeService.apply` | 它只知道自己写了一条点赞，不知道收件人是谁。要推得先查文章作者，等于把消费者干的活搬到写路径上 |
| `Relay` | 它只负责投递事件，不认识通知长什么样 |
| `NotificationWorker.handle` | 通知已经建好，`notice.UserID` 就是收件人，`notice.ID` 就是前端要的 id |

放在这里还有一个隐性好处：通知的幂等已经由唯一索引保证了，推送天然也就只发生一次，不需要额外去重。

### 5.2 增量修改：notification worker

四段改动。

第一段，import 加一个包。

```go
	"forum-backend/internal/realtime"
```

第二段，结构体与构造函数加 `hub`。

```go
// NotificationWorker 从消费组取事件，写成站内通知，再推给在线的收件人。
type NotificationWorker struct {
	db  *gorm.DB
	rdb *redis.Client
	hub *realtime.Hub
}

func NewNotificationWorker(db *gorm.DB, rdb *redis.Client, hub *realtime.Hub) *NotificationWorker {
	if hub == nil {
		// 构造函数保证 hub 不为 nil，调用方不必在每次推送前判断。
		hub = realtime.NewHub()
	}
	return &NotificationWorker{db: db, rdb: rdb, hub: hub}
}
```

第三段，`handle` 的结尾。

```go
	if res.Error != nil {
		return fmt.Errorf("insert notification %s: %w", p.EventID, res.Error)
	}
	// RowsAffected 为 0 说明这条事件之前已经消费过，通知早就建好了。
	// 重复事件不再推一次，否则前端红点会闪两下。
	if res.RowsAffected == 0 {
		return nil
	}

	// 推送是尽力而为：收件人不在线、连接刚好断开，都不影响这条通知已经落库。
	// 所以这里不检查结果，也不能因为推送失败去回滚或重投。
	w.hub.Push(notice.UserID, realtime.PushMessage{
		Type: "notification.created",
		Data: pushPayload{
			ID:         notice.ID,
			Type:       notice.Type,
			ArticleID:  notice.ArticleID,
			ActorID:    notice.ActorID,
			TargetType: notice.TargetType,
			TargetID:   notice.TargetID,
			CreatedAt:  notice.CreatedAt,
		},
	})
	return nil
}
```

第四段，文件末尾加推送用的结构体。

```go
// pushPayload 是推给前端的通知内容。
// 只带 ID 与类型：前端拿着 target_type 与 target_id 自己去拉详情，
// 免得把 users 表 join 加进消费者这条热路径。
// 新增行为时这里不加字段，只多一种 target_type 取值。
type pushPayload struct {
	ID         uint      `json:"id"`
	Type       string    `json:"type"`
	ArticleID  uint      `json:"article_id"`
	ActorID    uint      `json:"actor_id"`
	TargetType string    `json:"target_type"`
	TargetID   uint      `json:"target_id"`
	CreatedAt  time.Time `json:"created_at"`
}
```

### 5.3 推送失败为什么不能影响 ACK

`Push` 没有返回值，这是刻意的。

假设让它返回 error，并且在推送失败时 `return err`，会发生什么：消息不被 ACK，留在 PEL，下一轮重读，再查一遍文章、再插一次通知（被唯一索引挡住）、再推一次。收件人离线时这一步永远不会成功，于是这条消息无限重试，PEL 越积越长。

推送的正确语义是「通知已经存好了，顺手告诉他一声」。告诉他失败了，数据仍在。所以这里只能尽力而为。

如果真要做到「推不到就重试」，正确做法是另建一个待推送队列，而不是拿 Stream 的事务消息去重试推送。这个区别在本章不做，列在 9.2。

### 5.4 RowsAffected 为 0 时为什么不推

`OnConflict DoNothing` 命中时，`RowsAffected` 是 0。这说明这条 `event_id` 之前已经处理过，通知早就存在，用户可能也已经看过。

再推一次的直接后果是前端红点闪两下，体验上像 bug。更麻烦的是如果前端收到推送就清一次未读数，重复推送会让计数错乱。

一条规则：**幂等点之后的所有副作用，都要受同一个幂等判断保护**。

## 6. 第 4 步：在线状态

### 6.1 两个数字的含义

| 字段 | 含义 | 用途 |
| --- | --- | --- |
| `online_users` | 当前有至少一条连接的用户数 | 站点「当前 N 人在线」 |
| `online_conns` | 连接总数 | 排查「一个人开了几个标签页」、估算 goroutine 数 |

这两个数直接取自 `Hub` 的两层 map，读一次锁，没有额外开销。不需要往 Redis 里写在线集合，那样反而多一份可能不同步的数据。

### 6.2 增量修改：统计接口

handler 已经在 4.3 的 `ws.go` 里写好了，路由也加过了。这里只补 Swagger 注解，让接口出现在文档里。

**增量修改**：`internal/handler/ws.go` 的 `Stats` 方法上方。

```go
// Stats godoc
// @Summary      当前在线人数与连接数
// @Tags         realtime
// @Produce      json
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.onlineStatsResp}
// @Router       /ws/stats [get]
```

`Notifications` 那条不加注解。Swagger UI 画不出 WebSocket 握手，硬加一条 `@Router /ws/notifications [get]` 只会在页面上留一个点不动的按钮，反而让人以为坏了。这条接口的用法写进本文档比写进 Swagger 合适。

### 6.3 在线数不能当监控指标

`online_users` 只在当前进程内有效。多实例部署时，每个实例只知道自己那部分连接，三个实例各报 50，加起来不等于真实在线数。

还有一层：进程重启时这个数会瞬间归零再爬回来，用它的曲线做告警会产生大量假信号。

真要做全站在线统计，有两条路：所有实例连同一个 Redis，连接建立时 `ZADD` 带时间戳，断开时 `ZREM`，统计时按时间窗过滤；或者用一个中心化的连接网关。两条都不在本章范围。

现在这个接口的定位是**开发期和单机部署的自检工具**，写在代码注释里比藏在文档里更不容易被误用。

## 7. 第 5 步：装配与关停

### 7.1 参数爆炸与 Deps

原来的签名是：

```go
func New(db *gorm.DB, jwtManager *jwt.Manager, rdb *redis.Client) (*gin.Engine, error)
```

本章要加 `hub`，变成四个参数。后续若再加依赖，也只是往 `Deps` 结构体里加字段，调用点只补一个键值对，不会重蹈位置参数的覆辙。

六个位置参数的问题不在长短，在于**顺序错了编译器不报错**。`*gorm.DB` 和 `*redis.Client` 都是指针，写反了照样编译通过，运行时报 panic。参数越多，这种错误的概率越高。

所以本章顺手改成结构体。这是本章唯一一次破坏性改动，但它发生在正确的时机：参数刚好从三个变成四个，改动量还小。

### 7.2 增量修改：router

**增量修改**：`internal/router/router.go` 的签名与依赖校验部分。

```go
// Deps 是 router.New 需要的全部外部依赖。
// 参数涨到四个以后改用结构体：新增依赖不用改所有调用点，
// 也不会因为多传一个 hub 而去调整一长串参数顺序。
type Deps struct {
	DB    *gorm.DB
	JWT   *jwt.Manager
	Redis *redis.Client
	Hub   *realtime.Hub
}

// New 注册全部路由。
// 所有依赖都由 main 创建后传入，router 只负责组装。
func New(deps Deps) (*gin.Engine, error) {
	if deps.DB == nil {
		return nil, errors.New("router: database is required")
	}
	if deps.JWT == nil {
		return nil, errors.New("router: JWT manager is required")
	}
	if deps.Redis == nil {
		return nil, errors.New("router: Redis client is required")
	}
	if deps.Hub == nil {
		return nil, errors.New("router: realtime hub is required")
	}
```

**增量修改**：同一个函数里创建 handler 的那一段，把 `db`、`jwtManager`、`rdb` 全部换成 `deps.` 前缀，并加一行。

```go
	wsHandler := handler.NewWSHandler(deps.Hub, deps.JWT)
```

### 7.3 增量修改：main

**增量修改**：`cmd/server/main.go` 里创建 router 的那一段。

```go
	// Hub 先于 router 与 worker 创建：路由要用它接住新连接，
	// 消费者要用它找收件人的在线连接，两边共享同一个实例。
	hub := realtime.NewHub()

	r, err := router.New(router.Deps{DB: db, JWT: jwtManager, Redis: rdb, Hub: hub})
	if err != nil {
		log.Fatalf("注册路由失败: %v", err)
	}
```

**增量修改**：worker 的构造与关停。

```go
	notifier := worker.NewNotificationWorker(db, rdb, hub)
```

以及 shutdown goroutine 的第一行。

```go
	go func() {
		<-ctx.Done() // 收到关闭信号
		// WebSocket 连接在握手后就被 hijack 了，不算 http.Server 管的活跃请求，
		// Shutdown 不会关它们。必须自己先断，否则这些 goroutine 一直挂着。
		hub.CloseAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown server: %v", err)
		}
	}()
```

### 7.4 Shutdown 关不掉 WebSocket

这是本章第二个容易被忽略的知识点。

`http.Server.Shutdown` 的行为是：停止接受新连接，然后等所有**正在处理的请求**处理完。它的判断依据是 `net/http` 内部维护的活跃连接集合。

WebSocket 握手成功后，`Upgrader` 会调用 `http.ResponseWriter` 的 `Hijack()`，把底层的 TCP 连接从 HTTP 服务里摘出来，自己接管读写。摘出来之后，`http.Server` 就不知道这条连接的存在了，`Shutdown` 不会等它，也不会关它。

后果很具体：如果只调 `Shutdown`，进程收到 SIGTERM 后 HTTP 端口关了、worker 退出了，但那几条 WebSocket 连接的 goroutine 还在 `ReadMessage` 上阻塞着。`main` 函数里最后那句 `wg.Wait()` 不涉及它们，程序会直接走到 `defer sqlDB.Close()`，然后退出。退出时操作系统会回收 socket，所以表面上「关了」。

但在容器里，如果进程不退出（比如还有别的阻塞点），这些连接会一直挂着，健康检查看到端口已关，编排系统判定失败，反复重启。`hub.CloseAll()` 这一句就是消除这种不确定性。

顺序也有讲究：先 `CloseAll` 再 `Shutdown`。反过来的话，`Shutdown` 会先等等待中的普通请求，这段时间里 WebSocket 还活着，客户端可能正好在发消息，服务端还在处理，多出一次不必要的往来。

## 8. 单测与验收

### 8.1 完整文件：`internal/realtime/hub_test.go`

同目录、同包 `realtime`，文件名带 `_test` 后缀才会被 `go test` 收集。写到这里说明 2.2 与 3.2 都已落盘，否则测试压根编译不过。

```go
package realtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newTestClient 造一个不连真实 socket 的客户端，用来测登记与投递逻辑。
// send 容量按测试需要给：测慢客户端时故意给 1。
func newTestClient(h *Hub, userID uint, buf int) *Client {
	return &Client{
		hub:    h,
		userID: userID,
		send:   make(chan []byte, buf),
		closed: make(chan struct{}),
	}
}

func TestRegisterCountsUsersAndConns(t *testing.T) {
	h := NewHub()
	c1 := newTestClient(h, 7, 4)
	c2 := newTestClient(h, 7, 4)
	c3 := newTestClient(h, 9, 4)
	h.Register(c1)
	h.Register(c2)
	h.Register(c3)

	if got := h.OnlineUsers(); got != 2 {
		t.Fatalf("OnlineUsers = %d, want 2", got)
	}
	if got := h.OnlineConns(); got != 3 {
		t.Fatalf("OnlineConns = %d, want 3", got)
	}

	h.Unregister(c1)
	h.Unregister(c2)
	if got := h.OnlineUsers(); got != 1 {
		t.Fatalf("after unregister OnlineUsers = %d, want 1", got)
	}
}

func TestUnregisterTwiceIsSafe(t *testing.T) {
	h := NewHub()
	c := newTestClient(h, 1, 4)
	h.Register(c)
	h.Unregister(c)
	// 读泵退出和关停可能同时摘同一个连接，第二次不能 panic 也不能改计数。
	h.Unregister(c)
	if got := h.OnlineConns(); got != 0 {
		t.Fatalf("OnlineConns = %d, want 0", got)
	}
}

func TestPushReachesEveryConnOfUser(t *testing.T) {
	h := NewHub()
	c1 := newTestClient(h, 5, 4)
	c2 := newTestClient(h, 5, 4)
	other := newTestClient(h, 6, 4)
	h.Register(c1)
	h.Register(c2)
	h.Register(other)

	h.Push(5, PushMessage{Type: "notification.created", Data: "x"})

	for _, c := range []*Client{c1, c2} {
		select {
		case data := <-c.send:
			var got PushMessage
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Type != "notification.created" {
				t.Fatalf("type = %q", got.Type)
			}
		default:
			t.Fatal("message not delivered")
		}
	}
	select {
	case <-other.send:
		t.Fatal("message leaked to another user")
	default:
	}
}

func TestPushToOfflineUserDoesNothing(t *testing.T) {
	h := NewHub()
	// 收件人不在线时必须静默返回，不能阻塞、不能报错。
	h.Push(404, PushMessage{Type: "notification.created"})
	if got := h.OnlineUsers(); got != 0 {
		t.Fatalf("OnlineUsers = %d, want 0", got)
	}
}

func TestSlowClientGetsDropped(t *testing.T) {
	h := NewHub()
	slow := newTestClient(h, 3, 1)
	h.Register(slow)

	// 第一条塞满缓冲区，第二条发现满了，应当把连接踢掉并注销。
	h.Push(3, PushMessage{Type: "a"})
	h.Push(3, PushMessage{Type: "b"})

	if got := h.OnlineConns(); got != 0 {
		t.Fatalf("slow client not dropped, OnlineConns = %d", got)
	}
	select {
	case <-slow.closed:
	default:
		t.Fatal("slow client was not closed")
	}
}

// TestPushOverRealConnection 用真实 socket 走一遍升级、推送、关闭。
// 前面几个用例只验证内存里的 map 与 channel，这一条才验证 gorilla 那层的接线。
func TestPushOverRealConnection(t *testing.T) {
	h := NewHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := NewClient(h, conn, 9)
		h.Register(c)
		go c.WriteLoop()
		go c.ReadLoop()
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Dial 返回时服务端可能还没 Register，这里等一下，
	// 否则紧接着的 Push 会因为「用户不在线」被静默丢弃。
	deadline := time.Now().Add(2 * time.Second)
	for h.OnlineUsers() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("server never registered the connection")
		}
		time.Sleep(10 * time.Millisecond)
	}

	h.Push(9, PushMessage{Type: "notification.created", Data: "hello"})

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("unexpected payload: %s", data)
	}
}
```

最后一条测试里的等待循环值得单独说。`Dial` 返回表示握手完成，但服务端处理函数里 `Register` 与握手不是原子的。不等就直接 `Push`，会碰到「用户不在线」，测试随机失败。这种不稳定测试比没有测试更耗时间，因为你会怀疑代码有问题。

### 8.2 手工验收

三样东西准备：一个能发信号的浏览器控制台、两个账号、`wscat` 或任意 WebSocket 客户端。

**第一步**：拿 token。

```bash
curl -s -X POST http://127.0.0.1:8080/api/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"bob","password":"123456"}'
```

把返回的 `data.token` 记下来。

**第二步**：验证鉴权在升级之前。

```bash
curl -i "http://127.0.0.1:8080/api/ws/notifications"
```

期望 `HTTP/1.1 401`，响应体是 `{"code":...}`。如果返回 `101 Switching Protocols`，说明鉴权被跳过了，赶紧回头查。

**第三步**：连上。

```bash
wscat -c "ws://127.0.0.1:8080/api/ws/notifications?token=<bob 的 token>"
```

**第四步**：另开一个终端，用 alice 的 token 给 bob 的文章点赞。

```bash
curl -s -X PUT http://127.0.0.1:8080/api/articles/1/like \
  -H "Authorization: Bearer <alice 的 token>"
```

理论上 1 秒内 `wscat` 终端会跳出：

```json
{"type":"notification.created","data":{"id":12,"type":"like.created","article_id":1,"actor_id":2,"target_type":"article","target_id":1,"created_at":"2026-11-11T10:00:00Z"}}
```

评论走同一条链路。另开终端，用 alice 的 token 发一条评论：

```bash
curl -s -X POST http://127.0.0.1:8080/api/articles/1/comments \
  -H "Authorization: Bearer <alice 的 token>" \
  -H "Content-Type: application/json" \
  -d '{"content":"nice post"}'
```

`wscat` 会再收到一条。外层 `type` 不变，靠 `data.type` 区分。`target_type` 决定前端跳哪张表，`target_id` 是那张表的主键，`article_id` 只作列表展示用的文章上下文。

```json
{"type":"notification.created","data":{"id":13,"type":"comment.created","article_id":1,"actor_id":2,"target_type":"comment","target_id":34,"created_at":"2026-11-11T10:05:00Z"}}
```

两个容易判成「评论没走通知」的原因。

1. 用文章作者自己的账号评论。`resolveArticleAction` 里 `article.UserID == p.ActorID` 直接返回，点赞与评论都不发通知。换一个账号再试。
2. `data.id` 是 `notifications` 表主键，`target_id` 是目标对象主键。两者数值本来就不相等，看到对不上先确认在看哪个 id。

**第五步**：查在线数。

```bash
curl -s http://127.0.0.1:8080/api/ws/stats
```

**第六步**：关掉 `wscat`，再看一次在线数，应该少一个。再按 Ctrl+C 停服务，观察日志里有没有 `shutdown server` 之类的内容，进程应该干净退出。

### 8.3 勾选项

- [ ] C1 点赞后 1 秒内收到 `notification.created`
- [ ] C2 同一账号两个窗口都收到
- [ ] C3 无 token 返回 401，不是 101
- [ ] C4 `TestSlowClientGetsDropped` PASS
- [ ] C5 `online_conns` 与打开的连接数一致
- [ ] C6 Ctrl+C 后进程一次退出，不需要再按一次
- [ ] C7 自己给自己的文章点赞，不收到推送（04a 的 `article.UserID == p.ActorID` 分支仍然生效）

## 9. 边界与面试追问

### 9.1 多实例怎么办

这是本章最大的限制，必须主动说出来。

现在的 Hub 是进程内的 map。部署两台机器，alice 连到 A 机、bob 连到 B 机。alice 点赞 bob 的文章，事件的消费可能落在 A 机也可能落在 B 机的 worker 上。落在 A 机，A 的 Hub 里没有 bob，推送静默丢弃。

三种解法：

| 方案 | 做法 | 代价 |
| --- | --- | --- |
| Redis Pub/Sub 扇出 | 每个实例订阅 `ws:user:{id}`，收到就推本进程的连接 | 多一个订阅连接，Pub/Sub 不持久化，实例重启期间的推送会丢 |
| 按用户粘性路由 | 网关层按 userID 哈希，保证同一用户的所有连接落在同一实例 | 需要网关配合，扩容时迁移麻烦 |
| 外部连接服务 | 用 Centrifugo 这类专门的服务管连接 | 多一个组件，但省掉所有自研细节 |

学习项目取第一种最合理：`Hub.Push` 里先查本地，查不到就 `PUBLISH`。代码大约 20 行，能讲清「广播与定向」的区别。

本章不做，因为单机跑通了才有资格谈扇出，而且 04a 那套「数据库是真相源」的设计已经保证了多实例下数据不会错，只是推送延迟。这个取舍要在面试时讲明白，别让人以为没想到。

### 9.2 明确不做

| 未做 | 原因 |
| --- | --- |
| 多实例广播 | 见 9.1，单机优先 |
| 推送失败重试队列 | 推送是加速手段，不是数据通路，重试没有收益 |
| 客户端发消息（聊天室） | 本章只做服务端推；读泵目前只处理心跳，留了扩展位 |
| 消息序号与断点续推 | 需要服务端保存每连接的游标，收益低，重连后拉一次接口就够 |
| 二进制帧 | 只推 JSON，文本帧够用 |
| 压缩（permessage-deflate） | 单条消息几百字节，压缩比不划算，还多一层 CPU |
| 连接数上限与拒绝策略 | 生产需要按用户限连（比如一人最多 5 条），本章不做 |

### 9.3 面试追问

**问：为什么不用 SSE？**
答：SSE 更适合今天的场景，单向推送、纯 HTTP、自动重连都是现成的。选 WebSocket 是为下一步的双向能力留口子，代价是多写了心跳和读写分离。如果只做通知，SSE 是更省的选择，这一点我不回避。

**问：一条连接占多少资源？**
答：两个 goroutine，一个 1 KiB 读缓冲、一个 1 KiB 写缓冲，加一个容量 16 的字节通道。粗算每条连接 20 到 30 KB，主要来自 goroutine 栈。一万连接大约 200 到 300 MB，单机扛得住，但要调 `ulimit -n`。

**问：怎么发现连接已经断了？**
答：三处。读超时 60 秒没收到 pong 就断；写失败立刻断；对端发 close 帧也断。三处最后都走到 `ReadLoop` 或 `WriteLoop` 的 defer，调 `Unregister` 摘连接。

**问：客户端网络闪断，服务端多久知道？**
答：最坏 `pongWait` 60 秒。因为客户端消失时不会发 close 帧，只能靠心跳超时发现。要做到秒级，得把 `pongWait` 调小，代价是误判率上升。这是典型的「检测速度」与「误杀率」的权衡。

**问：推送能不能保证一定送到？**
答：不能，也没打算保证。能保证的是数据库里那行通知一定在。推送丢了，用户下次拉接口照样看得到。把可靠性放在持久层，实时层只做加速，这是这套设计的核心取舍。

**问：`Hub` 用了锁，会不会成为瓶颈？**
答：会，但量级差得远。一次 `Push` 持读锁的时间只够拷一遍该用户的连接列表，通常 1 到 3 个元素，纳秒级。真正的写入在锁外。实测瓶颈会先出现在 Redis 或 MySQL 上，轮不到这把锁。

**问：为什么 `Push` 里要调 `Unregister`，读泵的 defer 不是也会调？**
答：两处都可能先到。踢慢客户端时读泵可能还在阻塞读，`Close` 之后它才会退出。`Unregister` 做了重复调用保护，谁先谁后都对。

## 10. 本章勘误与进度快照

| 项 | 状态 |
| --- | --- |
| 代码落盘 | 本章的四个完整文件从文档正文原样抽出，落在干净项目副本上：逐字节一致、`gofmt -l` 对新增文件无输出、`go build ./...`、`go vet ./...`、`go test ./...` 全绿 |
| 单测 | `internal/realtime` 六个用例全 PASS，含真实 socket 一条 |
| 依赖版本 | `github.com/gorilla/websocket v1.5.3` |
| 破坏性改动 | `router.New` 签名改成 `New(Deps)`，`NewNotificationWorker` 多一个 `hub` 参数 |
| 04a 章影响 | 7.1 的 `main.go` 完整文件已过时，以本章 7.3 的增量与 07 章「启动链路」一节的最终版为准 |
| 未验证 | 多实例扇出、真实浏览器页面、Nginx 代理下的心跳续期、`go test -race`（本机 `CGO_ENABLED` 受限） |

## 11. 参考来源

- gorilla/websocket 官方仓库与 chat 示例：https://github.com/gorilla/websocket
- RFC 6455 The WebSocket Protocol：https://datatracker.ietf.org/doc/html/rfc6455
- `net/http` 关于 `Shutdown` 与 hijacked 连接的说明：https://pkg.go.dev/net/http#Server.Shutdown
- OWASP 跨站 WebSocket 劫持：https://owasp.org/www-community/attacks/Cross_Site_WebSocket_Hijacking
- MDN WebSocket API：https://developer.mozilla.org/en-US/docs/Web/API/WebSocket

**本套文档内部**：00 章「目标 API 合同」「数据与业务规则」「上线前总检查」，04a 章 5.2（`NotificationWorker` 的消费循环）、7.1（`signal.NotifyContext` 与 `WaitGroup`），04b 章 2.1（`main.go` 的通用注解块），05 章（测试与发布准备），06 章讲稿 6（本章的面试表达）。

> 下一步：进阶专题的测试与发布准备见 05 章，简历讲稿见 06 章。本章改动的 `NewNotificationWorker` 签名与 `router.Deps` 已稳定，先把 8.3 的勾选项跑完再往下。
