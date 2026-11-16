package notification

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
	"forum-backend/internal/user"
)

// notifyFixture 是一个用例需要的现场：库、service、一个收件人和两个触发者。
// 两个人分别代表「点赞的人」和「评论的人」，用来验证列表按 id 倒序且各自带上正确的人。
type notifyFixture struct {
	db    *gorm.DB
	svc   *NotificationService
	owner *model.User
	alice *model.User
	bob   *model.User
}

// newNotifyFixture 每个用例都从空表开始，避免上一个用例的通知影响未读计数。
func newNotifyFixture(t *testing.T) *notifyFixture {
	t.Helper()

	db := testutil.NewDB(t)
	return &notifyFixture{
		db:    db,
		svc:   NewNotificationService(db, user.NewUserService(db)),
		owner: testutil.SeedUser(t, db, "owner"),
		alice: testutil.SeedUser(t, db, "alice"),
		bob:   testutil.SeedUser(t, db, "bob"),
	}
}

// seedNotification 直接插一条通知，只填列表接口会读到的字段。
// isRead 单独传参：未读数是 List 的另一个返回值，用例需要能构造已读与未读混合的现场。
func seedNotification(t *testing.T, db *gorm.DB, receiverID, actorID uint, eventID, eventType string, articleID uint, isRead bool) *model.Notification {
	t.Helper()

	notice := &model.Notification{
		UserID:     receiverID,
		EventID:    eventID,
		ActorID:    actorID,
		Type:       eventType,
		ArticleID:  articleID,
		TargetType: "article",
		TargetID:   articleID,
		IsRead:     isRead,
	}
	if err := db.Create(notice).Error; err != nil {
		t.Fatalf("插入测试通知 %s 失败: %v", eventID, err)
	}
	return notice
}

// TestListAttachesActor 验证列表把 actor_id 翻译成了昵称头像，且顺序按 id 倒序。
//
// 这条是消息中心的核心：只回 actor_id 时前端画不出「谁赞了你」，
// 而顺序错了会让分页看起来像漏数据。
func TestListAttachesActor(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	// 给 bob 设昵称和头像，用来区分「昵称」和「用户名回退」两条路径。
	if err := f.db.Model(f.bob).Updates(map[string]any{
		"nickname":   "鲍勃",
		"avatar_url": "https://example.com/bob.png",
	}).Error; err != nil {
		t.Fatalf("更新 bob 资料失败: %v", err)
	}

	first := seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-1", "like.created", 7, false)
	second := seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-2", "comment.created", 7, false)

	items, total, unread, err := f.svc.List(ctx, f.owner.ID, "", 1, 10)
	if err != nil {
		t.Fatalf("List 返回错误: %v", err)
	}
	if total != 2 || unread != 2 {
		t.Fatalf("total/unread 期望 2/2，实际 %d/%d", total, unread)
	}
	if len(items) != 2 {
		t.Fatalf("列表长度期望 2，实际 %d", len(items))
	}

	// 按 id 倒序：后插入的 bob 那条排在前面。
	if items[0].Notice.ID != second.ID {
		t.Fatalf("首条应为最新通知 id=%d，实际 %d", second.ID, items[0].Notice.ID)
	}
	if items[0].Actor.DisplayName() != "鲍勃" {
		t.Fatalf("首条 actor 昵称期望 鲍勃，实际 %q", items[0].Actor.DisplayName())
	}
	if items[0].Actor.AvatarURL != "https://example.com/bob.png" {
		t.Fatalf("首条 actor 头像不对: %q", items[0].Actor.AvatarURL)
	}

	// alice 没设昵称，回退成用户名。
	if items[1].Notice.ID != first.ID {
		t.Fatalf("次条应为 id=%d，实际 %d", first.ID, items[1].Notice.ID)
	}
	if items[1].Actor.DisplayName() != "alice" {
		t.Fatalf("空昵称应回退用户名，实际 %q", items[1].Actor.DisplayName())
	}
}

