package notification

import (
	"context"
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

const notifyConsumer = "worker-1" // 消费者名，同一消费组内多个实例要各自不同

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

// Run 消费 Stream 直到 ctx 取消。
//
// 循环骨架（建组、补 PEL、取新消息、成功才 ACK、读失败退避）统一在
// event.Consumer 里，这里只声明「本 worker 是谁、从哪开始读、消息交给谁」。
//
// FromStart 为 true：通知是落库型消费者，组里记录的位置之外，
// Stream 里已有的历史也要补处理一遍，否则首次部署前产生的事件永远不会有通知。
// 重复投递由 notifications.event_id 唯一索引挡住，补读是安全的。
func (w *NotificationWorker) Run(ctx context.Context) {
	c := &event.Consumer{
		RDB:       w.rdb,
		Stream:    event.StreamKey,
		Group:     event.NotifyGroup,
		Name:      notifyConsumer,
		Handler:   w.handle,
		FromStart: true,
		LogTag:    "notification worker",
	}
	c.Run(ctx)
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

	handler, ok := notificationHandlers[p.Type]
	if !ok {
		// 没有注册的事件类型，通知消费端不处理。
		// 直接 ACK，避免未知类型永远留在 PEL 变成毒消息。
		slog.WarnContext(ctx, "no notification handler, ack event", "type", p.Type, "message_id", msg.ID)
		return nil
	}
	// resolver 只负责回答「这条事件该通知谁、点进去跳哪」，不碰落库。
	spec, err := handler.resolve(ctx, w.db, p)
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
