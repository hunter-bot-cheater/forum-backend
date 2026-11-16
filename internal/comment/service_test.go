package comment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/event"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
)

// commentFixture 是一个用例跑起来需要的全部现场：库、service、一篇文章、两个用户。
// 抽成结构体而不是五个返回值，是为了让每个用例开头那几行读起来知道自己在拿什么。
type commentFixture struct {
	db        *gorm.DB
	svc       *CommentService
	article   *model.Article // 文章作者是 author
	author    *model.User
	commenter *model.User
}

// newCommentFixture 准备干净库与固定数据。每个用例都从空表开始，互不影响。
func newCommentFixture(t *testing.T) *commentFixture {
	t.Helper()

	db := testutil.NewDB(t)
	f := &commentFixture{
		db:        db,
		svc:       NewCommentService(db),
		author:    testutil.SeedUser(t, db, "author"),
		commenter: testutil.SeedUser(t, db, "commenter"),
	}
	f.article = testutil.SeedArticle(t, db, f.author.ID, "测试文章")
	return f
}

// TestCreateWritesCommentAndEvent 验证创建评论时，评论与领域事件在同一个事务里各落一行。
// 这条是 Transactional Outbox 的地基：事件必须和业务数据同生共死。
func TestCreateWritesCommentAndEvent(t *testing.T) {
	f := newCommentFixture(t)

	created, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, 0, "写得不错")
	if err != nil {
		t.Fatalf("创建评论失败: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("创建后 ID 应当被回填，实际是 0")
	}
	if created.Content != "写得不错" {
		t.Errorf("评论内容 = %q", created.Content)
	}

	// outbox 只能有一行：这条事件对应一次评论动作，多出来就说明写重了。
	var rows []model.OutboxEvent
	if err := f.db.Find(&rows).Error; err != nil {
		t.Fatalf("查询 outbox 失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("outbox 应当只有 1 行，实际 %d 行", len(rows))
	}
	row := rows[0]
	if row.EventType != event.TypeCommentCreated {
		t.Errorf("事件类型 = %q，期望 %q", row.EventType, event.TypeCommentCreated)
	}
	// 聚合类型与聚合 ID 决定消费端去哪张表回查收件人。
	if row.AggregateType != event.TargetArticle || row.AggregateID != f.article.ID {
		t.Errorf("聚合 = %s/%d，期望 %s/%d", row.AggregateType, row.AggregateID, event.TargetArticle, f.article.ID)
	}
	// 刚写进去的事件必须是 pending，relay 才会扫到它。
	if row.Status != model.OutboxPending {
		t.Errorf("事件状态 = %q，期望 %q", row.Status, model.OutboxPending)
	}

	payload, err := event.Decode(row.Payload)
	if err != nil {
		t.Fatalf("解析事件体失败: %v", err)
	}
	if payload.ActorID != f.commenter.ID {
		t.Errorf("ActorID = %d，期望评论者 %d", payload.ActorID, f.commenter.ID)
	}
	if payload.Target.Type != event.TargetArticle || payload.Target.ID != f.article.ID {
		t.Errorf("Target = %+v，期望 article/%d", payload.Target, f.article.ID)
	}
	// Created 是通知点进去要定位的那条评论，缺了就点不到。
	if payload.Created == nil {
		t.Fatal("评论事件的 Created 不能为空")
	}
	if payload.Created.Type != event.TargetComment || payload.Created.ID != created.ID {
		t.Errorf("Created = %+v，期望 comment/%d", payload.Created, created.ID)
	}
}

// TestCreateTrimsContent 验证前后空白被去掉后再落库，避免「内容其实为空但长度不为 0」的脏数据。
func TestCreateTrimsContent(t *testing.T) {
	f := newCommentFixture(t)

	created, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, 0, "  前后都有空格  ")
	if err != nil {
		t.Fatalf("创建评论失败: %v", err)
	}
	if created.Content != "前后都有空格" {
		t.Errorf("落库内容 = %q，期望已去掉前后空白", created.Content)
	}
}

