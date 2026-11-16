package like

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/article"
	"forum-backend/internal/domain"
	"forum-backend/internal/event"
	"forum-backend/internal/keys"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
)

// likeFixture 是一次点赞测试的三件套：库、Redis、待点赞的文章。
//
// 这里用真的 article.ArticleService 而不是打桩：LikeService 与它的耦合只有一个
// InvalidateDetailCache，而「点完赞详情里的 like_count 要跟着变」正是必须验证的行为，
// 打桩就把这条路径的验证价值抹掉了。
type likeFixture struct {
	db         *gorm.DB
	rdb        *redis.Client
	svc        *LikeService
	articleSvc *article.ArticleService
	author     *model.User
	reader     *model.User
	post       *model.Article
}

func newLikeFixture(t *testing.T) *likeFixture {
	t.Helper()

	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	articleSvc := article.NewArticleService(db, rdb, nil)

	return &likeFixture{
		db:         db,
		rdb:        rdb,
		svc:        NewLikeService(db, rdb, articleSvc),
		articleSvc: articleSvc,
		author:     testutil.SeedUser(t, db, "likeauthor"),
		reader:     testutil.SeedUser(t, db, "likereader"),
		post:       testutil.SeedArticle(t, db, testutil.SeedUser(t, db, "likeposter").ID, "被点赞的文章"),
	}
}

// TestLikeCreatesRelationAndProjections 验证一次点赞同时落三处：
// 关系表、Redis 的点赞集合、热榜 ZSET 的分数。
//
// 三处缺任何一处都会出问题：少了关系表重启就丢；少了集合 HasLiked 每次都回源；
// 少了 ZSET 热榜里看不到这篇文章。
func TestLikeCreatesRelationAndProjections(t *testing.T) {
	f := newLikeFixture(t)
	ctx := context.Background()

	res, err := f.svc.Like(ctx, f.post.ID, f.reader.ID)
	if err != nil {
		t.Fatalf("点赞失败: %v", err)
	}
	if !res.Changed || !res.Liked {
		t.Errorf("首次点赞 changed=%v liked=%v，期望 true/true", res.Changed, res.Liked)
	}
	if res.Count != 1 {
		t.Errorf("like_count = %d，期望 1", res.Count)
	}
	if res.ArticleID != f.post.ID {
		t.Errorf("article_id = %d，期望 %d", res.ArticleID, f.post.ID)
	}

	if got := testutil.CountRows(t, f.db, "article_likes"); got != 1 {
		t.Errorf("article_likes 有 %d 行，期望 1", got)
	}

	member := strconv.FormatUint(uint64(f.reader.ID), 10)
	inSet, err := f.rdb.SIsMember(ctx, keys.LikeSetKey(f.post.ID), member).Result()
	if err != nil {
		t.Fatalf("查点赞集合失败: %v", err)
	}
	if !inSet {
		t.Error("点赞后 Redis 集合里应该有这个用户")
	}

	score, err := f.rdb.ZScore(ctx, keys.HotArticlesKey, strconv.FormatUint(uint64(f.post.ID), 10)).Result()
	if err != nil {
		t.Fatalf("查热榜分数失败: %v", err)
	}
	if score != 1 {
		t.Errorf("热榜分数 = %v，期望 1", score)
	}

	// 点赞必须发出一条领域事件，作者的通知靠它产生。
	if got := testutil.CountRows(t, f.db, "outbox_events"); got != 1 {
		t.Errorf("outbox 有 %d 行，期望 1 条 like_created", got)
	}
	var ev model.OutboxEvent
	if err := f.db.First(&ev).Error; err != nil {
		t.Fatalf("读事件失败: %v", err)
	}
	if ev.EventType != string(event.TypeLikeCreated) {
		t.Errorf("事件类型 = %q，期望 %q", ev.EventType, event.TypeLikeCreated)
	}
}

