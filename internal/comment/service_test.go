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

	created, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, "写得不错")
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

	created, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, "  前后都有空格  ")
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
			_, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, tc.content)
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

	_, err := f.svc.Create(context.Background(), 999999, f.commenter.ID, "评论一篇不存在的文章")
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

	_, err := f.svc.Create(context.Background(), f.article.ID, f.commenter.ID, "事件写不进去")
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
		if _, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, fmt.Sprintf("第 %d 条", i)); err != nil {
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
	if page1[0].Content != "第 1 条" || page1[1].Content != "第 2 条" {
		t.Errorf("第一页顺序不对: %q, %q", page1[0].Content, page1[1].Content)
	}
	// Preload("User") 必须把作者带出来，否则前端只能显示空白昵称。
	if page1[0].User.Username != "commenter" {
		t.Errorf("评论作者 = %q，期望 commenter", page1[0].User.Username)
	}

	// 第二页只剩一条。
	page2, _, err := f.svc.List(ctx, f.article.ID, 2, 2)
	if err != nil {
		t.Fatalf("查询第二页失败: %v", err)
	}
	if len(page2) != 1 || page2[0].Content != "第 3 条" {
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

	created, err := f.svc.Create(ctx, f.article.ID, f.commenter.ID, "这条会被删")
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
