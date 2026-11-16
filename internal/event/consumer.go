package event

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// 消费循环的默认参数，两个 worker 都用这一份。
//
// 20 条 / 2 秒不是「每秒 10 条」的吞吐上限：BLOCK 只在没有新消息时才等满，
// 有消息就立刻返回。这两个值影响的是延迟与空轮询频率，不是吞吐。
const (
	defaultBatch = 20
	defaultBlock = 2 * time.Second
)

// 读失败后的退避参数。
//
// 没有退避时，Redis 断连会让 Run 变成一个零延迟热循环：每一轮立刻失败、
// 立刻重试，一分钟能打几万次错误日志，把磁盘和日志采集一起拖垮。
// 100ms 起步、翻倍到 2 秒封顶，Redis 恢复后最多多等 2 秒就能接上。
const (
	backoffBase     = 100 * time.Millisecond
	backoffMax      = 2 * time.Second
	backoffMaxShift = 5 // 100ms << 5 = 3.2s，先被 backoffMax 截断，再大也只是多算几次
)

// Consumer 是 Redis Stream 消费循环的骨架。
//
// 通知生成与私信推送两个 worker 的循环完全一样：
// 建消费组 → 先读 PEL 补处理没 ACK 的 → 再读新消息 → 处理成功才 ACK。
// 差别只有消费组名、起始位置和处理函数，所以骨架留在这里，业务只写 Handler。
//
// 失败语义：
//   - 单条消息处理失败不 ACK，留在 PEL 里下一轮重试（相当于自动重投）；
//   - 读失败退避后重试，不会让一次断连变成热循环；
//   - ctx 取消时立刻退出，不把连接断开的错误当成故障。
type Consumer struct {
	RDB    *redis.Client
	Stream string
	Group  string
	// Name 是组内消费者名。同一消费组里多个实例必须各不相同，
	// 否则 Redis 会把它们当成同一个消费者，PEL 归属会互相覆盖。
	Name string

	// Handler 处理一条消息。返回 nil 才会 ACK。
	Handler func(ctx context.Context, msg redis.XMessage) error

	// Batch 是一次最多取多少条，0 表示用 defaultBatch。
	Batch int64
	// Block 是没有新消息时最多阻塞多久，0 表示用 defaultBlock。
	// 它同时充当退出检查点：阻塞期间 ctx 被取消，XReadGroup 会返回错误。
	Block time.Duration

	// FromStart 决定消费组从哪开始读：
	//   - false：从 "$" 开始，只要建组之后产生的新消息（推送类场景）；
	//   - true：从 "0" 开始，把 Stream 里已有的历史全部补处理一遍（落库类场景）。
	FromStart bool

	// LogTag 是日志里的 worker 字段，用来区分是哪个消费循环在报错。
	LogTag string
}

// Run 建好消费组后开始循环，ctx 取消后返回。
// 调用方通常在独立 goroutine 里跑它，再用 WaitGroup 等退出。
func (c *Consumer) Run(ctx context.Context) {
	c.ensureGroup(ctx)

	// failures 记录连续读失败次数，只用于算退避时长；读成功一次就清零。
	// 单条消息处理失败不计入：那条消息留在 PEL 里，下一轮还会再读到自己。
	var failures int

	for {
		if ctx.Err() != nil {
			return
		}

		// 先读 PEL：进程被杀时留下的「已读走但没 ACK」的消息都在这里，
		// 先处理它们比先取新消息恢复得更快。
		// 读 PEL 不能阻塞，PEL 可能本来就是空的。
		if !c.drain(ctx, "0", -1) {
			failures++
			if !c.backoff(ctx, failures) {
				return
			}
			continue
		}
		failures = 0

		// 再读 ">"：从未投递给本组的新消息。这一轮允许阻塞。
		if !c.drain(ctx, ">", c.block()) {
			failures++
			if !c.backoff(ctx, failures) {
				return
			}
			continue
		}
		failures = 0
	}
}

