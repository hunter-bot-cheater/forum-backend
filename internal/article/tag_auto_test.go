package article

import (
	"context"
	"errors"
	"testing"

	"forum-backend/internal/testutil"
)

// fakeTagger 是 TagSuggester 的假实现。
//
// 用它而不是真的 llm.Client：这里要验的是「什么时候调、返回结果怎么处理」，
// 不该依赖外网和密钥。calls 用来断言「用户自己填了标签时不许调模型」——
// 那条只能靠调用次数证明，看返回值是看不出来的。
type fakeTagger struct {
	tag   string
	err   error
	calls int
}

func (f *fakeTagger) SuggestTag(_ context.Context, _, _ string) (string, error) {
	f.calls++
	return f.tag, f.err
}

// TestCreateSuggestsTagWhenNoneGiven 验证「一个标签都不填」时会把模型的建议落库。
//
// 两处都要查：Create 的返回值（前端发布后立刻渲染用的）和 Detail 的重新读库
// （证明关联表真的写了一行，而不只是内存里塞了个字段）。
func TestCreateSuggestsTagWhenNoneGiven(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tagger := &fakeTagger{tag: "后端开发"}
	svc := NewArticleService(db, testutil.NewRedis(t), tagger)
	author := testutil.SeedUser(t, db, "autotag")

	created, err := svc.Create(ctx, author.ID, "MySQL 索引怎么建", "讲联合索引的最左前缀。", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tagger.calls != 1 {
		t.Fatalf("模型调用次数 = %d，期望 1", tagger.calls)
	}
	if len(created.Tags) != 1 || created.Tags[0] != "后端开发" {
		t.Fatalf("返回的标签 = %v，期望 [后端开发]", created.Tags)
	}

	got, err := svc.Detail(ctx, created.ID)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "后端开发" {
		t.Fatalf("落库后的标签 = %v，期望 [后端开发]", got.Tags)
	}
}

// TestCreateKeepsManualTagsWithoutCallingModel 验证用户填了标签就完全不碰模型。
//
// 这条不只是省一次请求：如果这里调了模型，用户手填的标签可能被覆盖或追加，
// 而「我写的分类被系统改了」是比「没有分类」更糟的体验。
func TestCreateKeepsManualTagsWithoutCallingModel(t *testing.T) {
	db := testutil.NewDB(t)
	tagger := &fakeTagger{tag: "不该被用到的标签"}
	svc := NewArticleService(db, testutil.NewRedis(t), tagger)
	author := testutil.SeedUser(t, db, "manualtag")

	created, err := svc.Create(context.Background(), author.ID, "标题", "正文", []string{"手填"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tagger.calls != 0 {
		t.Fatalf("用户已填标签，模型仍被调用了 %d 次", tagger.calls)
	}
	if len(created.Tags) != 1 || created.Tags[0] != "手填" {
		t.Fatalf("标签 = %v，期望 [手填]", created.Tags)
	}
}

// TestCreateSucceedsWhenTagSuggestionFails 验证模型失败不影响发布。
//
// 标签是附加信息：超时、密钥过期、内容审核拒绝都不该让用户发不出文章。
// 断言三件事——发布成功、标签为空、错误没有往上冒。
func TestCreateSucceedsWhenTagSuggestionFails(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tagger := &fakeTagger{err: errors.New("upstream timeout")}
	svc := NewArticleService(db, testutil.NewRedis(t), tagger)
	author := testutil.SeedUser(t, db, "tagfail")

	created, err := svc.Create(ctx, author.ID, "没有标签也要能发", "正文", nil)
	if err != nil {
		t.Fatalf("模型失败不该让发布失败: %v", err)
	}
	if len(created.Tags) != 0 {
		t.Fatalf("失败时标签应为空，实际 %v", created.Tags)
	}

	got, err := svc.Detail(ctx, created.ID)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(got.Tags) != 0 {
		t.Fatalf("落库后不该有标签，实际 %v", got.Tags)
	}
}

// TestUpdateNeverSuggestsTag 验证编辑接口不会自动补标签。
//
// 编辑时把标签全删掉是用户的明确动作，自动补一个等于「删不掉」，
// 用户会反复删、反复出现，最后只能认为标签功能坏了。
func TestUpdateNeverSuggestsTag(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tagger := &fakeTagger{tag: "自动补的"}
	svc := NewArticleService(db, testutil.NewRedis(t), tagger)
	author := testutil.SeedUser(t, db, "updatetag")

	created, err := svc.Create(ctx, author.ID, "原标题", "原正文", []string{"手填"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Create 用掉了 1 次调用额度，从这里开始只关心 Update 有没有再加一次。
	before := tagger.calls

	updated, err := svc.Update(ctx, created.ID, author.ID, "新标题", "新正文", nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if tagger.calls != before {
		t.Fatalf("编辑时模型被调用了，次数 %d → %d", before, tagger.calls)
	}
	if len(updated.Tags) != 0 {
		t.Fatalf("标签应当被清空，实际 %v", updated.Tags)
	}
}
