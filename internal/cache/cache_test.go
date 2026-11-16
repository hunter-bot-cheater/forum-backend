package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"forum-backend/internal/testutil"
)

// 测试用的载荷结构，字段和业务无关，只用来验证序列化往返。
type payload struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// TestStoreThenLoad 验证写进去能原样读出来。
func TestStoreThenLoad(t *testing.T) {
	ctx := context.Background()
	rdb := testutil.NewRedis(t)

	want := payload{Name: "爱丽丝", Count: 7}
	if err := StoreJSON(ctx, rdb, "k1", want, time.Minute); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	var got payload
	hit, err := LoadJSON(ctx, rdb, "k1", &got)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !hit {
		t.Fatal("应当命中")
	}
	if got != want {
		t.Errorf("读回 = %+v，期望 %+v", got, want)
	}
}

// TestLoadMissingIsNotError 验证 key 不存在时返回 (false, nil)。
// 未命中是正常路径，调用方不该为了它先判断 redis.Nil。
func TestLoadMissingIsNotError(t *testing.T) {
	ctx := context.Background()
	rdb := testutil.NewRedis(t)

	var got payload
	hit, err := LoadJSON(ctx, rdb, "no-such-key", &got)
	if err != nil {
		t.Fatalf("未命中不该返回 error，实际 %v", err)
	}
	if hit {
		t.Error("不存在的 key 不该报告命中")
	}
}

// TestLoadCorruptValueDropsKey 验证坏值被当成未命中，并且被顺手删掉。
//
// 不删的话，TTL 到期前每次请求都会解析失败、每次都回源，
// 缓存等于彻底失效，还白多一次 Redis 往返。
func TestLoadCorruptValueDropsKey(t *testing.T) {
	ctx := context.Background()
	rdb := testutil.NewRedis(t)

	if err := rdb.Set(ctx, "bad", "这不是 JSON", 0).Err(); err != nil {
		t.Fatalf("准备坏值失败: %v", err)
	}

	var got payload
	hit, err := LoadJSON(ctx, rdb, "bad", &got)
	if hit {
		t.Error("坏值不该报告命中")
	}
	if !errors.Is(err, ErrBadValue) {
		t.Fatalf("错误 = %v，期望 ErrBadValue", err)
	}

	// 坏 key 必须已经被删掉，否则下次还会撞上。
	if n, err := rdb.Exists(ctx, "bad").Result(); err != nil {
		t.Fatalf("查 key 是否存在失败: %v", err)
	} else if n != 0 {
		t.Error("坏值应当被删除")
	}
}

// TestStoreRejectsNonPositiveTTL 验证 ttl 为 0 或负数直接报错。
//
// Redis 里 ttl 为 0 等于「立刻过期」，写进去马上就没，
// 表现是「缓存看起来不生效」，这种调用错误应当在开发期就炸出来。
func TestStoreRejectsNonPositiveTTL(t *testing.T) {
	ctx := context.Background()
	rdb := testutil.NewRedis(t)

	for _, ttl := range []time.Duration{0, -time.Second} {
		if err := StoreJSON(ctx, rdb, "k", payload{}, ttl); err == nil {
			t.Errorf("ttl = %s 时应当报错", ttl)
		}
	}
}

// TestDelete 验证删除多个 key 与空列表两种情况。
func TestDelete(t *testing.T) {
	ctx := context.Background()
	rdb := testutil.NewRedis(t)

	if err := Delete(ctx, rdb); err != nil {
		t.Fatalf("空列表应当直接返回 nil，实际 %v", err)
	}

	if err := StoreJSON(ctx, rdb, "a", payload{}, time.Minute); err != nil {
		t.Fatalf("写入 a 失败: %v", err)
	}
	if err := StoreJSON(ctx, rdb, "b", payload{}, time.Minute); err != nil {
		t.Fatalf("写入 b 失败: %v", err)
	}

	if err := Delete(ctx, rdb, "a", "b"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if n, err := rdb.Exists(ctx, "a", "b").Result(); err != nil {
		t.Fatalf("查 key 失败: %v", err)
	} else if n != 0 {
		t.Errorf("还剩 %d 个 key，期望全删", n)
	}
}

// TestJitterTTLWithinWindow 验证抖动只往「更长」的方向加，且不超过上限。
//
// 方向必须只有一个：如果允许比 base 更短，某些 key 的存活时间会变得
// 不可预测，而且和调用方写的注释对不上。抖动的作用是把过期时刻摊开，
// 不需要把 TTL 缩短。
func TestJitterTTLWithinWindow(t *testing.T) {
	base := 5 * time.Minute
	jitter := time.Minute

	// 多取几次，覆盖随机分布的各个位置。
	seen := make(map[time.Duration]bool)
	for i := 0; i < 200; i++ {
		got := JitterTTL(base, jitter)
		if got < base {
			t.Fatalf("TTL = %s，比 base %s 还短", got, base)
		}
		if got >= base+jitter {
			t.Fatalf("TTL = %s，超出上限 %s", got, base+jitter)
		}
		seen[got] = true
	}

	// 200 次里如果只出现一个值，说明抖动没生效（比如取了固定随机源）。
	// 这里只要求「不止一个值」，不要求覆盖整个区间——区间宽度是分钟级，
	// 200 次采样撞不出全部取值是正常的。
	if len(seen) < 2 {
		t.Errorf("200 次取值只有 %d 种，抖动没有生效", len(seen))
	}
}
