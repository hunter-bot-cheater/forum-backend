package article

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forum-backend/internal/domain"
	"forum-backend/internal/testutil"
)

// TestCreateAcceptsChineseTitle 是「中文标题被误拒」的回归测试。
//
// 标题列是 varchar(128)，MySQL 按字符计；早先的实现用 len(title) > 128，
// len 取的是字节数，100 个汉字是 300 字节，会被判成超长而拒绝。
// 这条用例在旧实现下必然失败，是判断修复是否真的生效的判据。
func TestCreateAcceptsChineseTitle(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "author")

	title := strings.Repeat("测", 100)
	created, err := svc.Create(context.Background(), author.ID, title, "正文")
	if err != nil {
		t.Fatalf("100 个汉字的标题（300 字节）应当通过，实际报错: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("创建后 ID 应当被回填，实际是 0")
	}
	if created.Title != title {
		t.Errorf("标题被改动：%q", created.Title)
	}
}

// TestCreateRejectsOversizeInput 验证两类上限真的会拦人。
//
// 标题与正文单位不同（字符 / 字节），分别越界一次：
// 只测其中一种，另一条判断写错也发现不了。
func TestCreateRejectsOversizeInput(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "author")

	cases := []struct {
		name    string
		title   string
		content string
	}{
		{"标题超字符上限", strings.Repeat("好", maxTitleRunes+1), "正文"},
		// 正文按字节判：maxContentBytes+1 个字母正好越过上限。
		{"正文超字节上限", "正常标题", strings.Repeat("a", maxContentBytes+1)},
	}
	for _, tc := range cases {
		_, err := svc.Create(context.Background(), author.ID, tc.title, tc.content)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s：错误 = %v，期望 ErrInvalidInput", tc.name, err)
		}
	}
}

// TestListFiltersByAuthor 验证用户主页的文章标签只返回这个人的文章。
//
// 关键在于 total 也要跟着过滤：只过滤列表、不过滤总数时，
// 前端按 ceil(total/pageSize) 算出来的页数会比实际多，翻到第二页就是空的。
// 所以断言里同时检查「条数」「标题」「总数」三样。
func TestListFiltersByAuthor(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	ctx := context.Background()

	alice := testutil.SeedUser(t, db, "listalice")
	bob := testutil.SeedUser(t, db, "listbob")

	for _, title := range []string{"alice 的第一篇", "alice 的第二篇"} {
		if _, err := svc.Create(ctx, alice.ID, title, "正文"); err != nil {
			t.Fatalf("创建 %s 失败: %v", title, err)
		}
	}
	if _, err := svc.Create(ctx, bob.ID, "bob 的唯一一篇", "正文"); err != nil {
		t.Fatalf("创建 bob 的文章失败: %v", err)
	}

	list, total, err := svc.List(ctx, alice.ID, 1, 10)
	if err != nil {
		t.Fatalf("按作者查列表失败: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d，期望 2（只算 alice 的文章）", total)
	}
	if len(list) != 2 {
		t.Fatalf("返回 %d 条，期望 2 条", len(list))
	}
	for _, a := range list {
		if a.UserID != alice.ID {
			t.Errorf("文章 %q 属于用户 %d，不该出现在 alice 的列表里", a.Title, a.UserID)
		}
	}

	// authorID = 0 表示不限作者：三个人四篇都要出现。
	// 不测这一条的话，把默认值写成「只查 user_id=0」也能通过上面两段断言。
	all, allTotal, err := svc.List(ctx, 0, 1, 10)
	if err != nil {
		t.Fatalf("全站列表失败: %v", err)
	}
	if allTotal != 3 || len(all) != 3 {
		t.Errorf("全站列表 total=%d len=%d，期望 3/3", allTotal, len(all))
	}
}

