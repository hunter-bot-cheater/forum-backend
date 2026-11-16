package message

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/event"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
	"forum-backend/internal/user"
)

// msgFixture 是一个用例需要的现场：库、service、三个用户。
// 三个人足够测「A 与 B 的会话不影响 A 与 C 的会话」。
type msgFixture struct {
	db    *gorm.DB
	svc   *Service
	alice *model.User
	bob   *model.User
	carol *model.User
}

// newMsgFixture 准备干净库与固定用户。每个用例都从空表开始。
func newMsgFixture(t *testing.T) *msgFixture {
	t.Helper()

	db := testutil.NewDB(t)
	return &msgFixture{
		db:    db,
		svc:   NewService(db, user.NewUserService(db)),
		alice: testutil.SeedUser(t, db, "alice"),
		bob:   testutil.SeedUser(t, db, "bob"),
		carol: testutil.SeedUser(t, db, "carol"),
	}
}

// TestSendCreatesConversation 验证第一条消息会建会话，并且 pair_key 是归一化的。
//
// 归一化的意义在下一个用例：两个方向必须落到同一行。
// 这里先确认键的格式没写反（小 id 在前），否则下一个用例会以另一种方式失败，
// 排查时容易误判成并发问题。
func TestSendCreatesConversation(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	// bob 的 id 比 alice 大，发出去之后会话行应该是 (alice, bob)。
	if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "你好", ""); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	var conv model.Conversation
	if err := f.db.First(&conv).Error; err != nil {
		t.Fatalf("查会话失败: %v", err)
	}
	if conv.UserAID != f.alice.ID || conv.UserBID != f.bob.ID {
		t.Errorf("会话双方 = %d/%d，期望 %d/%d（小 id 在前）",
			conv.UserAID, conv.UserBID, f.alice.ID, f.bob.ID)
	}
	if conv.PairKey == "" || conv.UserAID >= conv.UserBID {
		t.Errorf("pair_key = %q，双方 %d/%d，归一化不成立", conv.PairKey, conv.UserAID, conv.UserBID)
	}
	if conv.LastMessageID == 0 {
		t.Error("last_message_id 应该指向刚发的消息")
	}
}

// TestConversationSharedByBothDirections 验证 A→B 与 B→A 落到同一条会话。
//
// 不归一化的表现：会话列表里出现两条，一条是「我给 B 发的」，
// 一条是「B 给我发的」，未读数也分裂成两份，用户看到两个红点。
func TestConversationSharedByBothDirections(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "alice 先发", ""); err != nil {
		t.Fatalf("alice 发送失败: %v", err)
	}
	if _, err := f.svc.Send(ctx, f.bob.ID, f.alice.ID, "bob 回复", ""); err != nil {
		t.Fatalf("bob 发送失败: %v", err)
	}

	var count int64
	if err := f.db.Model(&model.Conversation{}).Count(&count).Error; err != nil {
		t.Fatalf("统计会话失败: %v", err)
	}
	if count != 1 {
		t.Errorf("会话数 = %d，期望 1（两个方向共用一条）", count)
	}

	// bob 回复之后，alice 应该有 1 条未读，bob 自己是 0。
	items, _, err := f.svc.Conversations(ctx, f.alice.ID, 1, 10)
	if err != nil {
		t.Fatalf("取 alice 会话列表失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("alice 会话数 = %d，期望 1", len(items))
	}
	if items[0].Unread != 1 {
		t.Errorf("alice 未读 = %d，期望 1", items[0].Unread)
	}
	if items[0].Peer.ID != f.bob.ID {
		t.Errorf("会话对方 = %d，期望 bob %d", items[0].Peer.ID, f.bob.ID)
	}
}

// TestSendRejectsSelf 验证不能给自己发私信。
//
// 允许的话会话里「两个人」是同一个，未读数会加到永远读不掉的方向上。
func TestSendRejectsSelf(t *testing.T) {
	f := newMsgFixture(t)

	_, err := f.svc.Send(context.Background(), f.alice.ID, f.alice.ID, "自言自语", "")
	if !errors.Is(err, domain.ErrSelfAction) {
		t.Errorf("错误 = %v，期望 ErrSelfAction", err)
	}
}

