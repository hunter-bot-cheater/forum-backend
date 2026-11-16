package event

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"forum-backend/internal/testutil"
)

// 测试用的 Stream 与消费组名，和线上常量分开，避免读错键。
const (
	testStream = "test:events:v1"
	testGroup  = "test:consumer:v1"
)

// newTestConsumer 造一个只负责把消息交给 fn 的消费者。
// Batch 显式给 10：默认值在别的地方也要用，测试里写死更好定位问题。
func newTestConsumer(rdb *redis.Client, fn func(ctx context.Context, msg redis.XMessage) error) *Consumer {
	return &Consumer{
		RDB:     rdb,
		Stream:  testStream,
		Group:   testGroup,
		Name:    "test-worker",
		Handler: fn,
		Batch:   10,
		Block:   time.Second,
		LogTag:  "test",
	}
}

// addEvent 往 Stream 里塞一条事件，payload 用固定字符串。
// 内容本身不影响消费行为，消费端只关心有没有取到、有没有 ACK。
func addEvent(t *testing.T, rdb *redis.Client) string {
	t.Helper()

	id, err := rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: testStream,
		Values: map[string]any{"payload": "{}"},
	}).Result()
	if err != nil {
		t.Fatalf("写入测试事件失败: %v", err)
	}
	return id
}

// pendingCount 返回组内还没被 ACK 的消息数。
// 用 XPending 而不是看 Stream 长度：ACK 不会删消息，Stream 长度永远是 1。
func pendingCount(t *testing.T, rdb *redis.Client) int64 {
	t.Helper()

	pending, err := rdb.XPending(context.Background(), testStream, testGroup).Result()
	if err != nil {
		t.Fatalf("查询 PEL 失败: %v", err)
	}
	return pending.Count
}

// TestConsumerAcksHandledMessage 验证处理成功的消息会被 ACK。
//
// 不 ACK 的消息会一直留在 PEL 里被重复投递：用户会反复收到同一条通知，
// 而且 PEL 只增不减，久了会把 Redis 内存吃满。
func TestConsumerAcksHandledMessage(t *testing.T) {
	rdb := testutil.NewRedis(t)
	ctx := context.Background()

	// 先建组再写消息：组不存在时 XREADGROUP 会报 NOGROUP。
	if err := rdb.XGroupCreateMkStream(ctx, testStream, testGroup, "0").Err(); err != nil {
		t.Fatalf("建消费组失败: %v", err)
	}
	wantID := addEvent(t, rdb)

	var gotID string
	c := newTestConsumer(rdb, func(_ context.Context, msg redis.XMessage) error {
		gotID = msg.ID
		return nil
	})

	// 直接调 drain 而不是起 Run：避免依赖阻塞读的行为，
	// 也让「读一批、处理、ACK」这三步的断言是确定性的。
	if ok := c.drain(ctx, ">", -1); !ok {
		t.Fatal("读取应当成功")
	}
	if gotID != wantID {
		t.Fatalf("处理的消息 id = %q，期望 %q", gotID, wantID)
	}
	if n := pendingCount(t, rdb); n != 0 {
		t.Fatalf("处理成功后 PEL 应当为空，实际还有 %d 条", n)
	}
}

// TestConsumerKeepsMessageOnHandlerError 验证处理失败的消息留在 PEL。
//
// 这是「自动重试」的实现方式：不 ACK，下一轮读 "0" 会再拿到它。
// 如果失败也 ACK，那次通知就永远丢了，而且没有任何地方会告警。
func TestConsumerKeepsMessageOnHandlerError(t *testing.T) {
	rdb := testutil.NewRedis(t)
	ctx := context.Background()

	if err := rdb.XGroupCreateMkStream(ctx, testStream, testGroup, "0").Err(); err != nil {
		t.Fatalf("建消费组失败: %v", err)
	}
	addEvent(t, rdb)

	c := newTestConsumer(rdb, func(context.Context, redis.XMessage) error {
		return errors.New("模拟处理失败")
	})

	// 读取本身是成功的，失败的是单条处理，所以返回 true。
	if ok := c.drain(ctx, ">", -1); !ok {
		t.Fatal("读取本身应当成功")
	}
	if n := pendingCount(t, rdb); n != 1 {
		t.Fatalf("处理失败的消息应当留在 PEL，实际 %d 条", n)
	}
}

// TestConsumerStopsOnContextCancel 验证 Run 在 ctx 取消后退出。
//
// 这里刻意先把 Redis 客户端关掉，让读取持续失败、Run 进入退避分支：
// 退避如果用 time.Sleep 睡死两秒，这个用例会在 200ms 的等待后超时失败。
// 线上对应的是「收到退出信号，但进程要等满一个退避周期才停」，
// 容器编排那边可能直接 SIGKILL，正在处理的消息来不及收尾。
func TestConsumerStopsOnContextCancel(t *testing.T) {
	rdb := testutil.NewRedis(t)
	if err := rdb.Close(); err != nil {
		t.Fatalf("关闭测试 Redis 失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := newTestConsumer(rdb, func(context.Context, redis.XMessage) error { return nil })

	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()

	// 等一小会儿，让 Run 至少进入一次失败与退避。
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("ctx 取消后 Run 没有及时退出（退避可能在阻塞退出）")
	}
}

// TestBackoffDelayGrowsAndCaps 验证退避时长随失败次数翻倍，并在上限处封顶。
//
// 只测「第一次是 100ms」不够：连续失败时如果不翻倍，断连期间仍是每 100ms
// 一次的稳定冲击。也不该无限增长，否则 Redis 恢复后要等很久才接上。
func TestBackoffDelayGrowsAndCaps(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 800 * time.Millisecond},
		{5, 1600 * time.Millisecond},
		// 从第 6 次起超过 2 秒，应当被截断到上限。
		{6, backoffMax},
		{100, backoffMax},
		// failures 传 0 只在调用方算错时出现，这里给出最小值而不是负数。
		{0, 100 * time.Millisecond},
	}
	for _, tc := range cases {
		if got := backoffDelay(tc.failures); got != tc.want {
			t.Errorf("failures=%d：delay=%v，期望 %v", tc.failures, got, tc.want)
		}
	}
}

// TestBackoffCancelable 验证退避期间 ctx 取消能立刻返回。
//
// 这一条守的是「优雅退出」：如果 backoff 用 time.Sleep 睡满，
// 进程收到退出信号后要等完整个退避周期，容器编排那边可能直接 SIGKILL。
func TestBackoffCancelable(t *testing.T) {
	c := &Consumer{RDB: testutil.NewRedis(t), Stream: testStream, Group: testGroup, LogTag: "test"}

	// 用最长的退避档位：如果实现是睡死，这里会等满 2 秒才返回。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if ok := c.backoff(ctx, 10); ok {
		t.Fatal("ctx 已取消，backoff 应当返回 false")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("已取消的 ctx 应当立刻返回，实际等了 %v", elapsed)
	}
}
