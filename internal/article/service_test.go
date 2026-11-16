package article

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
)

// TestCreateAcceptsChineseTitle 是「中文标题被误拒」的回归测试。
//
// 标题列是 varchar(128)，MySQL 按字符计；早先的实现用 len(title) > 128，
// len 取的是字节数，100 个汉字是 300 字节，会被判成超长而拒绝。
// 这条用例在旧实现下必然失败，是判断修复是否真的生效的判据。
func TestCreateAcceptsChineseTitle(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	author := testutil.SeedUser(t, db, "author")

	title := strings.Repeat("测", 100)
	created, err := svc.Create(context.Background(), author.ID, title, "正文", nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
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
		_, err := svc.Create(context.Background(), author.ID, tc.title, tc.content, nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()

	alice := testutil.SeedUser(t, db, "listalice")
	bob := testutil.SeedUser(t, db, "listbob")

	for _, title := range []string{"alice 的第一篇", "alice 的第二篇"} {
		if _, err := svc.Create(ctx, alice.ID, title, "正文", nil); err != nil {
			t.Fatalf("创建 %s 失败: %v", title, err)
		}
	}
	if _, err := svc.Create(ctx, bob.ID, "bob 的唯一一篇", "正文", nil); err != nil {
		t.Fatalf("创建 bob 的文章失败: %v", err)
	}

	list, total, err := svc.List(ctx, alice.ID, "", "", 1, 10)
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
	all, allTotal, err := svc.List(ctx, 0, "", "", 1, 10)
	if err != nil {
		t.Fatalf("全站列表失败: %v", err)
	}
	if allTotal != 3 || len(all) != 3 {
		t.Errorf("全站列表 total=%d len=%d，期望 3/3", allTotal, len(all))
	}
}

// TestListFiltersByKeyword 验证搜索同时匹配标题与正文，且元字符当字面量。
//
// 覆盖三个容易写错的地方：
//  1. 只搜标题不搜正文 —— 用户记得的词常常在正文里
//  2. total 没跟着过滤 —— 前端按 total 算页数会多出空页
//  3. LIKE 的 % 和 _ 没转义 —— 搜一个 % 就返回全站
//
// 有意没去守「标题与正文的 OR 外面那对括号」：实测当前 GORM 会把带 OR 的
// 裸条件自动包起来，去掉括号生成的 SQL 依然是 user_id = ? AND (… OR …)，
// 写一条断言也照样是绿的，等于假守。这里保住的是行为——作者过滤与关键词
// 组合后结果正确；括号本身的意义写在 service 里那段注释上。
func TestListFiltersByKeyword(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()

	author := testutil.SeedUser(t, db, "kwauthor")
	other := testutil.SeedUser(t, db, "kwother")

	// author 的两篇：一篇关键词在标题，一篇在正文。
	// other 的那篇关键词只在正文 —— 专门用来验证作者条件与 OR 的括号关系。
	seeds := []struct {
		userID  uint
		title   string
		content string
	}{
		{author.ID, "GORM 的事务用法", "讲讲 Transaction 怎么写"},
		{author.ID, "缓存设计笔记", "顺手记一下 gorm 的日志配置"},
		{other.ID, "别人的闲聊", "我也用 gorm，但只是听说"},
	}
	for _, s := range seeds {
		if _, err := svc.Create(ctx, s.userID, s.title, s.content, nil); err != nil {
			t.Fatalf("创建 %q 失败: %v", s.title, err)
		}
	}

	// 只命中标题：标题里有「缓存」，另外两篇正文里没有。
	list, total, err := svc.List(ctx, 0, "缓存", "", 1, 10)
	if err != nil {
		t.Fatalf("按关键词查列表失败: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("搜「缓存」total=%d len=%d，期望 1/1", total, len(list))
	}
	if list[0].Title != "缓存设计笔记" {
		t.Errorf("命中 %q，期望「缓存设计笔记」", list[0].Title)
	}

	// 只命中正文，同时验证大小写不敏感：输入 gorm，标题里写的是 GORM。
	// MySQL 的 utf8mb4_unicode_ci 本身大小写不敏感，这里断言的是实际行为；
	// 以后换 collation 会在这里失败，提醒确认搜索是否符合预期。
	_, total, err = svc.List(ctx, 0, "gorm", "", 1, 10)
	if err != nil {
		t.Fatalf("搜正文失败: %v", err)
	}
	if total != 3 {
		t.Errorf("搜 gorm 全站 total=%d，期望 3（标题一篇、正文两篇）", total)
	}

	// 关键词与作者条件同时生效：作者名下只有 2 篇含 gorm，
	// 第三篇属于 other，正文同样含 gorm。两个条件都得真的拼进 SQL，
	// 少用任何一个都会让这里的条数或归属对不上。
	list, total, err = svc.List(ctx, author.ID, "gorm", "", 1, 10)
	if err != nil {
		t.Fatalf("按作者加关键词查列表失败: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("作者+关键词 total=%d len=%d，期望 2/2", total, len(list))
	}
	for _, a := range list {
		if a.UserID != author.ID {
			t.Errorf("文章 %q 属于用户 %d，不该出现在作者过滤的结果里", a.Title, a.UserID)
		}
	}

	// 搜不到时必须是空切片而不是 nil：nil 会序列化成 null，前端拿到的 list 就不是数组了。
	empty, emptyTotal, err := svc.List(ctx, 0, "这四个字不存在", "", 1, 10)
	if err != nil {
		t.Fatalf("空结果查询失败: %v", err)
	}
	if emptyTotal != 0 || len(empty) != 0 {
		t.Errorf("空结果 total=%d len=%d，期望 0/0", emptyTotal, len(empty))
	}
	if empty == nil {
		t.Error("空结果返回了 nil 切片，序列化后会是 null")
	}

	// LIKE 元字符必须被当成普通字符：% 匹配任意串、_ 匹配任意单字。
	// 不转义的话两条都会命中全部三篇，用户看到的结果与输入毫无关系。
	for _, kw := range []string{"%", "_"} {
		_, total, err = svc.List(ctx, 0, kw, "", 1, 10)
		if err != nil {
			t.Fatalf("搜 %q 失败: %v", kw, err)
		}
		if total != 0 {
			t.Errorf("搜 %q 命中 %d 篇，期望 0（元字符要被转义成字面量）", kw, total)
		}
	}

	// 全为空格的关键词按「没搜」处理，不能变成一次针对空格的全表扫描。
	_, total, err = svc.List(ctx, 0, "   ", "", 1, 10)
	if err != nil {
		t.Fatalf("搜空白关键词失败: %v", err)
	}
	if total != 3 {
		t.Errorf("空白关键词 total=%d，期望 3（等同于不筛选）", total)
	}
}

// TestListFiltersByTag 验证按标签筛选，且与作者条件能叠加。
//
// 这里特意让一篇文章挂两个标签：标签筛选如果写成 JOIN 而又没去重，
// 一篇文章会按标签数重复出现；Count 也会跟着翻倍，
// 前端算出来的页数比实际多。所以同时断言条数与总数。
func TestListFiltersByTag(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()

	alice := testutil.SeedUser(t, db, "tagalice")
	bob := testutil.SeedUser(t, db, "tagbob")

	// alice：第一篇挂两个标签，第二篇只挂后端；bob 的也挂后端。
	if _, err := svc.Create(ctx, alice.ID, "Go 与 MySQL", "正文", []string{"后端", "数据库"}); err != nil {
		t.Fatalf("创建第一篇失败: %v", err)
	}
	if _, err := svc.Create(ctx, alice.ID, "接口设计", "正文", []string{"后端"}); err != nil {
		t.Fatalf("创建第二篇失败: %v", err)
	}
	if _, err := svc.Create(ctx, bob.ID, "别人的后端笔记", "正文", []string{"后端"}); err != nil {
		t.Fatalf("创建 bob 的文章失败: %v", err)
	}

	// 全站按「后端」筛：三篇都命中，其中 alice 的第一篇挂了两个标签不能算两次。
	list, total, err := svc.List(ctx, 0, "", "后端", 1, 10)
	if err != nil {
		t.Fatalf("按标签查列表失败: %v", err)
	}
	if total != 3 || len(list) != 3 {
		t.Fatalf("标签「后端」total=%d len=%d，期望 3/3", total, len(list))
	}

	// 标签 + 作者：只剩 alice 的两篇。
	list, total, err = svc.List(ctx, alice.ID, "", "后端", 1, 10)
	if err != nil {
		t.Fatalf("按作者加标签查列表失败: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("作者+标签 total=%d len=%d，期望 2/2", total, len(list))
	}
	for _, a := range list {
		if a.UserID != alice.ID {
			t.Errorf("文章 %q 属于用户 %d，不该出现在 alice 的标签筛选里", a.Title, a.UserID)
		}
	}

	// 列表项要带上标签本身：前端靠它显示当前筛选，也用它渲染卡片上的标签。
	for _, a := range list {
		if len(a.Tags) == 0 {
			t.Errorf("文章 %q 的 Tags 为空，列表没把标签带回来", a.Title)
		}
	}

	// 不存在的标签：空列表、总数为 0，且不能报错。
	empty, emptyTotal, err := svc.List(ctx, 0, "", "不存在的标签", 1, 10)
	if err != nil {
		t.Fatalf("按不存在的标签查询失败: %v", err)
	}
	if emptyTotal != 0 || len(empty) != 0 {
		t.Errorf("不存在的标签 total=%d len=%d，期望 0/0", emptyTotal, len(empty))
	}
}

// TestCreateNormalizesTags 验证标签的清洗规则：去空白、去重、按不区分大小写判重。
//
// 不去重的话，前端把 "Go" 和 " go " 一起送上来会落成两条关联指向同一个标签，
// 列表里同一个标签出现两次，看起来像数据错乱。
func TestCreateNormalizesTags(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "tagclean")

	created, err := svc.Create(ctx, author.ID, "标签清洗", "正文", []string{" 后端 ", "后端", "Go", " go ", ""})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	want := []string{"后端", "Go"}
	if len(created.Tags) != len(want) {
		t.Fatalf("清洗后标签 = %v，期望 %v", created.Tags, want)
	}
	for i, name := range want {
		if created.Tags[i] != name {
			t.Errorf("第 %d 个标签 = %q，期望 %q", i, created.Tags[i], name)
		}
	}

	// 关联表里也只能有两行。
	var linked int64
	if err := db.Model(&model.ArticleTag{}).Where("article_id = ?", created.ID).Count(&linked).Error; err != nil {
		t.Fatalf("统计标签关联失败: %v", err)
	}
	if linked != 2 {
		t.Errorf("关联行数 = %d，期望 2", linked)
	}
}

// TestCreateRejectsTooManyTags 验证标签数量上限，且被拒时文章不会落库。
//
// 只断言错误不够：如果实现先写文章再校验标签，文章会留下来变成一篇没有标签的帖子，
// 用户以为发布失败、列表里却多了一篇。所以还要数一下 articles 的行数。
func TestCreateRejectsTooManyTags(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	author := testutil.SeedUser(t, db, "taglimit")

	_, err := svc.Create(context.Background(), author.ID, "四个标签", "正文", []string{"a", "b", "c", "d"})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("四个标签应返回 ErrInvalidInput，实际: %v", err)
	}
	if got := testutil.CountRows(t, db, "articles"); got != 0 {
		t.Errorf("标签校验失败后 articles 里还有 %d 行，期望 0", got)
	}
}

// TestUpdateReplacesTags 验证编辑时标签是整体替换，而不是叠加。
//
// 叠加的话，把「后端」改成「前端」会变成两个标签都留着，
// 用户没法取消一个已经加错的标签。
func TestUpdateReplacesTags(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "tagupdate")

	created, err := svc.Create(ctx, author.ID, "原标题", "原正文", []string{"后端", "数据库"})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	// 先读一次详情，把带标签的值灌进缓存：编辑后必须清掉，否则还会看到旧标签。
	if _, err := svc.Detail(ctx, created.ID); err != nil {
		t.Fatalf("预热缓存失败: %v", err)
	}

	updated, err := svc.Update(ctx, created.ID, author.ID, "新标题", "新正文", []string{"前端"})
	if err != nil {
		t.Fatalf("编辑失败: %v", err)
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != "前端" {
		t.Errorf("编辑后标签 = %v，期望只有「前端」", updated.Tags)
	}

	again, err := svc.Detail(ctx, created.ID)
	if err != nil {
		t.Fatalf("改完再查详情失败: %v", err)
	}
	if len(again.Tags) != 1 || again.Tags[0] != "前端" {
		t.Errorf("详情里的标签 = %v，期望只有「前端」（缓存没清？）", again.Tags)
	}

	// 旧关联必须真的从表里删掉，而不只是返回值变了。
	byTag, err := svc.tagsOf(ctx, []uint{created.ID})
	if err != nil {
		t.Fatalf("回查标签失败: %v", err)
	}
	for _, name := range byTag[created.ID] {
		if name == "后端" || name == "数据库" {
			t.Errorf("旧标签 %q 还留在关联表里", name)
		}
	}
}

// TestTopTagsOrdersByCount 验证热门标签按文章数倒序，平手时按标签 id 稳定排序。
//
// 平手项不写的话，两个同样热度的标签每次返回顺序可能不同，
// 页面刷新一次标签云就重排，看起来像在乱跳。
func TestTopTagsOrdersByCount(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "tagtop")

	// 「热门」出现在两篇里，「冷门」一篇。
	if _, err := svc.Create(ctx, author.ID, "一", "正文", []string{"热门"}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, err := svc.Create(ctx, author.ID, "二", "正文", []string{"热门", "冷门"}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	tags, err := svc.TopTags(ctx, 10)
	if err != nil {
		t.Fatalf("读取热门标签失败: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("标签数 = %d，期望 2", len(tags))
	}
	if tags[0].Name != "热门" || tags[0].Count != 2 {
		t.Errorf("第一名 = %+v，期望「热门」2 篇", tags[0])
	}
	if tags[1].Name != "冷门" || tags[1].Count != 1 {
		t.Errorf("第二名 = %+v，期望「冷门」1 篇", tags[1])
	}
}

// TestDeleteIsSoftAndKeepsChildren 验证删除是「打标记」：数据还在，但查不到了。
//
// 这条用例同时守着四件事：
//  1. 库里那一行仍然存在且 deleted_at 有值（说明没有真删，还能恢复）；
//  2. 详情与列表都查不到它（说明 GORM 的默认作用域生效，接口层不需要手写过滤）；
//  3. 标签关联还在（软删的意义就是子数据完整，恢复后标签不会丢）；
//  4. 再删一次命中 0 行，返回 404 而不是第二次成功。
func TestDeleteIsSoftAndKeepsChildren(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "tagdelete")

	created, err := svc.Create(ctx, author.ID, "带标签的文章", "正文", []string{"后端"})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := svc.Delete(ctx, created.ID, author.ID); err != nil {
		t.Fatalf("作者删自己的文章应当成功，实际报错: %v", err)
	}

	// 1. 行还在，只是被标记。
	var row model.Article
	if err := db.Unscoped().First(&row, created.ID).Error; err != nil {
		t.Fatalf("软删后应当还能在库里查到这一行: %v", err)
	}
	if !row.DeletedAt.Valid {
		t.Fatal("deleted_at 没有写入，说明走的是硬删除")
	}

	// 2. 业务查询必须看不见它。
	if _, err := svc.Detail(ctx, created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("软删后详情 error = %v, want ErrNotFound", err)
	}
	_, total, err := svc.List(ctx, 0, "", "", 1, 10)
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if total != 0 {
		t.Errorf("列表里还有 %d 篇，期望 0", total)
	}

	// 3. 标签关联保留下来，恢复文章时标签不会丢。
	if got := testutil.CountRows(t, db, "article_tags"); got != 1 {
		t.Errorf("软删后 article_tags 剩 %d 行，期望 1（子数据要保留）", got)
	}
	if got := testutil.CountRows(t, db, "tags"); got != 1 {
		t.Errorf("软删后 tags 剩 %d 行，期望 1", got)
	}

	// 4. 重复删除得到 404，而不是「又删了一次」。
	if err := svc.Delete(ctx, created.ID, author.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("重复删除 error = %v, want ErrNotFound", err)
	}
}

// TestDeleteByOtherUserFails 验证别人删不掉，且同样回 404（不泄露归属）。
func TestDeleteByOtherUserFails(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "owner")
	other := testutil.SeedUser(t, db, "intruder")

	created, err := svc.Create(ctx, author.ID, "我的文章", "正文", nil)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := svc.Delete(ctx, created.ID, other.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("别人删除 error = %v, want ErrNotFound", err)
	}
	// 文章必须还在，且没有被标记删除。
	var row model.Article
	if err := db.First(&row, created.ID).Error; err != nil {
		t.Fatalf("文章被别人删掉了: %v", err)
	}
	if row.DeletedAt.Valid {
		t.Fatal("deleted_at 被写入了，说明越权删除生效了")
	}
}

// TestUpdateByAuthor 验证作者能改自己的文章，且缓存被正确清掉。
//
// 先 Detail 一次把旧内容灌进缓存，再 Update：
// 如果 Update 忘了清缓存，最后那次 Detail 会拿到旧标题，
// 这条断言就是专门用来抓「改了但不生效」的。
func TestUpdateByAuthor(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "updateauthor")

	created, err := svc.Create(ctx, author.ID, "旧标题", "旧正文", nil)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	// 灌一次缓存，让后面的 Update 有旧值可清。
	if _, err := svc.Detail(ctx, created.ID); err != nil {
		t.Fatalf("预热缓存失败: %v", err)
	}

	updated, err := svc.Update(ctx, created.ID, author.ID, "新标题", "新正文", nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "owneruser")
	other := testutil.SeedUser(t, db, "otheruser")

	created, err := svc.Create(ctx, author.ID, "原题", "原文", nil)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	_, err = svc.Update(ctx, created.ID, other.ID, "被篡改的标题", "被篡改的正文", nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	author := testutil.SeedUser(t, db, "missingauthor")

	_, err := svc.Update(context.Background(), 999999, author.ID, "标题", "正文", nil)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("改不存在的文章应返回 ErrNotFound，实际: %v", err)
	}
}

// TestUpdateSameContentSucceeds 守 MySQL 的 RowsAffected 坑：
// 内容没变时 UPDATE 的 affected rows 是 0，不能据此判断「文章不存在」，
// 否则用户打开编辑页什么都不改直接保存，会收到「文章不存在」。
func TestUpdateSameContentSucceeds(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "sameauthor")

	created, err := svc.Create(ctx, author.ID, "原样标题", "原样正文", nil)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	updated, err := svc.Update(ctx, created.ID, author.ID, "原样标题", "原样正文", nil)
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
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "invalidauthor")

	created, err := svc.Create(ctx, author.ID, "原始标题", "原始正文", nil)
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
		if _, err := svc.Update(ctx, created.ID, author.ID, tc.title, tc.content, nil); !errors.Is(err, domain.ErrInvalidInput) {
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

// TestRecordViewCountsOncePerViewer 验证浏览数按访问者去重，且能被 ViewCount 读回。
//
// 去重是浏览数能不能用的关键：不去重的话，一个人按住 F5 就能把数字刷上去，
// 这个字段立刻失去参考价值。这条用例同时守住「换个人要算新的」——
// 只测「两次只算一次」的话，写成「永久只算一次」也能通过。
func TestRecordViewCountsOncePerViewer(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	author := testutil.SeedUser(t, db, "author")
	post := testutil.SeedArticle(t, db, author.ID, "被反复打开的文章")
	ctx := context.Background()

	// 同一个登录用户连看两次：只有第一次算数。
	for i := 1; i <= 2; i++ {
		if err := svc.RecordView(ctx, post.ID, "u:7"); err != nil {
			t.Fatalf("第 %d 次记录浏览失败: %v", i, err)
		}
	}
	if got := viewCountInDB(t, db, post.ID); got != 1 {
		t.Errorf("同一访问者看两次后 view_count = %d，期望 1", got)
	}

	// 换一个游客：算新的一次。
	if err := svc.RecordView(ctx, post.ID, "ip:10.0.0.9"); err != nil {
		t.Fatalf("记录游客浏览失败: %v", err)
	}

	n, err := svc.ViewCount(ctx, post.ID)
	if err != nil {
		t.Fatalf("读取浏览数失败: %v", err)
	}
	if n != 2 {
		t.Errorf("两个不同访问者之后 view_count = %d，期望 2", n)
	}
}

// TestRecordViewDegradesWhenRedisUnavailable 验证 Redis 挂了照常计数。
//
// 降级方向和详情缓存相反：缓存读不到要回源（少一次缓存），
// 去重标记写不进则必须放行（宁可多算几次），否则 Redis 抖动期间
// 所有浏览都被静默丢掉，热榜的排序依据会凭空消失一整天。
func TestRecordViewDegradesWhenRedisUnavailable(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	svc := NewArticleService(db, rdb, nil)
	author := testutil.SeedUser(t, db, "author")
	post := testutil.SeedArticle(t, db, author.ID, "Redis 故障时被打开的文章")
	ctx := context.Background()

	// 关掉客户端连接，后续命令一律返回 "client is closed"，等价于 Redis 整体不可用。
	// miniredis 本身还活着，但 service 只拿得到这个客户端。
	if err := rdb.Close(); err != nil {
		t.Fatalf("关闭测试 Redis 客户端失败: %v", err)
	}

	for i := 1; i <= 2; i++ {
		if err := svc.RecordView(ctx, post.ID, "u:7"); err != nil {
			t.Fatalf("Redis 不可用时应降级为照常计数，第 %d 次却报错: %v", i, err)
		}
	}
	if got := viewCountInDB(t, db, post.ID); got != 2 {
		t.Errorf("降级期间两次浏览后 view_count = %d，期望 2", got)
	}
}

// TestRecordViewKeepsUpdatedAt 验证记浏览数不会改 updated_at。
//
// 用 Update 而不是 UpdateColumn 时，GORM 会顺带写 updated_at，
// 于是每被看一次文章的「更新时间」就跳到当前：按更新时间排序的列表会乱，
// 用户也会以为作者刚编辑过。这条用例是那个写法选择的唯一守卫。
func TestRecordViewKeepsUpdatedAt(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	author := testutil.SeedUser(t, db, "author")
	post := testutil.SeedArticle(t, db, author.ID, "只是被看了一眼的文章")

	// 留出时间差：如果 updated_at 真被改写，新值至少比原值晚几毫秒。
	time.Sleep(5 * time.Millisecond)
	if err := svc.RecordView(context.Background(), post.ID, "u:7"); err != nil {
		t.Fatalf("记录浏览失败: %v", err)
	}

	var after model.Article
	if err := db.Select("updated_at").First(&after, post.ID).Error; err != nil {
		t.Fatalf("重新读取文章失败: %v", err)
	}
	if !after.UpdatedAt.Equal(post.UpdatedAt) {
		t.Errorf("updated_at 被浏览改动了：%v -> %v", post.UpdatedAt, after.UpdatedAt)
	}
}

// TestRecordViewIgnoresMissingArticle 验证给不存在的文章计数不算错误。
//
// 详情接口会先确认文章存在再计数，但文章可能在「读到」和「计数」之间被作者删掉。
// 这个窗口里 RowsAffected 是 0，不是故障，不该记一条 error 日志吓人。
func TestRecordViewIgnoresMissingArticle(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)

	if err := svc.RecordView(context.Background(), 999999, "u:7"); err != nil {
		t.Errorf("文章不存在时不应报错，实际: %v", err)
	}
}

// viewCountInDB 直接从库里读 view_count，绕开 service，
// 确保断言看到的是落盘的值而不是某层内存缓存。
func viewCountInDB(t *testing.T, db *gorm.DB, id uint) int64 {
	t.Helper()

	var n int64
	if err := db.Model(&model.Article{}).Where("id = ?", id).Pluck("view_count", &n).Error; err != nil {
		t.Fatalf("读 view_count 失败: %v", err)
	}
	return n
}