// drain 读一批消息，逐条交给 Handler，成功才 ACK。
// 返回 false 表示「这次读取本身失败」，Run 据此进入退避。
func (c *Consumer) drain(ctx context.Context, start string, block time.Duration) bool {
	streams, err := c.RDB.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    c.Group,
		Consumer: c.Name,
		Streams:  []string{c.Stream, start},
		Count:    c.batch(),
		Block:    block,
	}).Result()

	// redis.Nil 是「阻塞超时、没有新消息」，属于正常工作状态，不是故障。
	if errors.Is(err, redis.Nil) {
		return true
	}
	if err != nil {
		if ctx.Err() != nil {
			// 退出过程中连接被关掉，不当作故障，也不该进退避。
			return true
		}
		slog.ErrorContext(ctx, "consume stream", "worker", c.LogTag, "from", start, "error", err)
		return false
	}

	for _, stream := range streams {
		for _, msg := range stream.Messages {
			if err := c.Handler(ctx, msg); err != nil {
				// 不 ACK，消息留在 PEL，下一轮用 "0" 重新读到。
				// 这一条处理不了不代表整批都处理不了，继续处理后面的。
				slog.ErrorContext(ctx, "handle event", "worker", c.LogTag, "message_id", msg.ID, "error", err)
				continue
			}
			if err := c.RDB.XAck(ctx, c.Stream, c.Group, msg.ID).Err(); err != nil {
				// ACK 失败最多导致这条消息被重复处理一次，业务侧靠唯一索引挡幂等。
				slog.ErrorContext(ctx, "ack event", "worker", c.LogTag, "message_id", msg.ID, "error", err)
			}
		}
	}
	return true
}

// ensureGroup 确保消费组存在。组已存在时 Redis 返回 BUSYGROUP，
// 那正是我们想要的状态，不需要当成错误。
//
// 这里读失败也不退出：Redis 可能只是刚启动还没就绪，
// 后面的 drain 会失败并进入退避，等 Redis 恢复后下一轮仍会尝试建组。
func (c *Consumer) ensureGroup(ctx context.Context) {
	start := "$"
	if c.FromStart {
		start = "0"
	}
	err := c.RDB.XGroupCreateMkStream(ctx, c.Stream, c.Group, start).Err()
	if err == nil || strings.Contains(err.Error(), "BUSYGROUP") {
		return
	}
	slog.ErrorContext(ctx, "create consumer group", "worker", c.LogTag, "group", c.Group, "error", err)
}

// backoffDelay 返回连续第 failures 次读失败后应当等待的时长。
//
// 抽成纯函数是为了能单独单测：直接测 backoff() 的话，每个档位都要真的睡一遍，
// 光封顶那一档就要等两秒，测试会慢到没人愿意跑。
func backoffDelay(failures int) time.Duration {
	shift := failures - 1
	if shift < 0 {
		shift = 0
	}
	if shift > backoffMaxShift {
		shift = backoffMaxShift
	}
	delay := backoffBase << shift
	if delay > backoffMax {
		delay = backoffMax
	}
	return delay
}

// backoff 在连续读失败后等待一段时间再重试，返回 false 表示 ctx 已取消。
//
// 用定时器而不是 time.Sleep：睡死两秒的话，进程收到退出信号要等两秒才停，
// 容器编排那边可能直接 SIGKILL，正在处理的消息就来不及收尾。
func (c *Consumer) backoff(ctx context.Context, failures int) bool {
	delay := backoffDelay(failures)

	slog.WarnContext(ctx, "consume stream backoff", "worker", c.LogTag,
		"failures", failures, "delay", delay)

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// batch 返回实际使用的单批条数。
func (c *Consumer) batch() int64 {
	if c.Batch > 0 {
		return c.Batch
	}
	return defaultBatch
}

// block 返回实际使用的阻塞时长。
func (c *Consumer) block() time.Duration {
	if c.Block > 0 {
		return c.Block
	}
	return defaultBlock
}