// TestListMissingActorIsZero 验证触发者账号已注销时列表仍能返回。
// 这类历史数据不能把整页请求打成 500，Actor 保持零值由 DTO 层省掉字段。
func TestListMissingActorIsZero(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-gone", "like.created", 3, false)

	// 模拟注销：删掉触发者，通知行按设计继续保留（没有外键）。
	if err := f.db.Delete(&model.User{}, f.alice.ID).Error; err != nil {
		t.Fatalf("删除 alice 失败: %v", err)
	}

	items, _, _, err := f.svc.List(ctx, f.owner.ID, "", 1, 10)
	if err != nil {
		t.Fatalf("List 不应因 actor 缺失报错: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("列表长度期望 1，实际 %d", len(items))
	}
	if items[0].Actor.ID != 0 {
		t.Fatalf("缺失的 actor 应为零值，实际 id=%d", items[0].Actor.ID)
	}
	if items[0].Notice.ActorID != f.alice.ID {
		t.Fatalf("通知本身的 actor_id 应保留原值 %d，实际 %d", f.alice.ID, items[0].Notice.ActorID)
	}
}

// TestListInvalidPagination 验证分页参数越界直接拒绝，且不查库。
func TestListInvalidPagination(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	cases := []struct {
		name     string
		page     int
		pageSize int
	}{
		{name: "页码为 0", page: 0, pageSize: 10},
		{name: "页大小为 0", page: 1, pageSize: 0},
		{name: "页大小超过上限", page: 1, pageSize: 51},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := f.svc.List(ctx, f.owner.ID, "", tc.page, tc.pageSize)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("期望 ErrInvalidInput，实际 %v", err)
			}
		})
	}
}

// TestListOnlyOwnNotifications 验证不会串号：别人的通知既不出现在列表里，也不计入未读。
func TestListOnlyOwnNotifications(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-own-1", "like.created", 1, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-own-2", "comment.created", 2, true)
	seedNotification(t, f.db, f.alice.ID, f.bob.ID, "evt-other", "like.created", 3, false)

	items, total, unread, err := f.svc.List(ctx, f.owner.ID, "", 1, 10)
	if err != nil {
		t.Fatalf("List 返回错误: %v", err)
	}
	if total != 2 {
		t.Fatalf("total 期望 2，实际 %d", total)
	}
	if unread != 1 {
		t.Fatalf("unread 期望 1（另一条已读），实际 %d", unread)
	}
	for _, item := range items {
		if item.Notice.UserID != f.owner.ID {
			t.Fatalf("列表混入了别人的通知 user_id=%d", item.Notice.UserID)
		}
	}
}

// TestListSecondPage 验证翻页只影响数据、不影响总数与未读数。
// 复用带 Offset 的查询链去 Count 是常见写法错误，这里用第二页把那个错误挡在门外。
func TestListSecondPage(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		eventID := fmt.Sprintf("evt-page-%d", i)
		seedNotification(t, f.db, f.owner.ID, f.alice.ID, eventID, "like.created", uint(i), false)
	}

	items, total, unread, err := f.svc.List(ctx, f.owner.ID, "", 2, 2)
	if err != nil {
		t.Fatalf("List 返回错误: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("第二页期望 1 条，实际 %d", len(items))
	}
	if total != 3 {
		t.Fatalf("total 不应被分页影响，期望 3，实际 %d", total)
	}
	if unread != 3 {
		t.Fatalf("unread 不应被分页影响，期望 3，实际 %d", unread)
	}
}

// TestMarkAllReadOnlyMine 验证「全部已读」只改自己的，且重复调用返回 0。
func TestMarkAllReadOnlyMine(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-read-1", "like.created", 1, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-read-2", "comment.created", 2, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-read-3", "like.created", 3, true)
	seedNotification(t, f.db, f.alice.ID, f.bob.ID, "evt-read-other", "like.created", 4, false)

	affected, err := f.svc.MarkAllRead(ctx, f.owner.ID)
	if err != nil {
		t.Fatalf("MarkAllRead 返回错误: %v", err)
	}
	if affected != 2 {
		t.Fatalf("受影响行数期望 2（已读那条不该再算），实际 %d", affected)
	}

	// 幂等：再调一次没有未读可改。
	again, err := f.svc.MarkAllRead(ctx, f.owner.ID)
	if err != nil {
		t.Fatalf("第二次 MarkAllRead 返回错误: %v", err)
	}
	if again != 0 {
		t.Fatalf("第二次受影响行数期望 0，实际 %d", again)
	}

	// alice 的通知必须还是未读，否则说明 UPDATE 漏了 user_id 条件。
	_, _, aliceUnread, err := f.svc.List(ctx, f.alice.ID, "", 1, 10)
	if err != nil {
		t.Fatalf("读取 alice 通知失败: %v", err)
	}
	if aliceUnread != 1 {
		t.Fatalf("alice 未读期望 1，实际 %d（UPDATE 可能漏了 user_id）", aliceUnread)
	}
}

