// 本文件只测标签的管理侧操作：改名与合并。
// 单独成文件是为了让「合并时复合主键会撞」这类回归有一个明确的落点，
// 混在 tag_auto_test.go 里和异步补标签的用例搅在一起不好定位。
package article

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
)

// linkTags 给一篇文章挂上若干标签，走的是发帖时同一个 replaceArticleTags。
// 不复用 svc.Create 是为了让用例只关心标签关系，
// 不用顺带构造标题正文与命中发布判重的 Redis key。
func linkTags(t *testing.T, db *gorm.DB, articleID uint, names ...string) {
	t.Helper()
	if err := replaceArticleTags(db, articleID, names); err != nil {
		t.Fatalf("挂标签失败: %v", err)
	}
}

// tagIDByName 按名字取标签 id，查不到直接失败。
// 用它是为了验证「列表里看到的 id 能直接拿去改名」这条链路。
func tagIDByName(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()
	var tag model.Tag
	if err := db.Where("name = ?", name).First(&tag).Error; err != nil {
		t.Fatalf("标签 %q 不存在: %v", name, err)
	}
	return tag.ID
}

// tagsOfArticle 读一篇文章当前的标签名，用来断言改名的最终效果。
func tagsOfArticle(t *testing.T, svc *ArticleService, articleID uint) []string {
	t.Helper()
	byArticle, err := svc.tagsOf(context.Background(), []uint{articleID})
	if err != nil {
		t.Fatalf("读取文章标签失败: %v", err)
	}
	return byArticle[articleID]
}

// TestRenameTagRenamesWithoutMerge 验证目标名字没人用时就是纯改名。
//
// 这类改名只动 tags.name 一行，关联表一行都不用碰；
// 若实现里为了「简单」走成「删旧标签+建新标签+重建关联」，
// 这里能看出来：tag_id 会变，article_tags 的行会被删了又插。
func TestRenameTagRenamesWithoutMerge(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	svc := NewArticleService(db, rdb, nil)
	author := testutil.SeedUser(t, db, "tagrenam")
	post := testutil.SeedArticle(t, db, author.ID, "带一个标签的文章")
	linkTags(t, db, post.ID, "golang")

	oldID := tagIDByName(t, db, "golang")
	result, err := svc.RenameTag(context.Background(), oldID, "Go")
	if err != nil {
		t.Fatalf("改名失败: %v", err)
	}

	if result.Merged {
		t.Errorf("目标名字没人用，不该走合并")
	}
	if result.TagID != oldID {
		t.Errorf("改名后 tag_id = %d，期望保持 %d（只改名字不换行）", result.TagID, oldID)
	}
	if result.Name != "Go" {
		t.Errorf("结果里的 name = %q，期望 Go", result.Name)
	}
	if result.MovedArticles != 1 {
		t.Errorf("moved_articles = %d，期望 1", result.MovedArticles)
	}

	if got := tagsOfArticle(t, svc, post.ID); len(got) != 1 || got[0] != "Go" {
		t.Errorf("文章标签 = %v，期望 [Go]", got)
	}
	// 旧名字必须彻底消失，否则标签云里会同时出现 golang 与 Go。
	var count int64
	if err := db.Model(&model.Tag{}).Where("name = ?", "golang").Count(&count).Error; err != nil {
		t.Fatalf("统计旧标签失败: %v", err)
	}
	if count != 0 {
		t.Errorf("旧名字 golang 还留着 %d 行", count)
	}
}

