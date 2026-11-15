package notification

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/event"
	"forum-backend/internal/model"
)

const (
	relayInterval      = 200 * time.Millisecond // 轮询间隔，越短延迟越低、空扫越多
	relayBatchSize     = 100                    // 每次最多搬多少条，防止一轮占满 MySQL 和 Redis
	maxPublishAttempts = 5                      // 投递失败超过这个次数就置 failed，交给人工
)

// Relay 把 outbox 里 pending 的事件投进 Redis Stream。
//
// 它是「事务发件箱」的后半段：业务侧只管把事件写进 MySQL（和业务数据同一事务），
// 这里负责把落库的事件搬出去。搬失败就留在 pending，下一轮再试。
type Relay struct {
	db  *gorm.DB
	rdb *redis.Client
}

// NewRelay 由装配层调用，之后把返回值交给 Run 在独立 goroutine 里跑。
func NewRelay(db *gorm.DB, rdb *redis.Client) *Relay {
	return &Relay{db: db, rdb: rdb}
}

// Run 按固定间隔扫表，ctx 取消后退出，由 main 的 WaitGroup 等它收尾。
// 它不返回错误：单轮失败只打日志，因为下一轮还会重试同一批 pending 事件。
func (r *Relay) Run(ctx context.Context) {
	// ticker 每 200ms 往 C 里塞一个时间点，select 收到就触发一轮搬运。
	ticker := time.NewTicker(relayInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.PublishBatch(ctx); err != nil {
				slog.ErrorContext(ctx, "relay publish batch", "error", err)
			}
		}
	}
}

// PublishBatch 一次最多投 relayBatchSize 条。
// 单独导出成方法，是为了能在测试里不启动 goroutine 就调用它。
func (r *Relay) PublishBatch(ctx context.Context) error {
	// 按 id 正序取，保证先进 outbox 的事件先进 Stream（同一条业务链上的顺序不乱）。
	events := make([]model.OutboxEvent, 0)
	err := r.db.WithContext(ctx).
		Where("status = ?", model.OutboxPending).
		Order("id ASC").Limit(relayBatchSize).
		Find(&events).Error
	if err != nil {
		return fmt.Errorf("load pending events: %w", err)
	}

	for _, e := range events {
		// 一条事件对应一次 XADD，Redis 消息体只放 event_id / type / payload 三个字段。
		_, err := r.rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: event.StreamKey,
			Values: map[string]interface{}{
				"event_id": e.EventID,
				"type":     e.EventType,
				"payload":  e.Payload,
			},
		}).Result()
		if err != nil {
			// XADD 是单条网络请求，一条失败不影响后面的，标记失败后继续处理下一条。
			slog.ErrorContext(ctx, "relay xadd", "event_id", e.EventID, "error", err)
			r.markFailure(ctx, e)
			continue
		}

		// 投递成功才把 MySQL 里的状态改成 published，这样重复扫描不会把同一条重投。
		err = r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
			Where("id = ?", e.ID).
			Update("status", model.OutboxPublished).Error
		if err != nil {
			// 这条 Update 失败通常意味着 MySQL 连接断了或 ctx 被取消，
			// 继续循环只会一路报错，直接返回让下一轮重来。
			return fmt.Errorf("mark published %s: %w", e.EventID, err)
		}
	}
	return nil
}

// markFailure 累加失败次数，到上限后把事件留在 failed 状态等待排查。
// 这里的选择是「最多重试 5 次」，不做无限重试：毒消息一直占着 pending 会拖慢每一轮扫描。
func (r *Relay) markFailure(ctx context.Context, e model.OutboxEvent) {
	updates := map[string]interface{}{
		"attempts": gorm.Expr("attempts + 1"), // 在数据库侧自增，避免并发覆盖计数
	}
	if e.Attempts+1 >= maxPublishAttempts {
		// 置 failed 之后，PublishBatch 的 WHERE status='pending' 就再也扫不到它。
		updates["status"] = model.OutboxFailed
	}
	err := r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("id = ?", e.ID).Updates(updates).Error
	if err != nil {
		slog.ErrorContext(ctx, "mark outbox failure", "event_id", e.EventID, "error", err)
	}
}