// TestListFiltersByKind 验证分类过滤同时作用于列表、总数与未读数。
// 三者只要有一处漏加过滤，tab 上的红点就会和列表长度对不上。
func TestListFiltersByKind(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-like-1", "like.created", 1, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-comment-1", "comment.created", 1, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-like-2", "like.created", 2, true)

	items, total, unread, err := f.svc.List(ctx, f.owner.ID, KindLike, 1, 10)
	if err != nil {
		t.Fatalf("按分类查询失败: %v", err)
	}
	if len(items) != 2 || total != 2 || unread != 1 {
		t.Fatalf("like 分类期望 列表2/总数2/未读1，实际 %d/%d/%d", len(items), total, unread)
	}
	for _, item := range items {
		if item.Notice.Type != "like.created" {
			t.Fatalf("like 分类里混进了 %q", item.Notice.Type)
		}
	}

	// 全部（空分类）仍然能看到两类共 3 条。
	_, totalAll, unreadAll, err := f.svc.List(ctx, f.owner.ID, "", 1, 10)
	if err != nil {
		t.Fatalf("查询全部失败: %v", err)
	}
	if totalAll != 3 || unreadAll != 2 {
		t.Fatalf("全部期望 总数3/未读2，实际 %d/%d", totalAll, unreadAll)
	}
}

// TestListUnknownKind 验证未知分类被拒绝，而不是静默降级成全部。
func TestListUnknownKind(t *testing.T) {
	f := newNotifyFixture(t)

	_, _, _, err := f.svc.List(context.Background(), f.owner.ID, "wat", 1, 10)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("未知分类期望 ErrInvalidInput，实际 %v", err)
	}
}

// TestUnreadSummaryByKind 验证分类未读统计：三类都要出现，未登记类型只进总数。
func TestUnreadSummaryByKind(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-sum-1", "like.created", 1, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-sum-2", "like.created", 2, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-sum-3", "comment.created", 3, false)
	// 已读的那条不进任何计数。
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-sum-4", "like.created", 4, true)
	// 未登记分类：算总数，但不属于任何一个 tab。
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-sum-5", "floor.prize", 5, false)

	summary, err := f.svc.UnreadSummary(ctx, f.owner.ID)
	if err != nil {
		t.Fatalf("UnreadSummary 失败: %v", err)
	}
	if summary.Total != 4 {
		t.Fatalf("total 期望 4，实际 %d", summary.Total)
	}
	if summary.ByKind[KindLike] != 2 {
		t.Fatalf("like 期望 2，实际 %d", summary.ByKind[KindLike])
	}
	if summary.ByKind[KindComment] != 1 {
		t.Fatalf("comment 期望 1，实际 %d", summary.ByKind[KindComment])
	}
	if count, ok := summary.ByKind[KindFollow]; !ok || count != 0 {
		t.Fatalf("follow 应存在且为 0，实际 %v", summary.ByKind)
	}
}

// TestMarkReadIsIdempotent 验证单条已读：第一次 1 行，第二次 0 行，都不报错。
//
// 重复点击、多端同时点开同一条都会走到「已经读过」这条分支，
// 报错会让前端弹一次无意义的失败提示。
func TestMarkReadIsIdempotent(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	notice := seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-mark-1", "like.created", 1, false)

	affected, err := f.svc.MarkRead(ctx, f.owner.ID, notice.ID)
	if err != nil {
		t.Fatalf("第一次标记失败: %v", err)
	}
	if affected != 1 {
		t.Fatalf("第一次 affected 期望 1，实际 %d", affected)
	}

	affected, err = f.svc.MarkRead(ctx, f.owner.ID, notice.ID)
	if err != nil {
		t.Fatalf("重复标记不该报错: %v", err)
	}
	if affected != 0 {
		t.Fatalf("重复标记 affected 期望 0，实际 %d", affected)
	}
}