// TestSendRejectsBadContent 验证空正文与超长正文都被拒。
func TestSendRejectsBadContent(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		content string
	}{
		{"空字符串", ""},
		{"只有空格", "   \n\t "},
		{"超长", longRunes(maxContentRunes + 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, tc.content, "")
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("错误 = %v，期望 ErrInvalidInput", err)
			}
		})
	}

	// 正好到上限应当通过，避免把「999 字」也误杀。
	if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, longRunes(maxContentRunes), ""); err != nil {
		t.Errorf("刚好 %d 字应当允许: %v", maxContentRunes, err)
	}
}

// TestSendToMissingUser 验证收件人不存在时返回 NotFound。
// 表上没有外键，这个检查只有 service 这一处，漏了就会写出一条指向空气的消息。
func TestSendToMissingUser(t *testing.T) {
	f := newMsgFixture(t)

	_, err := f.svc.Send(context.Background(), f.alice.ID, 999999, "在吗", "")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("错误 = %v，期望 ErrNotFound", err)
	}
}

// TestUnreadCounts 验证未读数只加给收件人，发件人自己不变。
func TestUnreadCounts(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	// bob 收 2 条，自己 0 条未读。
	for i := 0; i < 2; i++ {
		if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "第几条", ""); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}

	var conv model.Conversation
	if err := f.db.First(&conv).Error; err != nil {
		t.Fatalf("查会话失败: %v", err)
	}

	// alice 是小 id，对应 UnreadA；bob 对应 UnreadB。
	if conv.UnreadA != 0 {
		t.Errorf("发件人未读 = %d，期望 0", conv.UnreadA)
	}
	if conv.UnreadB != 2 {
		t.Errorf("收件人未读 = %d，期望 2", conv.UnreadB)
	}

	total, err := f.svc.UnreadTotal(ctx, f.bob.ID)
	if err != nil {
		t.Fatalf("取未读总数失败: %v", err)
	}
	if total != 2 {
		t.Errorf("bob 未读总数 = %d，期望 2", total)
	}

	total, err = f.svc.UnreadTotal(ctx, f.alice.ID)
	if err != nil {
		t.Fatalf("取未读总数失败: %v", err)
	}
	if total != 0 {
		t.Errorf("alice 未读总数 = %d，期望 0", total)
	}
}

// TestUnreadTotalAcrossConversations 验证未读总数会把多个会话相加。
//
// 只读一个会话或者只按某一列求和，都会漏掉另一半。
func TestUnreadTotalAcrossConversations(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	// alice 给 bob 发 2 条，carol 给 bob 发 1 条。
	for i := 0; i < 2; i++ {
		if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "hi", ""); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}
	if _, err := f.svc.Send(ctx, f.carol.ID, f.bob.ID, "hi", ""); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	// bob 在大 id 一侧，两个会话里都是 UnreadB 那条分支。
	total, err := f.svc.UnreadTotal(ctx, f.bob.ID)
	if err != nil {
		t.Fatalf("取未读总数失败: %v", err)
	}
	if total != 3 {
		t.Errorf("未读总数 = %d，期望 3", total)
	}
}

// TestMarkReadClearsUnread 验证标记已读同时清掉计数和消息行上的 read_at。
//
// 只清计数的话，以后按「未读消息」查历史会对不上；
// 只改 read_at 的话，红点永远消不掉。两处都断。
func TestMarkReadClearsUnread(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "hi", ""); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}

	cleared, err := f.svc.MarkRead(ctx, f.bob.ID, f.alice.ID)
	if err != nil {
		t.Fatalf("标记已读失败: %v", err)
	}
	if cleared != 2 {
		t.Errorf("清掉 %d 条，期望 2", cleared)
	}

	total, err := f.svc.UnreadTotal(ctx, f.bob.ID)
	if err != nil {
		t.Fatalf("取未读总数失败: %v", err)
	}
	if total != 0 {
		t.Errorf("标记后未读 = %d，期望 0", total)
	}

	// 消息行的 read_at 必须补上。
	var unread int64
	if err := f.db.Model(&model.Message{}).Where("read_at IS NULL").Count(&unread).Error; err != nil {
		t.Fatalf("统计未读消息失败: %v", err)
	}
	if unread != 0 {
		t.Errorf("还有 %d 条消息 read_at 为空", unread)
	}

	// 幂等：再调一次应当返回 0，不报错。
	cleared, err = f.svc.MarkRead(ctx, f.bob.ID, f.alice.ID)
	if err != nil {
		t.Fatalf("重复标记已读不该报错: %v", err)
	}
	if cleared != 0 {
		t.Errorf("重复标记清掉 %d 条，期望 0", cleared)
	}
}