// TestCreateRejectsInvalidContent 覆盖三种非法内容：空、全空白、超长。
// 三种都必须在写库之前被挡掉，所以顺带断言两张表都没有新增行。
func TestCreateRejectsInvalidContent(t *testing.T) {
	f := newCommentFixture(t)

	cases := []struct {
		name    string
		content string
	}{
		{name: "空字符串", content: ""},
		{name: "只有空白", content: "   \n\t  "},
		{name: "超过一万字节", content: strings.Repeat("啊", 10001)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, 0, tc.content)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("错误 = %v，期望 ErrInvalidInput", err)
			}
			if got := testutil.CountRows(t, f.db, "comments"); got != 0 {
				t.Errorf("非法内容不应写库，comments 有 %d 行", got)
			}
			if got := testutil.CountRows(t, f.db, "outbox_events"); got != 0 {
				t.Errorf("非法内容不应写事件，outbox 有 %d 行", got)
			}
		})
	}
}

// TestCreateMissingArticle 验证文章不存在时返回 ErrNotFound（而不是 gorm.ErrRecordNotFound 漏成 500）。
// 同时确认「查文章」这一步在事务之前返回，不会留下一条孤儿事件。
func TestCreateMissingArticle(t *testing.T) {
	f := newCommentFixture(t)

	_, err := f.svc.Create(context.Background(), 999999, f.commenter.ID, 0, "评论一篇不存在的文章")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误 = %v，期望 ErrNotFound", err)
	}
	if got := testutil.CountRows(t, f.db, "outbox_events"); got != 0 {
		t.Errorf("文章不存在时不该写事件，outbox 有 %d 行", got)
	}
}

// TestCreateRollsBackWhenEventFails 验证事件写不进去时，评论也一起回滚。
//
// 手法是把 outbox 表删掉来制造失败：这是最贴近真实故障的造法（比如迁移漏了这张表、
// 表被误删），比打桩更直接。代价是必须恢复表结构，否则同包后续用例清表时会报表不存在。
func TestCreateRollsBackWhenEventFails(t *testing.T) {
	f := newCommentFixture(t)

	if err := f.db.Migrator().DropTable(&model.OutboxEvent{}); err != nil {
		t.Fatalf("删除 outbox 表失败: %v", err)
	}
	t.Cleanup(func() {
		if err := f.db.AutoMigrate(&model.OutboxEvent{}); err != nil {
			t.Fatalf("恢复 outbox 表失败: %v", err)
		}
	})

	_, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, 0, "事件写不进去")
	if err == nil {
		t.Fatal("事件写不进去时 Create 必须返回错误")
	}

	// 这条断言是本用例的核心：评论已经被 INSERT 过，但事务回滚必须把它抹掉。
	// 否则就会出现「评论存在、通知却永远发不出去」的半成品状态。
	if got := testutil.CountRows(t, f.db, "comments"); got != 0 {
		t.Errorf("事务应当回滚，comments 却留下了 %d 行", got)
	}
}

// TestListPaginatesAndOrders 验证分页边界与固定排序（created_at 升序，同秒用 id 兜底）。
func TestListPaginatesAndOrders(t *testing.T) {
	f := newCommentFixture(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if _, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, 0, fmt.Sprintf("第 %d 条", i)); err != nil {
			t.Fatalf("准备第 %d 条评论失败: %v", i, err)
		}
	}

	// 第一页拿前两条，顺序必须是插入顺序。
	page1, total, err := f.svc.List(ctx, f.article.ID, 1, 2)
	if err != nil {
		t.Fatalf("查询第一页失败: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d，期望 3", total)
	}
	if len(page1) != 2 {
		t.Fatalf("第一页应当有 2 条，实际 %d 条", len(page1))
	}
	if page1[0].Comment.Content != "第 1 条" || page1[1].Comment.Content != "第 2 条" {
		t.Errorf("第一页顺序不对: %q, %q", page1[0].Comment.Content, page1[1].Comment.Content)
	}
	// Preload("User") 必须把作者带出来，否则前端只能显示空白昵称。
	if page1[0].Comment.User.Username != "commenter" {
		t.Errorf("评论作者 = %q，期望 commenter", page1[0].Comment.User.Username)
	}

	// 第二页只剩一条。
	page2, _, err := f.svc.List(ctx, f.article.ID, 2, 2)
	if err != nil {
		t.Fatalf("查询第二页失败: %v", err)
	}
	if len(page2) != 1 || page2[0].Comment.Content != "第 3 条" {
		t.Fatalf("第二页内容不对: %+v", page2)
	}

	// 页码越界时返回空切片而不是 nil，保证 JSON 里是 [] 不是 null。
	page9, total9, err := f.svc.List(ctx, f.article.ID, 9, 2)
	if err != nil {
		t.Fatalf("查询越界页失败: %v", err)
	}
	if page9 == nil {
		t.Error("越界页返回了 nil，序列化后会变成 null")
	}
	if len(page9) != 0 || total9 != 3 {
		t.Errorf("越界页 = %d 条 / total %d，期望 0 条 / total 3", len(page9), total9)
	}
}