// TestMarkReadRejectsForeign 验证不能标记别人的通知，对外统一成 NotFound。
func TestMarkReadRejectsForeign(t *testing.T) {
	f := newNotifyFixture(t)

	foreign := seedNotification(t, f.db, f.bob.ID, f.alice.ID, "evt-mark-foreign", "like.created", 1, false)

	_, err := f.svc.MarkRead(context.Background(), f.owner.ID, foreign.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("标记别人的通知期望 ErrNotFound，实际 %v", err)
	}

	// 越权尝试不能真的改到数据。
	var got model.Notification
	if err := f.db.First(&got, foreign.ID).Error; err != nil {
		t.Fatalf("查对方的通知失败: %v", err)
	}
	if got.IsRead {
		t.Fatal("越权请求改到了别人的通知")
	}
}

// TestMarkReadOnlyClearsThatOne 验证单条已读不影响同用户其他未读。
// 「全部已读」才是清零，两个接口的语义不能混。
func TestMarkReadOnlyClearsThatOne(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	first := seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-one-1", "like.created", 1, false)
	seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-one-2", "comment.created", 2, false)

	if _, err := f.svc.MarkRead(ctx, f.owner.ID, first.ID); err != nil {
		t.Fatalf("标记第一条失败: %v", err)
	}

	summary, err := f.svc.UnreadSummary(ctx, f.owner.ID)
	if err != nil {
		t.Fatalf("统计未读失败: %v", err)
	}
	if summary.Total != 1 {
		t.Fatalf("标一条后总未读期望 1，实际 %d", summary.Total)
	}
	if summary.ByKind[KindLike] != 0 || summary.ByKind[KindComment] != 1 {
		t.Fatalf("分类未读不对: %v", summary.ByKind)
	}
}

// TestListAttachesArticleTitle 验证列表带出「互动发生在哪篇文章」。
//
// 没有标题时前端只能说「赞了你的文章」，用户得点进去才知道是哪篇，
// 消息中心的列表就退化成了一条条无差别的「有人赞了你」。
//
// 同时验证两个边界：非文章域事件（article_id=0）不查也不带标题；
// 文章已被删的通知保留下来，只把标题留空，不能因此让整页请求失败。
func TestListAttachesArticleTitle(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()

	// 真文章：标题要原样带出来。
	article := testutil.SeedArticle(t, f.db, f.owner.ID, "被点赞的那篇文章")
	// 从没存在过的 id：模拟文章已删，通知还留着。
	const deletedArticleID = 999999

	kept := seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-title", "like.created", article.ID, false)
	orphan := seedNotification(t, f.db, f.owner.ID, f.bob.ID, "evt-orphan", "comment.created", deletedArticleID, false)
	// 关注类事件没有文章上下文，article_id 是 0。
	follow := seedNotification(t, f.db, f.owner.ID, f.alice.ID, "evt-follow", "follow.created", 0, false)

	items, _, _, err := f.svc.List(ctx, f.owner.ID, "", 1, 10)
	if err != nil {
		t.Fatalf("List 不应因标题缺失报错: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("列表长度期望 3，实际 %d", len(items))
	}

	// 列表按 id 倒序，后插的 follow 在最前、kept 在最后。
	byID := make(map[uint]Item, len(items))
	for _, item := range items {
		byID[item.Notice.ID] = item
	}

	if got := byID[kept.ID].ArticleTitle; got != "被点赞的那篇文章" {
		t.Fatalf("文章标题期望「被点赞的那篇文章」，实际 %q", got)
	}
	if got := byID[orphan.ID].ArticleTitle; got != "" {
		t.Fatalf("文章已删时标题应为空，实际 %q", got)
	}
	if got := byID[follow.ID].ArticleTitle; got != "" {
		t.Fatalf("非文章域事件标题应为空，实际 %q", got)
	}
}