// TestRenameTagMergesIntoExisting 验证「改成已存在的名字」会合并两个标签。
//
// 最容易漏的是「同时挂着两个标签的文章」：那些行把 tag_id 从源改成目标时
// 会撞 (article_id, tag_id) 复合主键，整个事务失败。
// 所以这里专门造一篇同时挂 golang 与 Go 的文章，
// 并且断言它最后只剩一条关联，而不是两条重复的 Go。
func TestRenameTagMergesIntoExisting(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	svc := NewArticleService(db, rdb, nil)
	author := testutil.SeedUser(t, db, "tagmerge")

	onlyOld := testutil.SeedArticle(t, db, author.ID, "只挂旧标签")
	onlyNew := testutil.SeedArticle(t, db, author.ID, "只挂新标签")
	both := testutil.SeedArticle(t, db, author.ID, "两个都挂")
	linkTags(t, db, onlyOld.ID, "golang")
	linkTags(t, db, onlyNew.ID, "Go")
	linkTags(t, db, both.ID, "golang", "Go")

	oldID := tagIDByName(t, db, "golang")
	newID := tagIDByName(t, db, "Go")

	result, err := svc.RenameTag(context.Background(), oldID, "Go")
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if !result.Merged {
		t.Fatalf("目标标签已存在，应当标记为合并")
	}
	if result.TagID != newID {
		t.Errorf("结果 tag_id = %d，期望指向保留下来的 %d", result.TagID, newID)
	}
	// 挂过旧标签的两篇（只有旧标签的 + 两个都挂的）都受影响。
	if result.MovedArticles != 2 {
		t.Errorf("moved_articles = %d，期望 2", result.MovedArticles)
	}

	for _, post := range []*model.Article{onlyOld, onlyNew, both} {
		got := tagsOfArticle(t, svc, post.ID)
		if len(got) != 1 || got[0] != "Go" {
			t.Errorf("文章 %d 的标签 = %v，期望 [Go]", post.ID, got)
		}
	}

	// 源标签这一行要删掉：留着一个没有任何关联的空标签，
	// 标签云里就永远排在最前面（计数 0 也占位），点进去还是空列表。
	var left int64
	if err := db.Model(&model.Tag{}).Where("id = ?", oldID).Count(&left).Error; err != nil {
		t.Fatalf("统计源标签失败: %v", err)
	}
	if left != 0 {
		t.Errorf("合并后源标签还在")
	}
}

// TestRenameTagInvalidatesDetailCache 验证改名会清掉受影响文章的详情缓存。
//
// 详情缓存里带着标签名，键是「文章 id」而不是「标签 id」，
// 所以改标签名是唯一一种「不碰文章表却必须让文章缓存失效」的操作。
// 漏了这一步，管理员改完名字去点那篇文章，看到的还是旧标签，
// 会以为没保存成功，然后再点一次。
func TestRenameTagInvalidatesDetailCache(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	svc := NewArticleService(db, rdb, nil)
	author := testutil.SeedUser(t, db, "tagcache")
	post := testutil.SeedArticle(t, db, author.ID, "有缓存的文章")
	linkTags(t, db, post.ID, "golang")
	ctx := context.Background()

	// 先正常读一次详情，把带标签的缓存写进去。
	detail, err := svc.Detail(ctx, post.ID)
	if err != nil {
		t.Fatalf("读详情失败: %v", err)
	}
	if len(detail.Tags) != 1 || detail.Tags[0] != "golang" {
		t.Fatalf("准备阶段标签 = %v，期望 [golang]", detail.Tags)
	}
	cacheKey := articleDetailKey(post.ID)
	if n, err := rdb.Exists(ctx, cacheKey).Result(); err != nil || n != 1 {
		t.Fatalf("准备阶段缓存没写进去: n=%d err=%v", n, err)
	}

	if _, err := svc.RenameTag(ctx, tagIDByName(t, db, "golang"), "Go"); err != nil {
		t.Fatalf("改名失败: %v", err)
	}

	if n, err := rdb.Exists(ctx, cacheKey).Result(); err != nil || n != 0 {
		t.Fatalf("改名后详情缓存没被清掉: n=%d err=%v", n, err)
	}
	// 再读一次应当是库里的新名字，而不是缓存里的旧值。
	again, err := svc.Detail(ctx, post.ID)
	if err != nil {
		t.Fatalf("改名后读详情失败: %v", err)
	}
	if len(again.Tags) != 1 || again.Tags[0] != "Go" {
		t.Errorf("改名后详情标签 = %v，期望 [Go]", again.Tags)
	}
}

// TestRenameTagRejectsBadInput 覆盖三种会被拒绝的输入。
//
// 空名字与超长必须挡在事务外：让空名字落库会得到一个点不开的标签，
// 超长会被 MySQL 按字段长度截断，截断后的名字可能和已有标签撞唯一索引。
func TestRenameTagRejectsBadInput(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	svc := NewArticleService(db, rdb, nil)
	author := testutil.SeedUser(t, db, "tagbad")
	post := testutil.SeedArticle(t, db, author.ID, "文章")
	linkTags(t, db, post.ID, "valid")
	ctx := context.Background()
	id := tagIDByName(t, db, "valid")

	cases := []struct {
		name  string
		tagID uint
		input string
		want  error
	}{
		{"空名字", id, "   ", domain.ErrInvalidInput},
		{"超长", id, strings.Repeat("标", maxTagRunes+1), domain.ErrInvalidInput},
		{"标签不存在", 1 << 20, "任意", domain.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.RenameTag(ctx, tc.tagID, tc.input); !errors.Is(err, tc.want) {
				t.Errorf("err = %v，期望 %v", err, tc.want)
			}
		})
	}

	// 被拒绝的请求不能留下任何改动。
	if got := tagsOfArticle(t, svc, post.ID); len(got) != 1 || got[0] != "valid" {
		t.Errorf("失败的请求改动了数据: %v", got)
	}
}

