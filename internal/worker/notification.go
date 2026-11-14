package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"forum-backend/internal/event"
	"forum-backend/internal/model"
	"forum-backend/internal/realtime"
)

const (
	notifyConsumer = "worker-1"
	readBlock      = 2 * time.Second
	readBatchSize  = 20
)

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

// Run 先确保消费组存在，再循环处理 PEL 与新消息。
func (w *NotificationWorker) Run(ctx context.Context) {
	// 重复启动会返回 BUSYGROUP，组已存在正是我们要的状态，忽略即可。
	_ = w.rdb.XGroupCreateMkStream(ctx, event.StreamKey, event.NotifyGroup, "0").Err()
	//用 forum:notify:v1 这个消费组，来消费 forum:events:v1 这个队列里的消息。如果这个队列或消费组还不存在，会自动创建它们
	for {
		if ctx.Err() != nil {
			return
		}
		//PEL极致 待处理消息队列
		w.consume(ctx, "0") // 先补上次没 ACK 的 //消息 100 已经被 Redis 标记为“已分配给这个消费者”，
		// 但消费者还没来得及给它发 ACK  消息 100 就变成了 Pending（待处理）状态，躺在 PEL 列表里
		w.consume(ctx, ">") // 再阻塞读新消息
	}
}

// consume 读一批消息，逐条处理，成功才 ACK。
func (w *NotificationWorker) consume(ctx context.Context, start string) {
	block := readBlock
	if start == "0" {
		// go-redis v9 里只有 Block >= 0 才发 BLOCK 参数。
		// 传 -1 让这次读立即返回，不阻塞循环。
		block = -1
	}
	streams, err := w.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    event.NotifyGroup,
		Consumer: notifyConsumer,
		Streams:  []string{event.StreamKey, start},
		Count:    readBatchSize,
		Block:    block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("read group from %s: %v", start, err)
		return
	}

	for _, stream := range streams {
		for _, msg := range stream.Messages {
			if err := w.handle(ctx, msg); err != nil {
				// 不 ACK，消息留在 PEL，下一轮用 "0" 重新读到。
				log.Printf("handle event %s: %v", msg.ID, err)
				continue
			}
			if err := w.rdb.XAck(ctx, event.StreamKey, event.NotifyGroup, msg.ID).Err(); err != nil {
				log.Printf("ack event %s: %v", msg.ID, err)
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
		log.Printf("no notification resolver for %s, ack event %s", p.Type, msg.ID)
		return nil
	}
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
	res := w.db.WithContext(ctx).Clauses(clause.OnConflict{ //告诉 GORM，我要处理“冲突”情况
		Columns:   []clause.Column{{Name: "event_id"}}, //根据 event_id 这个字段来判断是否冲突的
		DoNothing: true,                                //如果发生了冲突（唯一键重复），就假装什么都没发生，静默跳过，不要报错
	}).Create(notice)
	//个消费者（不同机器）同一瞬间拿到了同一条消息，同时执行插入。
	// MySQL 的 InnoDB 会在行级别加锁，第一个插入成功，第二个被拦截并静默忽略
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
