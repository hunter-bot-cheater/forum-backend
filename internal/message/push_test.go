package message

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"forum-backend/internal/event"
	"forum-backend/internal/model"
	"forum-backend/internal/realtime"
	"forum-backend/internal/user"
)

// dialWS 起一个只负责升级并登记连接的测试 WS 服务，返回客户端那一侧。
//
// 为什么不直接 new 一个 Client 读它的 send channel：那个 channel 不导出，
// 只能从 realtime 包内部看。走一次真实的 WebSocket 握手反而更接近线上，
// 顺带把「推送 JSON 能被前端解出来」这件事一起验证了。
func dialWS(t *testing.T, hub *realtime.Hub, userID uint) *websocket.Conn {
	t.Helper()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := realtime.NewClient(hub, conn, userID)
		hub.Register(c)
		go c.WriteLoop()
		// ReadLoop 会阻塞到连接断开，退出时自己从 hub 摘掉。
		c.ReadLoop()
	}))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("连接测试 WS 失败: %v", err)
	}
	// 注册顺序在 srv.Close 之后，Cleanup 是后进先出，所以连接先关、服务器后关，
	// 否则 srv.Close 会等 ReadLoop 退出而卡住。
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// pushedMessage 是测试侧对推送帧的视图。
type pushedMessage struct {
	Type string `json:"type"`
	Data struct {
		Message struct {
			ID          uint   `json:"id"`
			SenderID    uint   `json:"sender_id"`
			Content     string `json:"content"`
			ClientMsgID string `json:"client_msg_id"`
			Mine        bool   `json:"mine"`
		} `json:"message"`
		Sender *struct {
			ID       uint   `json:"id"`
			Username string `json:"username"`
			Nickname string `json:"nickname"`
		} `json:"sender"`
		Unread int `json:"unread"`
	} `json:"data"`
}

// TestPushWorkerPushesToReceiver 验证收件人的在线连接能收到刚发的私信。
//
// 走的是完整链路里除 Redis 之外的部分：真实消息落库 -> 事务里写的 outbox 事件
// -> worker.handle -> hub -> 真实 WebSocket 帧。Redis 那一段只负责搬运，
// 单独在 event/relay 里测。
func TestPushWorkerPushesToReceiver(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	hub := realtime.NewHub()
	// 收件人（bob）在线；发送者（alice）不连，用来确认推送只发给收件人。
	conn := dialWS(t, hub, f.bob.ID)
	// rdb 传 nil：handle 只用 db、hub 和 users，不碰 Redis。
	worker := NewPushWorker(f.db, nil, hub, user.NewUserService(f.db))

	msg, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "实时送达", "k-push")
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	var outbox model.OutboxEvent
	if err := f.db.Where("event_type = ?", event.TypeMessageCreated).First(&outbox).Error; err != nil {
		t.Fatalf("没找到 message.created 事件: %v", err)
	}

	if err := worker.handle(ctx, redis.XMessage{
		ID:     "1-0",
		Values: map[string]interface{}{"payload": outbox.Payload},
	}); err != nil {
		t.Fatalf("handle 失败: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var got pushedMessage
	if err := conn.ReadJSON(&got); err != nil {
		t.Fatalf("读取推送帧失败: %v", err)
	}

	if got.Type != pushTypeMessageCreated {
		t.Errorf("type = %q，期望 %q", got.Type, pushTypeMessageCreated)
	}
	if got.Data.Message.ID != msg.ID || got.Data.Message.Content != "实时送达" {
		t.Errorf("message = %+v，期望 id=%d content=实时送达", got.Data.Message, msg.ID)
	}
	// 对收件人来说这条不是自己发的，Mine 必须是 false，气泡才画在左边。
	if got.Data.Message.Mine {
		t.Error("收件人收到的消息 Mine 应当为 false")
	}
	// 前端拿 client_msg_id 做去重：重投时同一条不会被追加两次。
	if got.Data.Message.ClientMsgID != "k-push" {
		t.Errorf("client_msg_id = %q，期望 k-push", got.Data.Message.ClientMsgID)
	}
	if got.Data.Sender == nil || got.Data.Sender.ID != f.alice.ID || got.Data.Sender.Username != "alice" {
		t.Errorf("sender = %+v，期望 alice", got.Data.Sender)
	}
	// 未读数是服务端算的权威值，前端直接拿来更新红点。
	if got.Data.Unread != 1 {
		t.Errorf("unread = %d，期望 1", got.Data.Unread)
	}
}

// TestPushWorkerIgnoresOtherEvents 验证同一条 Stream 上的其它事件被静默跳过。
//
// 点赞、评论、搜索同步都跑在 forum:events:v1 上。私信推送如果对它们有反应，
// 用户会在聊天页看到莫名其妙的气泡。
func TestPushWorkerIgnoresOtherEvents(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	hub := realtime.NewHub()
	conn := dialWS(t, hub, f.bob.ID)
	worker := NewPushWorker(f.db, nil, hub, user.NewUserService(f.db))

	other := event.Payload{
		EventID: "e-1",
		Type:    event.TypeLikeCreated,
		ActorID: f.alice.ID,
		Target:  event.Target{Type: event.TargetArticle, ID: 1},
	}
	raw, err := other.Encode()
	if err != nil {
		t.Fatalf("编码事件失败: %v", err)
	}

	if err := worker.handle(ctx, redis.XMessage{
		ID:     "2-0",
		Values: map[string]interface{}{"payload": raw},
	}); err != nil {
		t.Fatalf("非私信事件应当被静默跳过，实际报错: %v", err)
	}

	// 没有推送时读取会超时，这就是期望结果。
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Error("点赞事件不该推给私信收件人")
	}
}

// TestPushWorkerOfflineReceiver 验证收件人不在线时不报错。
//
// 推送是尽力而为：他下次打开聊天页会从 MySQL 拉到完整历史。
// 这里如果返回错误，消息会一直留在 PEL 里反复重试，直到被人工处理。
func TestPushWorkerOfflineReceiver(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	msg, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "没人在线", "")
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	var outbox model.OutboxEvent
	if err := f.db.Where("event_type = ?", event.TypeMessageCreated).First(&outbox).Error; err != nil {
		t.Fatalf("没找到 message.created 事件: %v", err)
	}

	worker := NewPushWorker(f.db, nil, realtime.NewHub(), user.NewUserService(f.db))
	if err := worker.handle(ctx, redis.XMessage{
		ID:     "3-0",
		Values: map[string]interface{}{"payload": outbox.Payload},
	}); err != nil {
		t.Fatalf("收件人不在线不该报错: %v", err)
	}
	if msg.ID == 0 {
		t.Fatal("消息应当已经落库")
	}
}