// TestRenameTagIsIdempotent 验证重名的 second 次调用不报错。
//
// 管理员点完按钮没看到反馈（网络慢、页面卡）很常见，第二次请求不该变成
// 「资源已存在」这种让人以为出错的提示。
func TestRenameTagIsIdempotent(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	svc := NewArticleService(db, rdb, nil)
	author := testutil.SeedUser(t, db, "tagidem")
	post := testutil.SeedArticle(t, db, author.ID, "文章")
	linkTags(t, db, post.ID, "golang")
	ctx := context.Background()
	id := tagIDByName(t, db, "golang")

	for i := 1; i <= 2; i++ {
		result, err := svc.RenameTag(ctx, id, "Go")
		if err != nil {
			t.Fatalf("第 %d 次改名失败: %v", i, err)
		}
		if result.Merged {
			t.Errorf("第 %d 次不该走合并", i)
		}
	}
	if got := tagsOfArticle(t, svc, post.ID); len(got) != 1 || got[0] != "Go" {
		t.Errorf("文章标签 = %v，期望 [Go]", got)
	}

	// 第二次调用什么都不会改，因此也不该去清详情缓存：
	// 一个热门标签挂着几千篇文章，连点两下按钮就白删几千个 key。
	// 先把缓存读回来，再发一次同名请求，看它还在不在。
	if _, err := svc.Detail(ctx, post.ID); err != nil {
		t.Fatalf("预热详情缓存失败: %v", err)
	}
	cacheKey := articleDetailKey(post.ID)
	if n, err := rdb.Exists(ctx, cacheKey).Result(); err != nil || n != 1 {
		t.Fatalf("预热后缓存没写进去: n=%d err=%v", n, err)
	}
	if _, err := svc.RenameTag(ctx, id, "Go"); err != nil {
		t.Fatalf("同名请求失败: %v", err)
	}
	if n, err := rdb.Exists(ctx, cacheKey).Result(); err != nil || n != 1 {
		t.Errorf("什么都没改却删掉了详情缓存: n=%d err=%v", n, err)
	}
}

// TestListTagsForAdminIncludesUnused 验证管理端列表包含没人使用的标签、带 id、支持搜索。
//
// 公开的 /tags 只按热度取前 20 个，冷门标签在其中根本看不到；
// 而「一个文章都没有的标签」恰恰是最该被清理的，列表里必须有它。
func TestListTagsForAdminIncludesUnused(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	svc := NewArticleService(db, rdb, nil)
	author := testutil.SeedUser(t, db, "taglist")
	post := testutil.SeedArticle(t, db, author.ID, "文章")
	linkTags(t, db, post.ID, "golang")

	// 手动建一个没有任何文章的空标签。
	unused := model.Tag{Name: "没人用的标签"}
	if err := db.Create(&unused).Error; err != nil {
		t.Fatalf("建空标签失败: %v", err)
	}

	ctx := context.Background()
	items, total, err := svc.ListTagsForAdmin(ctx, "", 1, 10)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d，期望 2", total)
	}
	// 先判长度再按下标取：只写 items[1] 的话，空标签被内连接滤掉时
	// 用例会以数组越界的形式挂掉，看不出真正的原因。
	if len(items) != 2 {
		t.Fatalf("列表返回 %d 条，期望 2 条（含没人使用的标签）: %+v", len(items), items)
	}
	// 排序按文章数倒序：golang 有 1 篇，空标签 0 篇。
	if items[0].Name != "golang" || items[0].Count != 1 {
		t.Errorf("第一条 = %+v，期望 golang/1", items[0])
	}
	if items[1].Name != "没人用的标签" || items[1].Count != 0 {
		t.Errorf("第二条 = %+v，期望 没人用的标签/0", items[1])
	}
	// 必须有 id：改名接口按 id 定位，列表里不给就等于让管理员去猜。
	if items[1].ID != unused.ID {
		t.Errorf("空标签 id = %d，期望 %d", items[1].ID, unused.ID)
	}

	// 关键词过滤同时作用于列表与总数。
	filtered, filteredTotal, err := svc.ListTagsForAdmin(ctx, "gol", 1, 10)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if filteredTotal != 1 || len(filtered) != 1 || filtered[0].Name != "golang" {
		t.Errorf("搜索 gol 得到 total=%d list=%+v，期望只有 golang", filteredTotal, filtered)
	}
}