// TestConversationsOrderedByLastMessage 验证会话按最后消息时间倒序。
//
// 用户找会话靠的是「刚聊过的排最上面」，顺序错了会以为消息没发出去。
func TestConversationsOrderedByLastMessage(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	// 先和 bob 聊，再和 carol 聊。两次写入时间必须能分开，
	// 否则同一毫秒内的两条记录顺序不定，测试会偶发失败。
	if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "和 bob 聊", ""); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := f.svc.Send(ctx, f.alice.ID, f.carol.ID, "和 carol 聊", ""); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	items, total, err := f.svc.Conversations(ctx, f.alice.ID, 1, 10)
	if err != nil {
		t.Fatalf("取会话列表失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total=%d len=%d，期望都是 2", total, len(items))
	}
	if items[0].Peer.ID != f.carol.ID {
		t.Errorf("第一条会话对方 = %d，期望 carol %d（最近聊的在最上面）", items[0].Peer.ID, f.carol.ID)
	}

	// bob 再回一条，他应当跳到第一位。
	time.Sleep(5 * time.Millisecond)
	if _, err := f.svc.Send(ctx, f.bob.ID, f.alice.ID, "bob 回一句", ""); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	items, _, err = f.svc.Conversations(ctx, f.alice.ID, 1, 10)
	if err != nil {
		t.Fatalf("取会话列表失败: %v", err)
	}
	if items[0].Peer.ID != f.bob.ID {
		t.Errorf("回消息后第一条会话对方 = %d，期望 bob %d", items[0].Peer.ID, f.bob.ID)
	}
}