// TestListEmptyWhenNoComments 验证没有任何评论时返回空切片与非零错误 nil。
func TestListEmptyWhenNoComments(t *testing.T) {
	f := newCommentFixture(t)

	list, total, err := f.svc.List(context.Background(), f.article.ID, 1, 10)
	if err != nil {
		t.Fatalf("查询空列表失败: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Errorf("total = %d, len = %d，期望都是 0", total, len(list))
	}
	if list == nil {
		t.Error("空列表应当是空切片，不能是 nil")
	}
}

// TestListRejectsInvalidPaging 验证分页参数在 service 层就被挡住。
// 即便将来有内部调用绕过 handler，也不会把 pageSize=500 的请求打到数据库。
func TestListRejectsInvalidPaging(t *testing.T) {
	f := newCommentFixture(t)

	cases := []struct {
		name     string
		page     int
		pageSize int
	}{
		{name: "页码为 0", page: 0, pageSize: 10},
		{name: "页长为 0", page: 1, pageSize: 0},
		{name: "页长超过上限", page: 1, pageSize: 51},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := f.svc.List(context.Background(), f.article.ID, tc.page, tc.pageSize)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("错误 = %v，期望 ErrInvalidInput", err)
			}
		})
	}
}

// TestDeleteOnlyByOwner 验证删除权限：非作者删不掉、作者能删、重复删返回 ErrNotFound。
// 「非作者删不掉」对外必须表现为 404 而不是 403，避免泄露这条评论属于谁。
func TestDeleteOnlyByOwner(t *testing.T) {
	f := newCommentFixture(t)
	ctx := context.Background()

	created, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, 0, "这条会被删")
	if err != nil {
		t.Fatalf("准备评论失败: %v", err)
	}

	// 文章作者也不是这条评论的作者，同样无权删。
	if err := f.svc.Delete(ctx, created.ID, f.author.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("他人删除的错误 = %v，期望 ErrNotFound", err)
	}
	if got := testutil.CountRows(t, f.db, "comments"); got != 1 {
		t.Fatalf("删除被拒后评论应当还在，实际剩 %d 行", got)
	}

	// 评论作者自己能删。
	if err := f.svc.Delete(ctx, created.ID, f.commenter.ID); err != nil {
		t.Fatalf("作者删除失败: %v", err)
	}
	if got := testutil.CountRows(t, f.db, "comments"); got != 0 {
		t.Fatalf("删除后 comments 应当为 0 行，实际 %d 行", got)
	}

	// 再删一次：行已经不在了，同样合并成 ErrNotFound。
	if err := f.svc.Delete(ctx, created.ID, f.commenter.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("重复删除的错误 = %v，期望 ErrNotFound", err)
	}
}

// TestReplyBuildsTwoLevelTree 验证回复挂在根评论下，而不是自己变成一条根评论。
//
// 关键字段是 RootID：回复的回复也必须指回根评论，
// 这样一次 WHERE root_id IN (...) 就能取回整棵树。
func TestReplyBuildsTwoLevelTree(t *testing.T) {
	f := newCommentFixture(t)
	ctx := context.Background()

	root, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, 0, "根评论")
	if err != nil {
		t.Fatalf("创建根评论失败: %v", err)
	}

	reply, err := f.svc.Create(ctx, f.article.ID, f.author.ID, root.ID, "回复根评论")
	if err != nil {
		t.Fatalf("创建回复失败: %v", err)
	}
	if reply.ParentID != root.ID || reply.RootID != root.ID {
		t.Fatalf("一级回复 parent/root 期望 %d/%d，实际 %d/%d", root.ID, root.ID, reply.ParentID, reply.RootID)
	}

	// 对回复再回复：parent 指向那条回复，root 仍然是最初的根评论。
	nested, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, reply.ID, "回复那条回复")
	if err != nil {
		t.Fatalf("创建二级回复失败: %v", err)
	}
	if nested.ParentID != reply.ID {
		t.Fatalf("二级回复 parent 期望 %d，实际 %d", reply.ID, nested.ParentID)
	}
	if nested.RootID != root.ID {
		t.Fatalf("二级回复 root 期望回到根评论 %d，实际 %d", root.ID, nested.RootID)
	}
}

