package follow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"forum-backend/internal/cache"
	"forum-backend/internal/model"
	"forum-backend/internal/user"
)

// TestStatsServedFromCache 验证第二次读主页时用的是缓存而不是数据库。
//
// 手法：第一次读完（缓存已回填）之后，绕过 service 直接把关系行删掉。
// 如果这次读的还是数据库，粉丝数会变成 0；只有读了缓存才会仍是 1。
// 直接查「有没有发 SQL」需要往 GORM 上挂回调，比这个办法绕得多。
func TestStatsServedFromCache(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Follow(ctx, f.alice.ID, f.bob.ID); err != nil {
		t.Fatalf("关注失败: %v", err)
	}

	// 第一次读：未命中，回源并回填。
	_, followers, _, err := f.svc.Stats(ctx, f.bob.ID, 0)
	if err != nil {
		t.Fatalf("第一次 Stats 失败: %v", err)
	}
	if followers != 1 {
		t.Fatalf("第一次粉丝数 = %d，期望 1", followers)
	}

	// 绕过 service 删掉关系行：数据库现在数出来是 0，缓存里还是 1。
	if err := f.db.Where("followee_id = ?", f.bob.ID).Delete(&model.Follow{}).Error; err != nil {
		t.Fatalf("直接删关系失败: %v", err)
	}

	_, followers, _, err = f.svc.Stats(ctx, f.bob.ID, 0)
	if err != nil {
		t.Fatalf("第二次 Stats 失败: %v", err)
	}
	if followers != 1 {
		t.Errorf("第二次粉丝数 = %d，期望命中缓存返回 1；返回 0 说明又查了数据库", followers)
	}
}

// TestStatsCacheInvalidatedOnFollow 验证关注之后计数立刻变，不会被缓存拖住。
//
// 这是缓存最典型的 bug：读的路径做了缓存，写的路径忘了删 key，
// 表现是「刚关注完，刷新主页粉丝数还是旧的」，而且过一会儿自己又好了（TTL 到期），
// 排查起来非常费劲。
func TestStatsCacheInvalidatedOnFollow(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	// 先让 bob 的主页进缓存（此时 0 个粉丝）。
	if _, followers, _, err := f.svc.Stats(ctx, f.bob.ID, 0); err != nil {
		t.Fatalf("预热缓存失败: %v", err)
	} else if followers != 0 {
		t.Fatalf("预热时粉丝数 = %d，期望 0", followers)
	}

	if _, err := f.svc.Follow(ctx, f.alice.ID, f.bob.ID); err != nil {
		t.Fatalf("关注失败: %v", err)
	}

	// 写路径删了 key，这里必须重新查库拿到 1。
	_, followers, _, err := f.svc.Stats(ctx, f.bob.ID, 0)
	if err != nil {
		t.Fatalf("关注后 Stats 失败: %v", err)
	}
	if followers != 1 {
		t.Errorf("关注后粉丝数 = %d，期望 1；说明写路径没删缓存", followers)
	}

	// 关注者的「关注数」同样要失效，否则个人中心会显示 0。
	following, _, _, err := f.svc.Stats(ctx, f.alice.ID, 0)
	if err != nil {
		t.Fatalf("关注者 Stats 失败: %v", err)
	}
	if following != 1 {
		t.Errorf("关注者的关注数 = %d，期望 1", following)
	}
}

// TestStatsSurvivesRedisFailure 验证 Redis 不可用时主页仍然能打开。
//
// 缓存是加速手段不是依赖：挂了应该退化成直查数据库，
// 而不是把公开的主页接口打成 500。
func TestStatsSurvivesRedisFailure(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Follow(ctx, f.alice.ID, f.bob.ID); err != nil {
		t.Fatalf("关注失败: %v", err)
	}

	// 关掉客户端，之后所有 Redis 命令都会返回 "client is closed"。
	if err := f.svc.rdb.Close(); err != nil {
		t.Fatalf("关闭测试 Redis 客户端失败: %v", err)
	}

	_, followers, _, err := f.svc.Stats(ctx, f.bob.ID, 0)
	if err != nil {
		t.Fatalf("Redis 故障时 Stats 不该失败: %v", err)
	}
	if followers != 1 {
		t.Errorf("粉丝数 = %d，期望 1（降级后查数据库）", followers)
	}
}

// TestStatsDropsCorruptCache 验证缓存值解析不了时当作未命中，并把手里的坏值删掉。
//
// 坏值的典型来源：结构改过、或者别的代码把同一个 key 写成了另一种格式。
// 不删的话，TTL 到期前每次请求都会解析失败、每次都回源，
// 缓存等于完全失效，还白多一次 Redis 往返。
func TestStatsDropsCorruptCache(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Follow(ctx, f.alice.ID, f.bob.ID); err != nil {
		t.Fatalf("关注失败: %v", err)
	}

	key := followStatsKey(f.bob.ID)
	if err := f.svc.rdb.Set(ctx, key, "这不是 JSON", 0).Err(); err != nil {
		t.Fatalf("写入坏缓存失败: %v", err)
	}

	_, followers, _, err := f.svc.Stats(ctx, f.bob.ID, 0)
	if err != nil {
		t.Fatalf("Stats 失败: %v", err)
	}
	if followers != 1 {
		t.Errorf("粉丝数 = %d，期望 1（回源结果）", followers)
	}

	// 坏值被删掉后重新回填，现在的值必须是能解析的 JSON。
	raw, err := f.svc.rdb.Get(ctx, key).Bytes()
	if err != nil {
		t.Fatalf("回填后读缓存失败: %v", err)
	}
	var snap statsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("回填的缓存值无法解析: %v，原始内容 %q", err, raw)
	}
	if snap.Followers != 1 {
		t.Errorf("回填的粉丝数 = %d，期望 1", snap.Followers)
	}
}