// TestMessagesCursorOrder 验证游标翻页的顺序与 has_more 边界。
//
// 聊天窗口的两种阅读习惯都要照顾：打开就看到最新内容（不带 before_id 就是最新一页），
// 但一页内部要能从上往下读（正序）。
func TestMessagesCursorOrder(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	// 发 5 条，内容就是序号，方便断言顺序。
	for i := 1; i <= 5; i++ {
		if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "第"+itoa(i)+"条", ""); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}

	// 第一页：最新 2 条，应当是第 4、5 条，且正序。
	got, err := f.svc.Messages(ctx, f.alice.ID, f.bob.ID, MessageQuery{Limit: 2})
	if err != nil {
		t.Fatalf("取消息列表失败: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("第一页 %d 条，期望 2", len(got.Items))
	}
	// 进入聊天页的顶栏要画对方是谁，所以消息列表必须带对方资料。
	if got.Peer.ID != f.bob.ID || got.Peer.Username != "bob" {
		t.Errorf("peer = %+v，期望 bob", got.Peer)
	}
	if got.Items[0].Message.Content != "第4条" || got.Items[1].Message.Content != "第5条" {
		t.Errorf("第一页 = %s / %s，期望 第4条 / 第5条",
			got.Items[0].Message.Content, got.Items[1].Message.Content)
	}
	if !got.HasMore {
		t.Error("还有更早的消息，has_more 应当为 true")
	}
	if got.NextBeforeID != got.Items[0].Message.ID {
		t.Errorf("next_before_id = %d，期望等于本页最早一条的 id %d",
			got.NextBeforeID, got.Items[0].Message.ID)
	}

	// 第二页：拿上一页的游标继续往上翻，应当是第 2、3 条。
	got, err = f.svc.Messages(ctx, f.alice.ID, f.bob.ID, MessageQuery{BeforeID: got.NextBeforeID, Limit: 2})
	if err != nil {
		t.Fatalf("取第二页失败: %v", err)
	}
	if len(got.Items) != 2 || got.Items[0].Message.Content != "第2条" || got.Items[1].Message.Content != "第3条" {
		t.Errorf("第二页 = %v，期望 第2条 / 第3条", contents(got.Items))
	}
	if !got.HasMore {
		t.Error("第二页之后还有第 1 条，has_more 应当为 true")
	}

	// 第三页只剩 1 条，并且到头了。
	got, err = f.svc.Messages(ctx, f.alice.ID, f.bob.ID, MessageQuery{BeforeID: got.NextBeforeID, Limit: 2})
	if err != nil {
		t.Fatalf("取第三页失败: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Message.Content != "第1条" {
		t.Errorf("第三页 = %v，期望只剩 第1条", contents(got.Items))
	}
	if got.HasMore {
		t.Error("已经翻到头了，has_more 应当为 false")
	}

	// Mine 字段按发送者算：alice 发的都是 true。
	// 一次取全部（5 条没超过上限），逐页取反而会把断言写复杂。
	all, err := f.svc.Messages(ctx, f.alice.ID, f.bob.ID, MessageQuery{Limit: 5})
	if err != nil {
		t.Fatalf("取全部消息失败: %v", err)
	}
	if len(all.Items) != 5 {
		t.Fatalf("共取到 %d 条，期望 5", len(all.Items))
	}
	for _, item := range all.Items {
		if !item.Mine {
			t.Errorf("alice 自己发的消息 Mine 应当为 true，实际 false：%s", item.Message.Content)
		}
	}
}

// TestMessagesCursorDoesNotDrift 是「边聊边往上翻」的回归测试。
//
// 场景：用户正在看第二页历史，对方又发了新消息。
// 用 OFFSET 分页时，新增的行会把窗口整体往后推，第二页会把第一页
// 已经看过的消息再返回一遍（用户会看到同一句话出现两次）；
// 游标以「本页最早一条的 id」为锚点，新消息的 id 更大，不进入下一页的查询条件。
func TestMessagesCursorDoesNotDrift(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "第"+itoa(i)+"条", ""); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}

	first, err := f.svc.Messages(ctx, f.alice.ID, f.bob.ID, MessageQuery{Limit: 2})
	if err != nil {
		t.Fatalf("取第一页失败: %v", err)
	}
	seen := map[uint]bool{}
	for _, item := range first.Items {
		seen[item.Message.ID] = true
	}

	// 关键动作：翻页之前，会话里又多了两条新消息。
	for i := 6; i <= 7; i++ {
		if _, err := f.svc.Send(ctx, f.bob.ID, f.alice.ID, "第"+itoa(i)+"条", ""); err != nil {
			t.Fatalf("发送新消息失败: %v", err)
		}
	}

	second, err := f.svc.Messages(ctx, f.alice.ID, f.bob.ID, MessageQuery{BeforeID: first.NextBeforeID, Limit: 2})
	if err != nil {
		t.Fatalf("取第二页失败: %v", err)
	}
	for _, item := range second.Items {
		if seen[item.Message.ID] {
			t.Errorf("第二页重复返回了第一页已有的消息 %d（%s）", item.Message.ID, item.Message.Content)
		}
		seen[item.Message.ID] = true
	}
	if len(second.Items) != 2 || second.Items[0].Message.Content != "第2条" || second.Items[1].Message.Content != "第3条" {
		t.Errorf("第二页 = %v，期望仍然是 第2条 / 第3条", contents(second.Items))
	}

	// 不传游标再取一次，新消息必须在最新一页里可见。
	latest, err := f.svc.Messages(ctx, f.alice.ID, f.bob.ID, MessageQuery{Limit: 2})
	if err != nil {
		t.Fatalf("取最新一页失败: %v", err)
	}
	if len(latest.Items) != 2 || latest.Items[1].Message.Content != "第7条" {
		t.Errorf("最新一页 = %v，期望最后一条是 第7条", contents(latest.Items))
	}
}

// TestMessagesEmptyConversation 验证还没聊过时返回空列表而不是报错。
//
// 回 404 的话，从陌生人主页点「私信」会先看到一个错误页，
// 而他其实只是想开始聊天。
func TestMessagesEmptyConversation(t *testing.T) {
	f := newMsgFixture(t)

	got, err := f.svc.Messages(context.Background(), f.alice.ID, f.carol.ID, MessageQuery{Limit: 10})
	if err != nil {
		t.Fatalf("没聊过时不该报错: %v", err)
	}
	if len(got.Items) != 0 || got.HasMore || got.NextBeforeID != 0 {
		t.Errorf("len=%d has_more=%v next=%d，期望全是空值",
			len(got.Items), got.HasMore, got.NextBeforeID)
	}
	// 没聊过也要有对方资料：空聊天页的顶栏照样要显示对方是谁。
	if got.Peer.ID != f.carol.ID {
		t.Errorf("peer = %+v，期望 carol", got.Peer)
	}
}

