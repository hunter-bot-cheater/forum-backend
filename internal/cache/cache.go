// Package cache 是 Redis 上「存一个 JSON 对象」的最小工具。
//
// 只封装三件重复率最高的事：读 key 并反序列化、序列化后写 key、删 key。
//
// 刻意不做成「自动读穿 + 回源 + 回填」的通用缓存器：各模块的降级策略并不一样，
// 文章详情回源后要写空标记挡穿透，关注数回源后要按自己的 TTL 写回，
// 把这些策略抽进去只会得到一个参数巨多、每个参数都要解释的函数。
// 策略留给调用方，这里只保证一条共同约束。
//
// 共同约束：Redis 出错不往上抛。缓存是加速手段，挂掉应该退化成直查数据库，
// 而不是把接口打成 500。所以这里返回 error 只是让调用方记日志、降级，
// 不表示本次请求失败。
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrBadValue 表示 key 存在、但值不是调用方期望的 JSON。
//
// 单独一个哨兵值，是为了让调用方区分两种失败：
//   - 值坏了：Redis 是通的，只是数据格式不对，回源之后应当回填覆盖它；
//   - 连接类错误：Redis 本身不可用，回填也没有意义，只会多一条失败日志。
//
// 混成一种错误的话，坏值会一直留到 TTL 过期，期间每次请求都要回源。
var ErrBadValue = errors.New("cache value is not valid JSON")

// opTimeout 是单次缓存操作的时间上限。
//
// 客户端本身的读写超时是 500ms（见 config.InitRedis），那是给「Redis 慢但还在」
// 留的余量。缓存不该占这么多预算：一次请求里通常有读、回填、失效好几步，
// 每步各等 500ms 会串成秒级延迟，而缓存本来就只是加速手段。
// 所以这里再压一层，超时当成未命中，直接回源。
//
// 它是上限不是目标：Redis 正常时一次 GET 是亚毫秒级，这个值只影响故障时的耗时。
const opTimeout = 100 * time.Millisecond

// WithTimeout 给一次 Redis 操作套上统一的短超时，调用方必须在同一函数里 cancel。
//
// 给「不走 LoadJSON/StoreJSON、直接调 *redis.Client」的模块用。
// 文章详情就是这种：它的值可能是字符串哨兵，不能直接反序列化，
// 只能自己读原始字符串。没有这个函数，那些裸调用会退回到客户端的 500ms，
// 一次请求里读、二次读、回填三步各等 500ms，Redis 抖一下就是 1.5 秒。
//
// 返回值里不带「是否超时」这类信息：超时对调用方就等于失败，
// 该走和连接错误一样的降级分支。
func WithTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, opTimeout)
}

// LoadJSON 读一个 key 并把 JSON 反序列化到 dst，返回是否命中。
//
// 三种情况要分开看：
//   - 命中：返回 (true, nil)
//   - key 不存在：返回 (false, nil)。缓存未命中是正常路径，不是错误，
//     调用方据此回源，不需要先判断 redis.Nil。
//   - Redis 故障或值解析不了：返回 (false, err)。调用方应当当成未命中继续回源，
//     同时记一笔降级。
func LoadJSON(ctx context.Context, rdb *redis.Client, key string, dst any) (bool, error) {
	// 超时压到 opTimeout：Redis 卡住时最多拖这么久，不会占满客户端的 500ms。
	ctx, cancel := WithTimeout(ctx)
	defer cancel()

	raw, err := rdb.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("get cache %s: %w", key, err)
	}

	if err := json.Unmarshal(raw, dst); err != nil {
		// 值解析不了的原因通常是结构改过（字段重命名、类型变了），
		// 或者这个 key 被别的代码写成了另一种格式。
		// 不删掉的话，TTL 到期前每次请求都会解析失败、每次都回源，
		// 缓存等于彻底失效，还多了一次 Redis 往返。
		if delErr := rdb.Del(ctx, key).Err(); delErr != nil {
			// 两个错误都要留下：err 是解析失败的原因，delErr 是清理失败。
			// Go 1.20 起 fmt.Errorf 支持多个 %w，errors.Is 能同时匹配到任意一个，
			// 所以这里不退回 %v，免得调用方只能看到一段拼在文案里的字符串。
			return false, fmt.Errorf("%w: %s: %w（清理坏值失败: %w）", ErrBadValue, key, err, delErr)
		}
		return false, fmt.Errorf("%w: %s: %w", ErrBadValue, key, err)
	}
	return true, nil
}

// StoreJSON 把 value 序列化后写进 key，ttl 决定它最多能活多久。
//
// ttl 必须为正：0 或负数在 Redis 里等于「立刻过期」，写进去马上就没，
// 看起来像缓存不生效。这种调用错误在启动期就该暴露，不该静默跑起来。
func StoreJSON(ctx context.Context, rdb *redis.Client, key string, value any, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("cache %s: ttl must be positive, got %s", key, ttl)
	}

	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode cache %s: %w", key, err)
	}

	// 写也压到 opTimeout：回填是「顺手做的好事」，
	// 不该因为它卡住而把这次请求的响应拖慢。
	ctx, cancel := WithTimeout(ctx)
	defer cancel()

	if err := rdb.Set(ctx, key, payload, ttl).Err(); err != nil {
		return fmt.Errorf("set cache %s: %w", key, err)
	}
	return nil
}

// Delete 删掉若干 key，用于写路径的失效。
//
// 一次 DEL 传多个 key 而不是循环单删：Redis 单线程执行命令，
// 多 key 版本只有一次往返，也不会出现「删了一半、中途断连」的中间态。
// 传空列表直接返回，调用方不必在每次拼 key 前判空。
func Delete(ctx context.Context, rdb *redis.Client, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}

	// 失效发生在写接口的响应路径上，给同样的短预算：
	// 删不掉还有 TTL 兜底，但不能让用户的关注操作等一秒。
	ctx, cancel := WithTimeout(ctx)
	defer cancel()

	if err := rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("delete cache keys %v: %w", keys, err)
	}
	return nil
}

// JitterTTL 返回 ba + [0, jitter) 区间里的一个随机时长，用来打散过期时刻。
//
// 解决的是缓存雪崩里最常见的一种成因：一批 key 在同一时间窗口被写入，
// 于是它们也在同一秒过期，过期瞬间所有请求一起回源，把数据库打出尖峰。
// 典型场景是 Redis 重启或清库之后——第一波访问会把热门数据集中回填，
// 之后每隔一个 TTL 就重复一次同样的尖峰。
//
// 加抖动之后，这批 key 的过期时刻被摊到一段窗口里，回源请求随之摊平。
// 代价只是「有些 key 多活一会儿」，对缓存来说没有副作用。
//
// 两个参数都必须为正：jitter 为 0 时退化成固定 TTL（等于没修雪崩），
// 负数会算出比 base 更短的 TTL。这里不做运行时校验，因为调用方传的都是
// 包级常量，配错在第一次读代码或第一个测试里就能看出来，
// 每个请求都判一次只是白白绕一层。
func JitterTTL(base, jitter time.Duration) time.Duration {
	// Int64N 返回 [0, n) 的整数，转成 Duration 后加到 base 上。
	// 用 math/rand/v2 的全局源：它自带并发安全，不需要自己加锁。
	extra := rand.Int64N(int64(jitter))
	return base + time.Duration(extra)
}