// TestLikeTwiceIsIdempotent 验证重复点赞不写第二行、不发第二条通知。
//
// 前端双击、网络重试都会打到这条路径。如果第二次也发通知，作者会收到两条一模一样的提醒。
func TestLikeTwiceIsIdempotent(t *testing.T) {
	f := newLikeFixture(t)
	ctx := context.Background()

	first, err := f.svc.Like(ctx, f.post.ID, f.reader.ID)
	if err != nil {
		t.Fatalf("第一次点赞失败: %v", err)
	}
	second, err := f.svc.Like(ctx, f.post.ID, f.reader.ID)
	if err != nil {
		t.Fatalf("重复点赞不该报错: %v", err)
	}

	if !first.Changed {
		t.Error("第一次点赞 changed 应当是 true")
	}
	if second.Changed {
		t.Error("重复点赞 changed 应当是 false")
	}
	if second.Count != 1 {
		t.Errorf("重复点赞后 like_count = %d，期望 1", second.Count)
	}
	// Liked 必须仍然是 true：客户端拿它点亮按钮，跟着 changed 走会让按钮灭掉。
	if !second.Liked {
		t.Error("重复点赞后 liked 仍应是 true")
	}

	if got := testutil.CountRows(t, f.db, "article_likes"); got != 1 {
		t.Errorf("article_likes 有 %d 行，期望 1", got)
	}
	if got := testutil.CountRows(t, f.db, "outbox_events"); got != 1 {
		t.Errorf("重复点赞不该再发通知，outbox 有 %d 行", got)
	}
}

// TestUnlikeRemovesRelationAndProjections 验证取消点赞把三处状态都清掉，
// 包括热榜里分数归零后要把成员删掉（留着会变成「0 赞还挂在榜上」）。
func TestUnlikeRemovesRelationAndProjections(t *testing.T) {
	f := newLikeFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Like(ctx, f.post.ID, f.reader.ID); err != nil {
		t.Fatalf("准备点赞失败: %v", err)
	}

	res, err := f.svc.Unlike(ctx, f.post.ID, f.reader.ID)
	if err != nil {
		t.Fatalf("取消点赞失败: %v", err)
	}
	if !res.Changed {
		t.Error("取消一个点过的赞，changed 应当是 true")
	}
	if res.Liked {
		t.Error("取消后 liked 应当是 false")
	}
	if res.Count != 0 {
		t.Errorf("like_count = %d，期望 0", res.Count)
	}

	if got := testutil.CountRows(t, f.db, "article_likes"); got != 0 {
		t.Errorf("article_likes 还剩 %d 行", got)
	}

	member := strconv.FormatUint(uint64(f.reader.ID), 10)
	inSet, err := f.rdb.SIsMember(ctx, keys.LikeSetKey(f.post.ID), member).Result()
	if err != nil {
		t.Fatalf("查点赞集合失败: %v", err)
	}
	if inSet {
		t.Error("取消后集合里不该还有这个用户")
	}

	_, err = f.rdb.ZScore(ctx, keys.HotArticlesKey, strconv.FormatUint(uint64(f.post.ID), 10)).Result()
	if !errors.Is(err, redis.Nil) {
		t.Errorf("点赞归零后热榜成员应当被删掉，实际 err = %v", err)
	}
}

// TestUnlikeWithoutLikeIsNoop 验证取消一个没点过的赞不报错、不写库。
// 接口幂等，前端无需先查状态。
func TestUnlikeWithoutLikeIsNoop(t *testing.T) {
	f := newLikeFixture(t)

	res, err := f.svc.Unlike(context.Background(), f.post.ID, f.reader.ID)
	if err != nil {
		t.Fatalf("取消没点过的赞不该报错: %v", err)
	}
	if res.Changed {
		t.Error("没有关系可删，changed 应当是 false")
	}
	if res.Count != 0 {
		t.Errorf("like_count = %d，期望 0", res.Count)
	}
	if got := testutil.CountRows(t, f.db, "outbox_events"); got != 0 {
		t.Errorf("取消点赞不该产生事件，outbox 有 %d 行", got)
	}
}

// TestLikeMissingArticle 验证点赞不存在的文章返回 ErrNotFound。
//
// 少了这层检查，插入会被外键拒绝，客户端拿到的是 500 而不是 404。
func TestLikeMissingArticle(t *testing.T) {
	f := newLikeFixture(t)

	_, err := f.svc.Like(context.Background(), 999999, f.reader.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误 = %v，期望 ErrNotFound", err)
	}
	if got := testutil.CountRows(t, f.db, "article_likes"); got != 0 {
		t.Errorf("文章不存在时不该写关系，article_likes 有 %d 行", got)
	}
}

