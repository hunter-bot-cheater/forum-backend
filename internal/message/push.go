package message

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/event"
	"forum-backend/internal/model"
	"forum-backend/internal/realtime"
	"forum-backend/internal/user"
)

const (
	// pushTypeMessageCreated 是推给前端的消息类型。
	// 前端按它分发：message.created 追加气泡，notification.created 更新红点。
	pushTypeMessageCreated = "message.created"

	pushReadBlock = 2 * time.Second // 等新事件时最多阻塞多久，顺便当退出检查点
	pushBatchSize = 20              // 一次最多取多少条，控制单轮处理时长
)

// PushWorker 把 message.created 事件推给收件人的在线连接。
//
// 为什么不发完消息直接 hub.Push：
//   - 消息已经落库，推送是锦上添花，不该让推送失败影响发送结果；
//   - 进程重启会丢掉内存里的推送，走 Stream 至少还有一次补投的机会；
//   - 多实例部署时，直推只能覆盖连在同一台机器上的收件人。
//
// 消费组按「实例」切分（组名带实例标识），而不是所有实例共用一个组：
// 消费组的语义是「一条消息只投给组内一个消费者」，共用组会让同一条私信
// 只有一台机器收到，其余机器上在线的收件人永远等不到实时推送。
// 每个实例一个组，等于每个实例各收一份，各自推给本机连接。
type PushWorker struct {
	db       *gorm.DB
	rdb      *redis.Client
	hub      *realtime.Hub
	users    *user.UserService
	group    string // 消费组名，每个实例不同
	consumer string // 组内消费者名，同一实例内唯一
}

// NewPushWorker 由装配层调用一次，之后把返回值交给 Run 在独立 goroutine 里跑。
// hub 允许传 nil，方便只验证事件消费、不关心推送的场景。
func NewPushWorker(db *gorm.DB, rdb *redis.Client, hub *realtime.Hub, users *user.UserService) *PushWorker {
	if hub == nil {
		// 构造函数兜住 nil，Run 里不用每次判空。
		hub = realtime.NewHub()
	}
	id := instanceID()
	return &PushWorker{
		db:       db,
		rdb:      rdb,
		hub:      hub,
		users:    users,
		group:    event.MessageGroup + ":" + id,
		consumer: id,
	}
}

// Run 建好消费组后循环消费，ctx 取消后退出，由 main 的 WaitGroup 等它收尾。
func (w *PushWorker) Run(ctx context.Context) {
	// "$" 表示只接收本组创建之后产生的事件。
	// 推送是尽力而为：用户不在线就跳过，他下次打开聊天页会从 MySQL 拉完整历史。
	// 用 "0" 的代价是每次部署都把整个 Stream 重放一遍，把几天前的消息重新推给在线用户。
	_ = w.rdb.XGroupCreateMkStream(ctx, event.StreamKey, w.group, "$").Err()

	for {
		if ctx.Err() != nil {
			return
		}
		// 先补处理 PEL（上次读走但没 ACK 的），再取新消息。
		w.consume(ctx, "0")
		w.consume(ctx, ">")
	}
}

// consume 读一批消息逐条处理，成功才 ACK。
// start 为 "0" 表示重读 PEL，为 ">" 表示只要从未投递给本组的新消息。
func (w *PushWorker) consume(ctx context.Context, start string) {
	// 读 PEL 时 PEL 可能本来就是空的，阻塞两秒纯属白等，传 -1 立即返回。
	block := pushReadBlock
	if start == "0" {
		block = -1
	}

	streams, err := w.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    w.group,
		Consumer: w.consumer,
		Streams:  []string{event.StreamKey, start},
		Count:    pushBatchSize,
		Block:    block,
	}).Result()
	// redis.Nil 是「阻塞超时、没有新消息」，属于正常情况。
	if errors.Is(err, redis.Nil) {
		return
	}
	if err != nil {
		if ctx.Err() != nil {
			return // 退出过程中断开连接，不当作故障
		}
		slog.ErrorContext(ctx, "message push read group", "from", start, "error", err)
		return
	}

	for _, stream := range streams {
		for _, msg := range stream.Messages {
			if err := w.handle(ctx, msg); err != nil {
				// 不 ACK，消息留在 PEL 下一轮重试。
				// 重推同一条消息只会让前端多收一帧，前端按 message.id 去重即可；
				// 反过来「ACK 了但没推」会让在线用户看不到这条，必须重进页面才发现。
				slog.ErrorContext(ctx, "message push handle", "message_id", msg.ID, "error", err)
				continue
			}
			if err := w.rdb.XAck(ctx, event.StreamKey, w.group, msg.ID).Err(); err != nil {
				slog.ErrorContext(ctx, "message push ack", "message_id", msg.ID, "error", err)
			}
		}
	}
}

