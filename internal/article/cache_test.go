package article

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"forum-backend/internal/model"
)

// TestArticleDetailKey 确认 key 格式稳定，且不同 id 不会撞车。
// 以后改 key 版本时，这个测试会失败，提醒你同步更新文档和排查命令。
//
// 这里刻意写死字面量而不是用 articleDetailKeyPrefix + articleDetailCacheVersion 拼：
// 拼出来的期望值会跟着实现一起变，等于什么都没守。
// v1 → v2 是给 model.Article 加 view_count 时升的（旧缓存没有这个字段），
// v2 → v3 是加 tags 时升的。
func TestArticleDetailKey(t *testing.T) {
	got := articleDetailKey(42)
	want := "article:detail:v3:42"
	if got != want {
		t.Fatalf("articleDetailKey(42) = %q, want %q", got, want)
	}
	if articleDetailKey(1) == articleDetailKey(2) {
		t.Fatal("different ids must not share a cache key")
	}
}

// TestArticleDetailTTLJitter 验证两件事：TTL 落在区间内，且取值足够分散。
// 只看「在区间内」不够：一个永远返回固定值的函数也能通过区间检查。
func TestArticleDetailTTLJitter(t *testing.T) {
	const rounds = 1000
	lo := articleDetailTTLBase
	hi := articleDetailTTLBase + articleDetailTTLJitter

	seen := make(map[time.Duration]bool)
	for i := 0; i < rounds; i++ {
		ttl := articleDetailTTL()
		if ttl < lo || ttl >= hi {
			t.Fatalf("ttl %v out of range [%v, %v)", ttl, lo, hi)
		}
		seen[ttl] = true
	}

	// 1000 次调用如果只得到很少几种取值，说明过期时刻仍然被对齐了。
	if len(seen) < 50 {
		t.Fatalf("jitter looks degenerate: only %d distinct TTLs in %d rounds", len(seen), rounds)
	}
}

// TestArticleEmptySentinelIsNotArticleJSON 证明哨兵不会和真实文章 JSON 撞车。
// 空标记的全部安全性都建立在这一条上。
func TestArticleEmptySentinelIsNotArticleJSON(t *testing.T) {
	data, err := json.Marshal(&model.Article{ID: 1, Title: "x"})
	if err != nil {
		t.Fatalf("marshal article: %v", err)
	}
	if string(data) == articleEmptySentinel {
		t.Fatal("sentinel collides with a real article JSON")
	}
	if strings.HasPrefix(articleEmptySentinel, "{") {
		t.Fatal("sentinel must not look like a JSON object")
	}
}

// TestNullCacheValueTrap 把 03 章 7.6 说过的陷阱跑成可复现的事实。
// 结论：不能把 "null" 当空标记，否则命中分支会把零值文章返回出去。
func TestNullCacheValueTrap(t *testing.T) {
	var a model.Article
	if err := json.Unmarshal([]byte("null"), &a); err != nil {
		t.Fatalf("unmarshal null into struct should not error: %v", err)
	}
	if a.ID != 0 {
		t.Fatal("unmarshal null left a non-zero id")
	}
}