// TestLikeInvalidatesArticleDetailCache 验证点完赞，详情里的 like_count 会变。
//
// 先 Detail 一次把 like_count=0 灌进缓存，再点赞。少了 InvalidateDetailCache 的话，
// 详情会一直显示 0，用户点了赞刷新页面发现没生效。
func TestLikeInvalidatesArticleDetailCache(t *testing.T) {
	f := newLikeFixture(t)
	ctx := context.Background()

	warm, err := f.articleSvc.Detail(ctx, f.post.ID)
	if err != nil {
		t.Fatalf("预热详情失败: %v", err)
	}
	if warm.LikeCount != 0 {
		t.Fatalf("预热时 like_count = %d，期望 0", warm.LikeCount)
	}

	if _, err := f.svc.Like(ctx, f.post.ID, f.reader.ID); err != nil {
		t.Fatalf("点赞失败: %v", err)
	}

	after, err := f.articleSvc.Detail(ctx, f.post.ID)
	if err != nil {
		t.Fatalf("点赞后查详情失败: %v", err)
	}
	if after.LikeCount != 1 {
		t.Errorf("点赞后详情 like_count = %d，期望 1（缓存没失效？）", after.LikeCount)
	}
}

// TestHasLikedFallsBackToMySQLAndRepairsSet 验证 Redis 投影丢了以后还能答对。
//
// 手法是点赞之后直接删掉集合 key，模拟 Redis 重启或同步失败。
// 只查集合的话这里会答 false，用户明明点过却看到空心按钮。
// 同时验证回填：确认点过之后要把成员补回集合。
func TestHasLikedFallsBackToMySQLAndRepairsSet(t *testing.T) {
	f := newLikeFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Like(ctx, f.post.ID, f.reader.ID); err != nil {
		t.Fatalf("准备点赞失败: %v", err)
	}
	if err := f.rdb.Del(ctx, keys.LikeSetKey(f.post.ID)).Err(); err != nil {
		t.Fatalf("删除点赞集合失败: %v", err)
	}

	liked, err := f.svc.HasLiked(ctx, f.post.ID, f.reader.ID)
	if err != nil {
		t.Fatalf("HasLiked 失败: %v", err)
	}
	if !liked {
		t.Error("集合丢了也要能从 MySQL 查出来点过赞")
	}

	member := strconv.FormatUint(uint64(f.reader.ID), 10)
	inSet, err := f.rdb.SIsMember(ctx, keys.LikeSetKey(f.post.ID), member).Result()
	if err != nil {
		t.Fatalf("查点赞集合失败: %v", err)
	}
	if !inSet {
		t.Error("确认点过之后应当把成员补回集合")
	}
}

// TestLikeRollsBackWhenEventFails 验证事件写不进去时，点赞关系一起回滚。
//
// 手法是删掉 outbox 表制造失败（与评论模块同一套做法）：这是最贴近真实故障的造法。
// 不回滚的话会留下「点了赞、作者永远收不到通知」的半成品。
func TestLikeRollsBackWhenEventFails(t *testing.T) {
	f := newLikeFixture(t)

	if err := f.db.Migrator().DropTable(&model.OutboxEvent{}); err != nil {
		t.Fatalf("删除 outbox 表失败: %v", err)
	}
	t.Cleanup(func() {
		if err := f.db.AutoMigrate(&model.OutboxEvent{}); err != nil {
			t.Fatalf("恢复 outbox 表失败: %v", err)
		}
	})

	if _, err := f.svc.Like(context.Background(), f.post.ID, f.reader.ID); err == nil {
		t.Fatal("事件写不进去时 Like 必须返回错误")
	}

	// 核心断言：article_likes 已经 INSERT 过，事务回滚必须把它抹掉。
	if got := testutil.CountRows(t, f.db, "article_likes"); got != 0 {
		t.Errorf("事务应当回滚，article_likes 却留下了 %d 行", got)
	}
}