// TestPagingValidation 验证非法分页被拒。
// service 自己判一次，不依赖 HTTP 层已经解析过。
func TestPagingValidation(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	bad := [][2]int{{0, 10}, {1, 0}, {1, 51}, {-1, 10}}
	for _, p := range bad {
		if _, _, err := f.svc.Conversations(ctx, f.alice.ID, p[0], p[1]); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("分页 %v：错误 = %v，期望 ErrInvalidInput", p, err)
		}
	}
	for _, limit := range []int{0, -1, 51} {
		if _, err := f.svc.Messages(ctx, f.alice.ID, f.bob.ID, MessageQuery{Limit: limit}); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("limit=%d：错误 = %v，期望 ErrInvalidInput", limit, err)
		}
	}
}

// TestSendIdempotent 验证同一个 client_msg_id 只落一条消息。
//
// 现实场景：用户点了发送，服务端已经写库成功，但响应在路上超时了，
// 客户端自动重试一次。没有幂等键的话，聊天记录里就会出现两条一模一样的消息，
// 而且未读数还被加了两次——这种错误对用户是明确可见的，不能只靠「概率低」糊过去。
func TestSendIdempotent(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	first, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "在吗", "key-1")
	if err != nil {
		t.Fatalf("第一次发送失败: %v", err)
	}
	second, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "在吗", "key-1")
	if err != nil {
		t.Fatalf("重试不该报错: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("重试后的 id = %d，期望仍是第一次的 %d", second.ID, first.ID)
	}

	var count int64
	if err := f.db.Model(&model.Message{}).Count(&count).Error; err != nil {
		t.Fatalf("统计消息失败: %v", err)
	}
	if count != 1 {
		t.Errorf("库里有 %d 条消息，期望 1 条", count)
	}

	var conv model.Conversation
	if err := f.db.First(&conv).Error; err != nil {
		t.Fatalf("查会话失败: %v", err)
	}
	// 重试不是一条新消息，未读只能 +1。加了两次的表现是红点数字越点越大。
	if conv.UnreadB != 1 {
		t.Errorf("bob 的未读数 = %d，期望 1", conv.UnreadB)
	}
	if conv.LastMessageID != first.ID {
		t.Errorf("last_message_id = %d，期望 %d", conv.LastMessageID, first.ID)
	}

	// 同一个键配不同内容：仍返回第一次那条，不覆盖已落库的记录。
	// 幂等键代表「同一次发送」，内容不同说明是客户端复用错了键，
	// 这时把旧消息改掉比返回旧消息更糟（用户会看到历史被篡改）。
	third, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "改过的内容", "key-1")
	if err != nil {
		t.Fatalf("复用键发送不该报错: %v", err)
	}
	if third.ID != first.ID || third.Content != "在吗" {
		t.Errorf("复用键返回 id=%d content=%q，期望 id=%d content=在吗",
			third.ID, third.Content, first.ID)
	}
}

// TestSendWithoutKeyNotDeduplicated 验证不带幂等键的消息不会互相顶掉。
//
// 这条用例守的是「client_msg_id 必须存 NULL 而不是空串」：
// 唯一索引对 NULL 不生效，对 ” 生效。一旦把没传的键写成 ”，
// 同一会话的第二条消息就会撞 uk_msg_client，用户发一条成功、发第二条就失败。
func TestSendWithoutKeyNotDeduplicated(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	a, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "第一条", "")
	if err != nil {
		t.Fatalf("第一条发送失败: %v", err)
	}
	b, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "第二条", "")
	if err != nil {
		t.Fatalf("第二条发送失败（不带幂等键不应去重）: %v", err)
	}
	if a.ID == b.ID {
		t.Errorf("两条消息拿到了同一个 id %d", a.ID)
	}

	var count int64
	if err := f.db.Model(&model.Message{}).Count(&count).Error; err != nil {
		t.Fatalf("统计消息失败: %v", err)
	}
	if count != 2 {
		t.Errorf("库里有 %d 条消息，期望 2 条", count)
	}
}

// TestSendRejectsLongClientMsgID 验证超长幂等键被挡在写库之前。
// 不挡的话，超过 varchar(64) 的部分会被 MySQL 截断或直接报错，
// 而截断会让两个不同的键变成同一个，去重逻辑就开始误伤。
func TestSendRejectsLongClientMsgID(t *testing.T) {
	f := newMsgFixture(t)

	long := longRunes(maxClientMsgIDLen + 1)
	if _, err := f.svc.Send(context.Background(), f.alice.ID, f.bob.ID, "hi", long); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("超长幂等键：错误 = %v，期望 ErrInvalidInput", err)
	}
}