// TestUpdateByAuthor 验证作者能改自己的文章，且缓存被正确清掉。
//
// 先 Detail 一次把旧内容灌进缓存，再 Update：
// 如果 Update 忘了清缓存，最后那次 Detail 会拿到旧标题，
// 这条断言就是专门用来抓「改了但不生效」的。
func TestUpdateByAuthor(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "updateauthor")

	created, err := svc.Create(ctx, author.ID, "旧标题", "旧正文")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	// 灌一次缓存，让后面的 Update 有旧值可清。
	if _, err := svc.Detail(ctx, created.ID); err != nil {
		t.Fatalf("预热缓存失败: %v", err)
	}

	updated, err := svc.Update(ctx, created.ID, author.ID, "新标题", "新正文")
	if err != nil {
		t.Fatalf("作者改自己的文章应当成功，实际报错: %v", err)
	}
	if updated.Title != "新标题" || updated.Content != "新正文" {
		t.Errorf("返回值没更新：title=%q content=%q", updated.Title, updated.Content)
	}
	if updated.ID != created.ID {
		t.Errorf("返回值 id=%d，期望 %d", updated.ID, created.ID)
	}

	// 重新走一次详情：缓存已被删掉，这里读到的是库里的新内容。
	again, err := svc.Detail(ctx, created.ID)
	if err != nil {
		t.Fatalf("改完再查详情失败: %v", err)
	}
	if again.Title != "新标题" || again.Content != "新正文" {
		t.Errorf("改完读到的还是旧内容：title=%q content=%q（缓存没清？）", again.Title, again.Content)
	}
}

// TestUpdateRejectsNonAuthor 验证别人改不动这篇文章。
//
// 期望 ErrNotFound 而不是「无权限」：回 403 等于告诉对方「这篇确实存在」，
// 与 Delete 的口径保持一致。同时断言库里内容一个字都没变。
func TestUpdateRejectsNonAuthor(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "owneruser")
	other := testutil.SeedUser(t, db, "otheruser")

	created, err := svc.Create(ctx, author.ID, "原题", "原文")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	_, err = svc.Update(ctx, created.ID, other.ID, "被篡改的标题", "被篡改的正文")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("非作者编辑应返回 ErrNotFound，实际: %v", err)
	}

	got, err := svc.Detail(ctx, created.ID)
	if err != nil {
		t.Fatalf("查详情失败: %v", err)
	}
	if got.Title != "原题" || got.Content != "原文" {
		t.Errorf("非作者的编辑居然落库了：title=%q content=%q", got.Title, got.Content)
	}
}

// TestUpdateMissingArticle 验证改不存在的文章返回 ErrNotFound，
// 而不是一条数据库错误或 500。
func TestUpdateMissingArticle(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "missingauthor")

	_, err := svc.Update(context.Background(), 999999, author.ID, "标题", "正文")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("改不存在的文章应返回 ErrNotFound，实际: %v", err)
	}
}

// TestUpdateSameContentSucceeds 守 MySQL 的 RowsAffected 坑：
// 内容没变时 UPDATE 的 affected rows 是 0，不能据此判断「文章不存在」，
// 否则用户打开编辑页什么都不改直接保存，会收到「文章不存在」。
func TestUpdateSameContentSucceeds(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "sameauthor")

	created, err := svc.Create(ctx, author.ID, "原样标题", "原样正文")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	updated, err := svc.Update(ctx, created.ID, author.ID, "原样标题", "原样正文")
	if err != nil {
		t.Fatalf("内容没改也应当成功，实际报错: %v", err)
	}
	if updated.Title != "原样标题" {
		t.Errorf("title = %q，期望原样返回", updated.Title)
	}
}

// TestUpdateRejectsInvalidInput 验证校验规则与发布一致：空、超长都拒，
// 而且拒掉时库里的旧内容不能被写坏。
func TestUpdateRejectsInvalidInput(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "invalidauthor")

	created, err := svc.Create(ctx, author.ID, "原始标题", "原始正文")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	cases := []struct {
		name    string
		title   string
		content string
	}{
		{name: "标题为空", title: "   ", content: "正文"},
		{name: "正文为空", title: "标题", content: "  "},
		{name: "标题超长", title: strings.Repeat("长", 129), content: "正文"},
		{name: "正文超长", title: "标题", content: strings.Repeat("a", 50001)},
	}
	for _, tc := range cases {
		if _, err := svc.Update(ctx, created.ID, author.ID, tc.title, tc.content); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s：期望 ErrInvalidInput，实际 %v", tc.name, err)
		}
	}

	got, err := svc.Detail(ctx, created.ID)
	if err != nil {
		t.Fatalf("查详情失败: %v", err)
	}
	if got.Title != "原始标题" || got.Content != "原始正文" {
		t.Errorf("非法输入把内容写坏了：title=%q content=%q", got.Title, got.Content)
	}
}
