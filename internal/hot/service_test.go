package hot

import (
	"context"
	"fmt"
	"testing"

	"github.com/redis/go-redis/v9"

	"forum-backend/internal/keys"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
)

// hotFixture 是一个用例需要的现场：库、Redis、service。
// 数据工厂嵌进来，造用户与文章就不用每条都传 (t, db)。
type hotFixture struct {
	*testutil.Fixture
	rdb *redis.Client
	svc *HotService
	// author 与 article 是点赞的落点，loadByIDs 要靠文章表补标题。
	author  *model.User
	article *model.Article
}

// newHotFixture 准备干净库与一篇文章。
func newHotFixture(t *testing.T) *hotFixture {
	t.Helper()

	fx := testutil.NewFixture(t)
	// rdb 只建一个：service 与用例断言要读写同一个实例，
	// 各建一个会得到两个互不相通的内存 Redis。
	rdb := testutil.NewRedis(t)
	author, article := fx.ArticleWithAuthor("热榜文章")
	return &hotFixture{
		Fixture: fx,
		rdb:     rdb,
		svc:     NewHotService(fx.DB(), rdb),
		author:  author,
		article: article,
	}
}

// seedLikes 给文章刷 n 个赞，每个赞来自不同用户（唯一索引是 article_id + user_id）。
// 用户名交给 Fixture 的递增序号，同一个用例里分多次刷赞也不会撞 users.username。
func (f *hotFixture) seedLikes(t *testing.T, articleID uint, n int) {
	t.Helper()

	for i := 0; i < n; i++ {
		liker := f.User("liker")
		like := &model.ArticleLike{ArticleID: articleID, UserID: liker.ID}
		if err := f.DB().Create(like).Error; err != nil {
			t.Fatalf("插入第 %d 个赞失败: %v", i, err)
		}
	}
}

// TestTopSelfHealsEvictedZSet 验证「ready 在、ZSET 却被淘汰」时能自愈。
//
// Redis 内存不足时按淘汰策略挑 key，ZSET 这种大对象比单值 key 先死，
// 于是出现「标记还在、榜单空了」。如果只信 ready 就返回空榜，
// 用户看到的热榜会突然清空，直到 ready 一小时后过期才恢复。
// 这里造出这个状态，断言一次读就把榜单重建回来。
func TestTopSelfHealsEvictedZSet(t *testing.T) {
	f := newHotFixture(t)
	ctx := context.Background()

	f.seedLikes(t, f.article.ID, 3)

	// 标记记着 3 条，但 ZSET 故意不建——等价于被内存淘汰。
	if err := f.rdb.Set(ctx, hotReadyKey, "3", hotReadyTTL).Err(); err != nil {
		t.Fatalf("写 ready 标记失败: %v", err)
	}
	if n, err := f.rdb.ZCard(ctx, keys.HotArticlesKey).Result(); err != nil || n != 0 {
		t.Fatalf("用例前提不成立：ZSET 应当为空，实际 %d（err=%v）", n, err)
	}

	list, err := f.svc.Top(ctx, 10)
	if err != nil {
		t.Fatalf("Top 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("自愈后应当返回 1 条，实际 %d 条", len(list))
	}
	if list[0].ArticleID != f.article.ID || list[0].LikeCount != 3 {
		t.Errorf("返回 %+v，期望 article_id=%d like_count=3", list[0], f.article.ID)
	}

	// 重建后 ZSET 必须真的有数据，否则下一次读又要自愈一遍。
	if n, err := f.rdb.ZCard(ctx, keys.HotArticlesKey).Result(); err != nil || n != 1 {
		t.Errorf("重建后 ZSET 期望 1 条，实际 %d（err=%v）", n, err)
	}
}

// TestTopDoesNotRebuildEmptyBoard 验证「真空榜」不触发重建查询。
//
// ready 记着 0 说明重建时确实没人点赞。这里故意再插一个赞：
// 如果代码看到空榜就去查库，它会发现这个赞并返回 1 条；
// 正确的行为是信 ready、直接返回空，数据库里的变化要等下一轮重建才可见。
func TestTopDoesNotRebuildEmptyBoard(t *testing.T) {
	f := newHotFixture(t)
	ctx := context.Background()

	if err := f.rdb.Set(ctx, hotReadyKey, "0", hotReadyTTL).Err(); err != nil {
		t.Fatalf("写 ready 标记失败: %v", err)
	}
	f.seedLikes(t, f.article.ID, 1)

	list, err := f.svc.Top(ctx, 10)
	if err != nil {
		t.Fatalf("空榜读取失败: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ready 记着 0 条时应当返回空榜，实际 %d 条（说明它去查库重启了重建）", len(list))
	}

	// ZSET 也不该被写：重建一次就会留下数据。
	if n, err := f.rdb.ZCard(ctx, keys.HotArticlesKey).Result(); err != nil || n != 0 {
		t.Errorf("真空榜不该触发重建，ZSET 期望 0 条，实际 %d（err=%v）", n, err)
	}
}

// TestTopFromMySQLCachesResult 验证降级路径的进程内缓存真的生效。
//
// 降级发生在 Redis 不可用时，这期间每个请求都跑一次 GROUP BY 会压垮数据库。
// 判据是「缓存窗口内改了库，返回值不变」：只有走缓存才会看不到新写入的赞。
func TestTopFromMySQLCachesResult(t *testing.T) {
	f := newHotFixture(t)
	ctx := context.Background()

	f.seedLikes(t, f.article.ID, 2)

	first, err := f.svc.TopFromMySQL(ctx, 10)
	if err != nil {
		t.Fatalf("第一次降级查询失败: %v", err)
	}
	if len(first) != 1 || first[0].LikeCount != 2 {
		t.Fatalf("第一次结果 = %+v，期望 1 条 2 赞", first)
	}

	// 缓存窗口内再加一个赞。没有缓存的话第二次会读到 3 赞。
	f.seedLikes(t, f.article.ID, 1)

	second, err := f.svc.TopFromMySQL(ctx, 10)
	if err != nil {
		t.Fatalf("第二次降级查询失败: %v", err)
	}
	if len(second) != 1 || second[0].LikeCount != 2 {
		t.Fatalf("缓存窗口内应当返回旧值 2 赞，实际 %+v（说明每次都直接查了库）", second)
	}
}

// TestTopFromMySQLTruncatesToLimit 验证降级路径按请求条数截断。
//
// 缓存固定存 fallbackTop 条，请求只要 1 条时不能把整份缓存都返回：
// 前端按条数渲染，多出来的会让「热榜前 1 名」显示成一屏。
func TestTopFromMySQLTruncatesToLimit(t *testing.T) {
	f := newHotFixture(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		other := f.Article(f.author.ID, fmt.Sprintf("文章-%d", i))
		f.seedLikes(t, other.ID, i+1)
	}

	list, err := f.svc.TopFromMySQL(ctx, 2)
	if err != nil {
		t.Fatalf("降级查询失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("请求 2 条，实际返回 %d 条", len(list))
	}
}