// TestMessagesPersistAfterReconnect 验证聊天记录是真的落在 MySQL 里。
//
// 做法：用原 service 发几条消息，然后换一条全新的数据库连接、
// 配一个新的 service 去读。读得到才算持久化，
// 否则「关掉浏览器再回来还能看到历史」这件事就只是运气好。
func TestMessagesPersistAfterReconnect(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "第"+itoa(i)+"条", ""); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}

	fresh := testutil.ReopenDB(t)
	freshSvc := NewService(fresh, user.NewUserService(fresh))

	got, err := freshSvc.Messages(ctx, f.bob.ID, f.alice.ID, MessageQuery{Limit: 10})
	if err != nil {
		t.Fatalf("新连接读取消息失败: %v", err)
	}
	if len(got.Items) != 3 {
		t.Fatalf("新连接读到 %d 条，期望 3 条", len(got.Items))
	}
	// 从 bob 的视角看，这三条都是对方发的，且顺序是正序。
	if got.Items[0].Message.Content != "第1条" || got.Items[2].Message.Content != "第3条" {
		t.Errorf("新连接读到的顺序 = %v，期望 第1条 … 第3条", contents(got.Items))
	}
	if got.Items[0].Mine {
		t.Error("bob 看 alice 发的消息，Mine 应当为 false")
	}

	// 会话行同样要持久：会话列表、未读数都从它来。
	convs, _, err := freshSvc.Conversations(ctx, f.bob.ID, 1, 10)
	if err != nil {
		t.Fatalf("新连接读取会话列表失败: %v", err)
	}
	if len(convs) != 1 {
		t.Fatalf("新连接读到 %d 个会话，期望 1 个", len(convs))
	}
	if convs[0].Unread != 3 || convs[0].Peer.ID != f.alice.ID {
		t.Errorf("会话 = %+v，期望对方是 alice、未读 3", convs[0])
	}
}

// TestSendEmitsMessageEvent 验证发消息会在同一个事务里写一条 message.created 事件。
//
// 实时推送靠这条事件驱动（outbox -> Stream -> PushWorker -> WebSocket）。
// 事件必须和消息一起提交：先落消息再补事件，中间挂掉就是「消息发出去了但对方收不到」，
// 而这种丢失没有报错、没有日志，只能靠用户反馈才发现。
func TestSendEmitsMessageEvent(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	msg, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "在吗", "k-event")
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	var events []model.OutboxEvent
	if err := f.db.Where("event_type = ?", event.TypeMessageCreated).Find(&events).Error; err != nil {
		t.Fatalf("查事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("事件 %d 条，期望 1 条", len(events))
	}

	p, err := event.Decode(events[0].Payload)
	if err != nil {
		t.Fatalf("解码事件失败: %v", err)
	}
	if p.ActorID != f.alice.ID {
		t.Errorf("actor = %d，期望发送者 %d", p.ActorID, f.alice.ID)
	}
	// Target 是「谁该收到」，私信里就是收件人；推送 worker 不看这个字段，
	// 但运维排查「这条事件本该通知谁」时要靠它。
	if p.Target.Type != event.TargetUser || p.Target.ID != f.bob.ID {
		t.Errorf("target = %+v，期望收件人 %d", p.Target, f.bob.ID)
	}
	if p.Created == nil || p.Created.Type != event.TargetMessage || p.Created.ID != msg.ID {
		t.Errorf("created = %+v，期望指向消息 %d", p.Created, msg.ID)
	}

	// 幂等重试不该补发事件：补发会让收件人收到两次同样的推送。
	if _, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "在吗", "k-event"); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	var after int64
	if err := f.db.Model(&model.OutboxEvent{}).
		Where("event_type = ?", event.TypeMessageCreated).Count(&after).Error; err != nil {
		t.Fatalf("统计事件失败: %v", err)
	}
	if after != 1 {
		t.Errorf("重试后事件 %d 条，期望仍是 1 条", after)
	}
}