// TestReplyRejectsForeignParent 验证不能把回复挂到别的文章的评论上。
func TestReplyRejectsForeignParent(t *testing.T) {
	f := newCommentFixture(t)
	ctx := context.Background()

	other := testutil.SeedArticle(t, f.db, f.author.ID, "另一篇文章")
	foreign, err := f.svc.Create(ctx, other.ID, f.commenter.ID, 0, "别的文章的评论")
	if err != nil {
		t.Fatalf("准备外部评论失败: %v", err)
	}

	_, err = f.svc.Create(ctx, f.article.ID, f.author.ID, foreign.ID, "跨文章回复")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("跨文章回复期望 ErrInvalidInput，实际 %v", err)
	}
}

// TestReplyRejectsMissingParent 验证父评论不存在时返回 404 语义，而不是插一条孤儿回复。
func TestReplyRejectsMissingParent(t *testing.T) {
	f := newCommentFixture(t)

	_, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, 999999, "回复不存在的评论")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("父评论不存在期望 ErrNotFound，实际 %v", err)
	}
}

// TestReplyEventTargetsParentComment 验证事件的 Target 是被回复的评论。
// 通知消费端靠这个字段判断收件人是「评论作者」还是「文章作者」。
func TestReplyEventTargetsParentComment(t *testing.T) {
	f := newCommentFixture(t)
	ctx := context.Background()

	root, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, 0, "根评论")
	if err != nil {
		t.Fatalf("创建根评论失败: %v", err)
	}
	if _, err := f.svc.Create(ctx, f.article.ID, f.author.ID, root.ID, "回复"); err != nil {
		t.Fatalf("创建回复失败: %v", err)
	}

	// 两条事件：根评论那条指向文章，回复那条指向根评论。
	var rows []model.OutboxEvent
	if err := f.db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("查询 outbox 失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("outbox 期望 2 行，实际 %d 行", len(rows))
	}
	if rows[0].AggregateType != event.TargetArticle || rows[0].AggregateID != f.article.ID {
		t.Fatalf("根评论事件聚合期望 article/%d，实际 %s/%d", f.article.ID, rows[0].AggregateType, rows[0].AggregateID)
	}
	if rows[1].AggregateType != event.TargetComment || rows[1].AggregateID != root.ID {
		t.Fatalf("回复事件聚合期望 comment/%d，实际 %s/%d", root.ID, rows[1].AggregateType, rows[1].AggregateID)
	}
}