// handle 把一条事件变成一次 WebSocket 推送。
//
// 推送内容里带发送者资料与收件人在该会话的未读数：
// 前端收到就能直接追加气泡并更新红点，不用再发一次请求。
func (w *PushWorker) handle(ctx context.Context, msg redis.XMessage) error {
	raw, ok := msg.Values["payload"].(string)
	if !ok {
		return fmt.Errorf("event %s: payload is not a string", msg.ID)
	}
	p, err := event.Decode(raw)
	if err != nil {
		return err
	}

	// 同一条 Stream 上还跑着点赞、评论、搜索同步等事件，不归这边处理。
	// 直接返回 nil 让上层 ACK，避免它们一直留在 PEL 里变成毒消息。
	if p.Type != event.TypeMessageCreated || p.Created == nil || p.Created.Type != event.TargetMessage {
		return nil
	}

	// 只取推送要用的列。私信正文可能上千字，Select 限定列是为了
	// 不把整行搬进内存（和文章列表 preloadAuthor 的想法一致）。
	var m model.Message
	err = w.db.WithContext(ctx).
		Select("id", "conversation_id", "sender_id", "receiver_id", "content", "client_msg_id", "created_at").
		First(&m, p.Created.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 消息已不存在（比如以后有了撤回），重试没有意义。
		return nil
	}
	if err != nil {
		return fmt.Errorf("load message %d: %w", p.Created.ID, err)
	}

	var conv model.Conversation
	err = w.db.WithContext(ctx).
		Select("id", "user_a_id", "user_b_id", "unread_a", "unread_b").
		First(&conv, m.ConversationID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load conversation %d: %w", m.ConversationID, err)
	}

	payload := messagePushPayload{
		Message: toMessageResp(MessageItem{Message: m, Mine: false}),
		Unread:  unreadOf(&conv, m.ReceiverID),
	}

	// 发送者资料：前端要用头像昵称画气泡。
	// 查不到说明账号已删，消息照推，只是不带 sender（前端显示「用户已注销」）。
	briefs, err := w.users.Briefs(ctx, []uint{m.SenderID})
	if err != nil {
		return err
	}
	if sender, ok := briefs[m.SenderID]; ok {
		resp := user.NewBriefResp(sender)
		payload.Sender = &resp
	}

	// 推送是尽力而为：收件人不在线、连接刚好断开都不影响结果，也不该回滚或重投。
	w.hub.Push(m.ReceiverID, realtime.PushMessage{
		Type: pushTypeMessageCreated,
		Data: payload,
	})
	return nil
}

// messagePushPayload 是推给前端的私信内容。
//
// Message 直接复用 REST 的 messageResp：前端一套渲染逻辑能吃两种来源，
// 不会出现「历史消息一个形状、实时消息另一个形状」的对不上。
// Unread 是收件人在这个会话里的未读数，由服务端算好的权威值，
// 前端拿它更新列表红点，比自己在本地 +1 更可靠（多标签页也不会算重）。
type messagePushPayload struct {
	Message messageResp     `json:"message"`
	Sender  *user.BriefResp `json:"sender,omitempty"`
	Unread  int             `json:"unread"`
}

// instanceID 返回当前实例的标识，用作消费组名与消费者名。
//
// 用主机名而不是进程号：容器/Pod 重启后主机名不变，消费组能接上，
// 不会每次重启都留下一个以 pid 命名的空组。
// 拿不到主机名时退回 pid，此时组名会随重启变化，历史组需要用
// XGROUP DESTROY 手工清理（单实例部署没有这个问题）。
func instanceID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return strconv.Itoa(os.Getpid())
}
