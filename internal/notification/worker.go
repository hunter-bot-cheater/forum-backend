package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"forum-backend/internal/event"
	"forum-backend/internal/model"
	"forum-backend/internal/realtime"
)

const (
	notifyConsumer = "worker-1"      // 消费者名，同一消费组内多个实例要各自不同
	readBlock      = 2 * time.Second // 等新消息时最多阻塞多久，顺便当作退出检查点
	readBatchSize  = 20              // 一次最多取多少条，控制单轮处理时长
)

// NotificationWorker 从消费组取事件，写成站内通知，再推给在线的收件人。
// 它是 outbox -> Stream -> 站内通知这条链路的最后一环。
type NotificationWorker struct {
	db  *gorm.DB
	rdb *redis.Client
	hub *realtime.Hub
}

// NewNotificationWorker 由装配层调用。hub 允许传 nil，方便只跑消费不推送的场景。
func NewNotificationWorker(db *gorm.DB, rdb *redis.Client, hub *realtime.Hub) *NotificationWorker {
	if hub == nil {
		// 构造函数保证 hub 不为 nil，调用方不必在每次推送前判断。
		hub = realtime.NewHub()
	}
	return &NotificationWorker{db: db, rdb: rdb, hub: hub}
}

// Run 先确保消费组存在，再循环处理 PEL 与新消息，ctx 取消后退出。
func (w *NotificationWorker) Run(ctx context.Context) {
	// 用 forum:notify:v1 消费组消费 forum:events:v1 这个 Stream。
	// 消费组或 Stream 不存在时会自动创建；已存在则返回 BUSYGROUP，
	// 那正是我们想要的状态，所以忽略这个错误。
	_ = w.rdb.XGroupCreateMkStream(ctx, event.StreamKey, event.NotifyGroup, "0").Err()

	for {
		if ctx.Err() != nil {
			return
		}
		// 每轮固定两个动作：
		// 1) 先读 "0"，把上次读走但没来得及 ACK 的 PEL 消息补处理掉（比如进程刚好被杀）。
		// 2) 再读 ">"，取从未投递给任何消费者的新消息。
		w.consume(ctx, "0")
		w.consume(ctx, ">")
	}
}

// consume 读一批消息，逐条处理，处理成功才 ACK。
// start 是 XReadGroup 的游标语义："0" 表示重读 PEL，">" 表示只要新消息。
func (w *NotificationWorker) consume(ctx context.Context, start string) {
	// 读 PEL 时不能阻塞：PEL 可能本来就是空的，阻塞两秒会白等。
	// go-redis v9 只有 Block 为正数时才发 BLOCK 参数，传 -1 就是立即返回。
	block := readBlock
	if start == "0" {
		block = -1
	}

	streams, err := w.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    event.NotifyGroup,
		Consumer: notifyConsumer,
		Streams:  []string{event.StreamKey, start},
		Count:    readBatchSize,
		Block:    block,
	}).Result()
	// redis.Nil 是「阻塞超时、没有新消息」，属于正常情况，不是错误。
	if errors.Is(err, redis.Nil) {
		return
	}
	if err != nil {
		if ctx.Err() != nil {
			return // 退出过程中断开连接，不当作故障
		}
		slog.ErrorContext(ctx, "read group", "from", start, "error", err)
		return
	}

	for _, stream := range streams {
		for _, msg := range stream.Messages {
			if err := w.handle(ctx, msg); err != nil {
				// 不 ACK，消息留在 PEL，下一轮用 "0" 重新读到，相当于自动重试。
				slog.ErrorContext(ctx, "handle event", "message_id", msg.ID, "error", err)
				continue
			}
			if err := w.rdb.XAck(ctx, event.StreamKey, event.NotifyGroup, msg.ID).Err(); err != nil {
				slog.ErrorContext(ctx, "ack event", "message_id", msg.ID, "error", err)
			}
		}
	}
}

// handle 把一条事件变成一条通知。重复事件由唯一索引挡住。
// 事件类型决定用哪个 resolver，落库与推送对所有类型一视同仁。
func (w *NotificationWorker) handle(ctx context.Context, msg redis.XMessage) error {
	raw, ok := msg.Values["payload"].(string)
	if !ok {
		return fmt.Errorf("event %s: payload is not a string", msg.ID)
	}
	p, err := event.Decode(raw)
	if err != nil {
		return err
	}

	resolve, ok := resolverByType[p.Type]
	if !ok {
		// 没有注册 resolver 的事件，通知消费端不处理。
		// 直接 ACK，避免未知类型永远留在 PEL 变成毒消息。
		slog.WarnContext(ctx, "no notification resolver, ack event", "type", p.Type, "message_id", msg.ID)
		return nil
	}
	// resolver 只负责回答「这条事件该通知谁、点进去跳哪」，不碰落库。
	spec, err := resolve(ctx, w.db, p)
	if err != nil {
		return err
	}
	if spec == nil {
		// resolver 明确说这条事件不产生通知，比如自己给自己点赞。
		return nil
	}

	notice := &model.Notification{
		UserID:     spec.UserID,
		EventID:    p.EventID,
		ActorID:    p.ActorID,
		Type:       spec.Type,
		ArticleID:  spec.ArticleID,
		TargetType: spec.TargetType,
		TargetID:   spec.TargetID,
	}

	// 以 event_id 唯一索引做幂等：两个消费者同时拿到同一条消息时，
	// InnoDB 会在索引行上加锁，第一个插入成功，第二个走 DoNothing 静默跳过。
	res := w.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "event_id"}},
		DoNothing: true,
	}).Create(notice)
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