// TestListGroupsRepliesUnderRoot 验证列表把回复归到对应根评论下，并带上被回复者。
func TestListGroupsRepliesUnderRoot(t *testing.T) {
	f := newCommentFixture(t)
	ctx := context.Background()

	// 给回复者设置昵称，用来断言 reply_to 取的是展示名而不是 id。
	if err := f.db.Model(f.commenter).Update("nickname", "评论者").Error; err != nil {
		t.Fatalf("更新昵称失败: %v", err)
	}

	root, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, 0, "根评论")
	if err != nil {
		t.Fatalf("创建根评论失败: %v", err)
	}
	first, err := f.svc.Create(ctx, f.article.ID, f.author.ID, root.ID, "第一条回复")
	if err != nil {
		t.Fatalf("创建第一条回复失败: %v", err)
	}
	if _, err := f.svc.Create(ctx, f.article.ID, f.author.ID, root.ID, "第二条回复"); err != nil {
		t.Fatalf("创建第二条回复失败: %v", err)
	}

	threads, total, err := f.svc.List(ctx, f.article.ID, 1, 10)
	if err != nil {
		t.Fatalf("查询列表失败: %v", err)
	}
	// 分页只数根评论：两条回复不该让 total 变成 3。
	if total != 1 {
		t.Fatalf("total 期望 1（只数根评论），实际 %d", total)
	}
	if len(threads) != 1 {
		t.Fatalf("threads 期望 1，实际 %d", len(threads))
	}
	thread := threads[0]
	if thread.Comment.ID != root.ID {
		t.Fatalf("根评论 id 期望 %d，实际 %d", root.ID, thread.Comment.ID)
	}
	if thread.ReplyTotal != 2 || len(thread.Replies) != 2 {
		t.Fatalf("回复期望 2 条，实际 total %d / 本页 %d", thread.ReplyTotal, len(thread.Replies))
	}
	if thread.Replies[0].Comment.ID != first.ID {
		t.Fatalf("回复应按 id 升序，首条期望 %d，实际 %d", first.ID, thread.Replies[0].Comment.ID)
	}
	// 被回复者是根评论的作者，昵称已设置，reply_to 必须是回退后的展示名。
	if thread.Replies[0].ReplyTo.DisplayName() != "评论者" {
		t.Fatalf("reply_to 期望 评论者，实际 %q", thread.Replies[0].ReplyTo.DisplayName())
	}
}

// TestDeleteRootRemovesReplies 验证删根评论会连回复一起删，不留孤儿。
func TestDeleteRootRemovesReplies(t *testing.T) {
	f := newCommentFixture(t)
	ctx := context.Background()

	root, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, 0, "根评论")
	if err != nil {
		t.Fatalf("创建根评论失败: %v", err)
	}
	if _, err := f.svc.Create(ctx, f.article.ID, f.author.ID, root.ID, "回复"); err != nil {
		t.Fatalf("创建回复失败: %v", err)
	}

	if err := f.svc.Delete(ctx, root.ID, f.commenter.ID); err != nil {
		t.Fatalf("删除根评论失败: %v", err)
	}

	var left int64
	if err := f.db.Model(&model.Comment{}).Count(&left).Error; err != nil {
		t.Fatalf("统计剩余评论失败: %v", err)
	}
	if left != 0 {
		t.Fatalf("删根评论后应连回复一起删，实际还剩 %d 条", left)
	}
}

// TestRepliesCappedPerRoot 验证回复上限是「每条根评论各自 200 条」。
//
// 早先的实现是整页共用一个 Limit(len(roots) * maxRepliesPerRoot)：
// 一条热帖的 400 多条回复会把整页配额吃光，排在它后面的根评论一条回复都带不回来，
// 而 reply_count 仍然显示 1，前端点开「查看全部」看到的是空列表。
//
// 直接批量插回复而不走 svc.Create：400 多次发评论会各写一条 outbox，
// 这个用例要验的是列表查询，不是写入链路。
func TestRepliesCappedPerRoot(t *testing.T) {
	f := newCommentFixture(t)
	ctx := context.Background()

	hot, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, 0, "热帖")
	if err != nil {
		t.Fatalf("创建热帖失败: %v", err)
	}
	quiet, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, 0, "冷清的那条")
	if err != nil {
		t.Fatalf("创建第二条根评论失败: %v", err)
	}

	replies := make([]*model.Comment, 0, 401)
	for i := 0; i < 401; i++ {
		replies = append(replies, &model.Comment{
			ArticleID: f.article.ID,
			UserID:    f.author.ID,
			Content:   "热帖下的回复",
			ParentID:  hot.ID,
			RootID:    hot.ID,
		})
	}
	if err := f.db.CreateInBatches(replies, 200).Error; err != nil {
		t.Fatalf("批量插入回复失败: %v", err)
	}

	// 这条回复的 id 排在第 402 位：用全局配额时它一定被挤掉。
	if _, err := f.svc.Create(ctx, f.article.ID, f.author.ID, quiet.ID, "唯一一条回复"); err != nil {
		t.Fatalf("给第二条根评论写回复失败: %v", err)
	}

	threads, total, err := f.svc.List(ctx, f.article.ID, 1, 10)
	if err != nil {
		t.Fatalf("查询列表失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("根评论总数期望 2，实际 %d", total)
	}

	var hotThread, quietThread *Thread
	for i := range threads {
		thread := &threads[i]
		if thread.Comment.ID == hot.ID {
			hotThread = thread
		}
		if thread.Comment.ID == quiet.ID {
			quietThread = thread
		}
	}
	if hotThread == nil || quietThread == nil {
		t.Fatalf("两条根评论都该在结果里，实际拿到 %d 条", len(threads))
	}

	// 热帖：本页最多 200 条，但总数必须是真实的 401。
	if len(hotThread.Replies) != maxRepliesPerRoot {
		t.Fatalf("热帖本页回复期望 %d 条，实际 %d", maxRepliesPerRoot, len(hotThread.Replies))
	}
	if hotThread.ReplyTotal != 401 {
		t.Fatalf("热帖回复总数期望 401，实际 %d", hotThread.ReplyTotal)
	}

	// 冷清那条：配额被热帖吃光时这里会是 0，正是这个用例要守的回归点。
	if len(quietThread.Replies) != 1 {
		t.Fatalf("第二条根评论应带回 1 条回复，实际 %d 条（配额被别的根评论吃光了）", len(quietThread.Replies))
	}
	if quietThread.ReplyTotal != 1 {
		t.Fatalf("第二条根评论回复总数期望 1，实际 %d", quietThread.ReplyTotal)
	}
}

