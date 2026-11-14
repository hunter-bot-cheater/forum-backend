// Package notification 放站内通知相关的 handler、service 与后台任务：
// 把 outbox 事件投进 Redis Stream、消费事件生成站内通知并推送给在线收件人。
package notification

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/event"
	"forum-backend/internal/model"
)

const (
	relayInterval       = 200 * time.Millisecond  //轮询间隔 200ms
	relayBatchSize      = 100 //每次最多搬100条
	maxPublishAttempts  = 5 //最大重试次数
)

// Relay 把 outbox 里 pending 的事件投进 Redis Stream。
type Relay struct {
	db  *gorm.DB
	rdb *redis.Client
}

func NewRelay(db *gorm.DB, rdb *redis.Client) *Relay {
	return &Relay{db: db, rdb: rdb}
}

// Run 按固定间隔扫表，ctx 取消后退出，由 main 的 WaitGroup 等它收尾。
func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(relayInterval)  //创建一个定时器 每隔 200 毫秒，
	//它就会往 ticker.C 这个管道（Channel）里滴答扔进一个时间对象
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done(): //系统取消信号
			return
		case <-ticker.C://闹钟信号（200 毫秒到了）
			if err := r.PublishBatch(ctx); err != nil {
				log.Printf("relay publish batch: %v", err)
			}
		}
	}
}

// PublishBatch 一次最多投 relayBatchSize 条。
// 单独导出成方法，是为了能在测试里不启动 goroutine 就调用它。
func (r *Relay) PublishBatch(ctx context.Context) error {
	events := make([]model.OutboxEvent, 0)
	err := r.db.WithContext(ctx).
		Where("status = ?", model.OutboxPending).
		Order("id ASC").Limit(relayBatchSize). //每次只拿 100 条 按 ID 正序拿。保证事件先进先出（FIFO）
		Find(&events).Error
	if err != nil {
		return fmt.Errorf("load pending events: %w", err)
	}

	for _, e := range events {
		_, err := r.rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: event.StreamKey,
			Values: map[string]interface{}{
				"event_id": e.EventID,
				"type":     e.EventType,
				"payload":  e.Payload,
			},
		}).Result()
		if err != nil {
			log.Printf("relay xadd %s: %v", e.EventID, err)
			r.markFailure(ctx, e)
			continue //XAdd 是单条网络请求   跳过这一条，后面的消息大概率能正常发
		}
		err = r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
			Where("id = ?", e.ID).
			Update("status", model.OutboxPublished).Error //推入 Redis Stream 成功后，立刻将 MySQL 里的状态改为 published
		if err != nil {
			return fmt.Errorf("mark published %s: %w", e.EventID, err)  //如果 Update 报错，意味着整个 MySQL 连接不可用，或者 ctx 被取消了 需要立刻返回
		}
	}
	return nil
}

// markFailure 累加失败次数，到上限后把事件留在 failed 状态等待排查。
func (r *Relay) markFailure(ctx context.Context, e model.OutboxEvent) {
	updates := map[string]interface{}{
		"attempts": gorm.Expr("attempts + 1"),
	}
	if e.Attempts+1 >= maxPublishAttempts {
		updates["status"] = model.OutboxFailed  //把状态改成 failed。一旦变成 failed，
		//PublishBatch 里的 Where("status = ?", model.OutboxPending) 就查不到它
	}
	err := r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("id = ?", e.ID).Updates(updates).Error
	if err != nil {
		log.Printf("mark outbox failure %s: %v", e.EventID, err)
	}
}
