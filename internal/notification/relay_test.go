package notification

import (
	"context"
	"fmt"
	"testing"

	"github.com/redis/go-redis/v9"

	"forum-backend/internal/event"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
)

// TestRelayTrimsStream 验证每次投递都顺手把 Stream 裁回上限。
//
// 这条用例存在的原因：Stream 不会自己回收已 ACK 的消息，裁剪只发生在 XADD 上。
// 一旦有人把 MaxLen 去掉，接口行为不会有任何变化，只有 Redis 内存慢慢涨，
// 几个月后才发现——所以必须有一个用例把「长度不再无限增长」钉死。
func TestRelayTrimsStream(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	ctx := context.Background()

	// 先把 Stream 填到超过上限，模拟长期运行后的堆积。
	// 这里刻意不带 MAXLEN：预填阶段就把长度压在上限内的话，
	// 后面那次投递裁不裁都看不出来，用例会变成永远通过。
	// 用 Pipeline 一次发完，一万多条逐个往返要几十秒。
	// XLen 返回 int64，预填数也跟着用 int64，省掉后面每处比较都要转换。
	filled := int64(relayStreamMaxLen + 200)
	pipe := rdb.Pipeline()
	for i := int64(0); i < filled; i++ {
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: event.StreamKey,
			Values: map[string]any{"event_id": fmt.Sprintf("old-%d", i)},
		})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatalf("预填 Stream 失败: %v", err)
	}

	// 一条 pending 事件：relay 处理它时发出的那次 XADD 才是被测对象。
	e := &model.OutboxEvent{
		EventID:       "relay-trim-check",
		EventType:     event.TypeLikeCreated,
		AggregateType: "article",
		AggregateID:   1,
		Payload:       "{}",
		Status:        model.OutboxPending,
	}
	if err := db.Create(e).Error; err != nil {
		t.Fatalf("造 pending 事件失败: %v", err)
	}

	if err := NewRelay(db, rdb).PublishBatch(ctx); err != nil {
		t.Fatalf("投递失败: %v", err)
	}

	// 先确认这一条真的投进去了，否则下面的长度断言测的是「什么都没写」。
	// 用倒序取最后一条：MAXLEN 裁剪删的是头部，新消息一定在尾部。
	last, err := rdb.XRevRangeN(ctx, event.StreamKey, "+", "-", 1).Result()
	if err != nil {
		t.Fatalf("读取 Stream 尾部失败: %v", err)
	}
	if len(last) != 1 || last[0].Values["event_id"] != e.EventID {
		t.Fatalf("Stream 尾部不是刚投递的事件: %+v", last)
	}

	length, err := rdb.XLen(ctx, event.StreamKey).Result()
	if err != nil {
		t.Fatalf("读取 Stream 长度失败: %v", err)
	}
	if length > filled {
		t.Fatalf("Stream 长度 %d 超过预填的 %d，说明投递没有带 MAXLEN", length, filled)
	}
	// 下界也要卡：万一 MaxLen 传成了 0 或很小，长度会被削到几乎为空，
	// 那等于把还没消费的消息一起丢了，同样是错的。
	if length < int64(relayStreamMaxLen) {
		t.Fatalf("Stream 长度 %d 低于上限 %d，裁剪过度", length, relayStreamMaxLen)
	}
}