// TestLocateComment 验证「从通知点进来落到那条评论」的定位结果。
//
// 这个接口的产物是序号而不是内容，序号错一位前端就翻错页、落到空的评论区，
// 所以三种情况都要钉住：根评论定位到自己、回复定位到所属根评论、序号按文章各算各的。
func TestLocateComment(t *testing.T) {
	f := newCommentFixture(t)
	ctx := context.Background()

	// 第一条根评论下面挂一条回复：它的序号必须是所在根的序号，不是它自己的出现顺序。
	first, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, 0, "第一条")
	if err != nil {
		t.Fatalf("创建第一条根评论失败: %v", err)
	}
	reply, err := f.svc.Create(ctx, f.article.ID, f.author.ID, first.ID, "回复第一条")
	if err != nil {
		t.Fatalf("创建回复失败: %v", err)
	}
	second, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, 0, "第二条")
	if err != nil {
		t.Fatalf("创建第二条根评论失败: %v", err)
	}

	// 另一篇文章里的评论有它自己的序号：漏了 article_id 过滤就会把别处的根评论算进来。
	other := testutil.SeedArticle(t, f.db, f.author.ID, "另一篇文章")
	otherComment, err := f.svc.Create(ctx, other.ID, f.commenter.ID, 0, "别处的评论")
	if err != nil {
		t.Fatalf("创建另一篇文章的评论失败: %v", err)
	}

	cases := []struct {
		name      string
		id        uint
		articleID uint
		wantRoot  uint
		wantIndex int64
	}{
		{"根评论定位到自己", second.ID, f.article.ID, second.ID, 2},
		{"回复定位到所属根评论", reply.ID, f.article.ID, first.ID, 1},
		{"另一篇文章的评论单独计数", otherComment.ID, other.ID, otherComment.ID, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			anchor, err := f.svc.Locate(ctx, c.id)
			if err != nil {
				t.Fatalf("定位评论 %d 失败: %v", c.id, err)
			}
			if anchor.CommentID != c.id {
				t.Errorf("CommentID = %d，期望 %d", anchor.CommentID, c.id)
			}
			if anchor.ArticleID != c.articleID {
				t.Errorf("ArticleID = %d，期望 %d", anchor.ArticleID, c.articleID)
			}
			if anchor.RootID != c.wantRoot {
				t.Errorf("RootID = %d，期望 %d", anchor.RootID, c.wantRoot)
			}
			if anchor.Index != c.wantIndex {
				t.Errorf("Index = %d，期望 %d", anchor.Index, c.wantIndex)
			}
		})
	}

	// 评论被删（或从未存在）时要能翻成 404，而不是回一个 Index=0 的假位置，
	// 否则前端会安静地拉第一页然后什么都不高亮。
	if _, err := f.svc.Locate(ctx, 9999999); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("定位不存在的评论应返回 ErrNotFound，实际 %v", err)
	}
}