// TestSendConcurrentSameKey 验证两个并发的同键请求都成功、且只落一条。
//
// 这条守的是「撞唯一索引之后补查必须用当前读」：
// 两个事务同时进来，都查不到、都去插入，后到的那个撞 uk_msg_client。
// 如果补查走普通 SELECT，RR 隔离下它的快照建立在前一个事务提交之前，
// 会得到「明明撞了键却查不到那一行」的结果，最终变成给用户回 500。
// 这和 ensureConversation 里补查会话是同一个坑，只是换了个位置。
func TestSendConcurrentSameKey(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	// 并发窗口很小，一轮不一定能撞上，所以跑五轮；任何一轮出问题都算失败。
	for round := 0; round < 5; round++ {
		key := "concurrent-" + itoa(round)
		ids := make([]uint, 2)
		errs := make([]error, 2)

		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				msg, err := f.svc.Send(ctx, f.alice.ID, f.bob.ID, "并发同一句", key)
				if err != nil {
					errs[idx] = err
					return
				}
				ids[idx] = msg.ID
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("第 %d 轮第 %d 个请求失败: %v", round, i, err)
			}
		}
		if ids[0] != ids[1] {
			t.Errorf("第 %d 轮两个请求拿到不同 id：%d / %d", round, ids[0], ids[1])
		}
	}

	// 5 个不同的键 → 5 条消息、5 条未读，多出来的都算重复计数。
	var count int64
	if err := f.db.Model(&model.Message{}).Count(&count).Error; err != nil {
		t.Fatalf("统计消息失败: %v", err)
	}
	if count != 5 {
		t.Errorf("库里有 %d 条消息，期望 5 条", count)
	}

	var conv model.Conversation
	if err := f.db.First(&conv).Error; err != nil {
		t.Fatalf("查会话失败: %v", err)
	}
	if conv.UnreadB != 5 {
		t.Errorf("bob 的未读数 = %d，期望 5", conv.UnreadB)
	}
}

// contents 抽出消息正文，断言失败时打出来的东西比 %v 整个结构体好读。
func contents(items []MessageItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Message.Content)
	}
	return out
}

// longRunes 生成 n 个汉字的字符串，用于测字数上限。
// 用汉字而不是 'a'：按字节判长度会把它误算成三倍。
func longRunes(n int) string {
	buf := make([]rune, n)
	for i := 0; i < n; i++ {
		buf[i] = '啊'
	}
	return string(buf)
}

// itoa 是测试里用的极小整数转字符串，避免为了一个数字引入 strconv。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 3)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestConcurrentSendKeepsSingleConversation 验证双方同时发第一条消息不会分裂会话，也不会丢消息。
//
// 这是本模块最容易写错的地方：两个事务同时发现「会话不存在」，
// 同时插入，一个成功一个撞唯一索引。处理不好的表现有两种：
//   - 补查用快照读，读不到对方刚提交的行，整个事务回滚，消息静默消失；
//   - 没有唯一索引兜底，两条会话各自挂了一半消息。
//
// 两种都不报错，只有用户发现「刚发的消息不见了」。所以这里直接数行。
func TestConcurrentSendKeepsSingleConversation(t *testing.T) {
	f := newMsgFixture(t)
	ctx := context.Background()

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)

	// 带缓冲，避免某个 goroutine 卡在发送结果上导致 wg.Wait 永远不返回。
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		// 奇偶交替，让两个方向都有人在发，覆盖「谁是 user_a」的两种分支。
		from, to := f.alice.ID, f.bob.ID
		if i%2 == 1 {
			from, to = f.bob.ID, f.alice.ID
		}
		go func(from, to uint) {
			defer wg.Done()
			<-start
			_, err := f.svc.Send(ctx, from, to, "并发发送", "")
			errs <- err
		}(from, to)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("并发发送失败: %v", err)
		}
	}

	var convs int64
	if err := f.db.Model(&model.Conversation{}).Count(&convs).Error; err != nil {
		t.Fatalf("统计会话失败: %v", err)
	}
	if convs != 1 {
		t.Errorf("会话数 = %d，期望 1（两个方向共用一条）", convs)
	}

	var msgs int64
	if err := f.db.Model(&model.Message{}).Count(&msgs).Error; err != nil {
		t.Fatalf("统计消息失败: %v", err)
	}
	if msgs != n {
		t.Errorf("消息数 = %d，期望 %d（一条都不能丢）", msgs, n)
	}

	// 两个方向各发 4 条，所以每人未读都是 4。
	var conv model.Conversation
	if err := f.db.First(&conv).Error; err != nil {
		t.Fatalf("查会话失败: %v", err)
	}
	if conv.UnreadA != n/2 || conv.UnreadB != n/2 {
		t.Errorf("未读 = %d/%d，期望各 %d", conv.UnreadA, conv.UnreadB, n/2)
	}
}