// TestStatsCacheKeyVersioned 守住 key 的格式。
// key 一旦改了名字或少了版本号，线上旧 key 不会自动失效，
// 会出现「改了结构但读到旧格式」的问题，所以用测试把它钉住。
func TestStatsCacheKeyVersioned(t *testing.T) {
	if got := followStatsKey(42); got != "user:follow:stats:v1:42" {
		t.Errorf("key = %q，期望 user:follow:stats:v1:42", got)
	}
}

// countLogger 是只做一件事的 GORM 日志器：数「查 follows 表的 COUNT」执行了几次。
//
// 为什么不用指标来数：指标是全进程累计的，要看增量得取前后差值，
// 而并发场景下差值里混着别的用例的写入，断言会飘。
// 直接数 SQL 是唯一能证明「到底回源了几次」的办法。
//
// delay 是给每次 COUNT 人为加的耗时。没有它会测不出东西：
// 本地 MySQL 上两个 COUNT 只要一两毫秒，20 个 goroutine 还没全部启动，
// 第一个请求就已经查完并回填了，后面全都命中缓存——单飞哪怕被删掉，
// 测试也照样通过。加了延迟才会真的形成「多个请求同时未命中」的窗口。
// Trace 在 SQL 执行完之后才被调用，所以 sleep 恰好延长了闭包持有锁的时间。
type countLogger struct {
	gormlogger.Interface
	delay time.Duration
	mu    sync.Mutex
	count int
}

// Trace 是 GORM 每次执行 SQL 后的回调，sql 是已经渲染好的最终语句。
func (l *countLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	if strings.Contains(sql, "SELECT count(*) FROM `follows`") {
		l.mu.Lock()
		l.count++
		l.mu.Unlock()
		time.Sleep(l.delay)
	}
}

// Hit 返回目前数到的 COUNT 次数。
func (l *countLogger) Hit() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

// TestStatsMergesConcurrentMisses 验证同一用户的并发未命中只回源一次（防击穿）。
//
// 场景：热点主页的缓存 key 刚好过期或被删，几十个请求同时打进来。
// 没有请求合并的话，每个请求都要跑两个 COUNT，而这两个 COUNT 要扫完
// 这个人的全部关系行——缓存没命中反而比不加缓存更伤数据库。
//
// 断言的是「COUNT 执行次数」而不是耗时或 share 计数：
// 前者是这件事的直接证据，后者只能间接推断。
func TestStatsMergesConcurrentMisses(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	// 造两个粉丝，让回源有真实的扫描量。
	for _, follower := range []*model.User{f.alice, f.carol} {
		if _, err := f.svc.Follow(ctx, follower.ID, f.bob.ID); err != nil {
			t.Fatalf("关注失败: %v", err)
		}
	}

	// 关注动作会删缓存，这里再显式删一次，确保接下来一定是未命中。
	if err := cache.Delete(ctx, f.svc.rdb, followStatsKey(f.bob.ID)); err != nil {
		t.Fatalf("清缓存失败: %v", err)
	}

	// 给这个 service 换一个带计数日志器的 db 句柄。
	// Session 只影响这个句柄，不改全局的 GORM 配置。
	// 每次 COUNT 加 10ms 延迟，把「并发未命中」的窗口撑开，否则测不出合并效果。
	counter := &countLogger{Interface: gormlogger.Discard, delay: 10 * time.Millisecond}
	svc := NewService(f.db.Session(&gorm.Session{Logger: counter}), f.svc.rdb, user.NewUserService(f.db))

	const n = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)

	// errs 带缓冲，避免某个 goroutine 卡在发送上导致 wg.Wait 永远不返回。
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			// 所有 goroutine 在这里对齐，close(start) 之后尽量同时冲进未命中分支。
			<-start
			_, followers, _, err := svc.Stats(ctx, f.bob.ID, 0)
			if err == nil && followers != 2 {
				err = fmt.Errorf("粉丝数 = %d，期望 2", followers)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("并发 Stats 失败: %v", err)
		}
	}

	// 单个请求回源是 2 次 COUNT（关注数 + 粉丝数）。
	// 完全不合并时这里是 2n = 40 次；合并生效就是 2 次。
	// 留一点余量：允许个别请求因为调度稍晚、缓存已回填而单独走一趟。
	if got := counter.Hit(); got > 6 {
		t.Errorf("COUNT 执行了 %d 次，期望 2 次左右（%d 个并发请求被合并）", got, n)
	}
}
