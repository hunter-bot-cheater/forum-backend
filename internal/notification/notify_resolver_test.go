package notification

import (
	"context"
	"testing"

	"forum-backend/internal/event"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
)

// Target 统一是被作用的对象。只有文章上的动作能复用文章 resolver。
func TestArticleIDOf(t *testing.T) {
	cases := []struct {
		name string
		p    event.Payload
		want uint
	}{
		{
			name: "like targets article",
			p:    event.Payload{Target: event.Target{Type: event.TargetArticle, ID: 7}},
			want: 7,
		},
		{
			name: "comment targets article",
			p: event.Payload{
				Target:  event.Target{Type: event.TargetArticle, ID: 7},
				Created: &event.Target{Type: event.TargetComment, ID: 34},
			},
			want: 7,
		},
		{
			name: "follow targets user",
			p:    event.Payload{Target: event.Target{Type: event.TargetUser, ID: 9}},
			want: 0,
		},
	}
	for _, c := range cases {
		got := articleIDOf(c.p)
		if got != c.want {
			t.Errorf("%s: articleIDOf = %d, want %d", c.name, got, c.want)
		}
	}
}

// 通知的跳转对象优先用 Created，没有 Created 时退回 Target。
func TestJumpTarget(t *testing.T) {
	article := event.Target{Type: event.TargetArticle, ID: 7}
	comment := event.Target{Type: event.TargetComment, ID: 34}

	got := jumpTarget(event.Payload{Target: article})
	if got != article {
		t.Errorf("like jump target = %+v, want %+v", got, article)
	}

	got = jumpTarget(event.Payload{Target: article, Created: &comment})
	if got != comment {
		t.Errorf("comment jump target = %+v, want %+v", got, comment)
	}
}

// 注册表是唯一扩展点。新增行为时加一行，handle 与落库代码不动。
// TestNotificationHandlerRegistry 盯住注册表本身：注册了就一定要有 resolve 与 kind，
// 没注册的类型则保持缺失（handle 会 ACK，不会误发通知）。
func TestNotificationHandlerRegistry(t *testing.T) {
	// 每一种事件必须同时给出分类与 resolver。只配一半的话：
	// 缺 resolver 通知不产生，缺 kind 则只在「全部」里能看到、分类 tab 永远为空，
	// 两种漏配都不会报错，所以只能靠测试守住。
	registered := []struct {
		eventType string
		wantKind  string
	}{
		{event.TypeLikeCreated, KindLike},
		{event.TypeCommentCreated, KindComment},
		{event.TypeFollowCreated, KindFollow},
	}
	for _, item := range registered {
		handler, ok := notificationHandlers[item.eventType]
		if !ok {
			t.Errorf("事件类型 %s 没有注册 handler", item.eventType)
			continue
		}
		if handler.resolve == nil {
			t.Errorf("事件类型 %s 没有 resolver", item.eventType)
		}
		if handler.kind != item.wantKind {
			t.Errorf("事件类型 %s 的分类 = %q，期望 %q", item.eventType, handler.kind, item.wantKind)
		}
	}

	// 私信要注册（否则每条私信打一条告警），但不属于任何分类 tab。
	msgHandler, ok := notificationHandlers[event.TypeMessageCreated]
	if !ok {
		t.Errorf("message.created 必须注册 handler，否则每条私信都会打一条告警日志")
	} else if msgHandler.kind != "" {
		t.Errorf("message.created 不该归入分类 tab，实际 kind = %q", msgHandler.kind)
	}

	// 搜索同步一类的事件没有注册，handle 会直接 ACK，不会误发通知。
	unregistered := []string{"article.created"}
	for _, key := range unregistered {
		if _, ok := notificationHandlers[key]; ok {
			t.Errorf("事件类型 %s 不该注册 handler", key)
		}
	}
}

// TestKindMappingMatchesRegistry 验证「分类 → 事件类型」和「事件类型 → 分类」
// 两个方向用的是同一张表，互为逆运算。
//
// 这两条路径分别给列表过滤和未读数统计用。它们一旦对不上，
// 表现是 tab 上有 3 个红点、点进去列表却是空的，而且没有任何报错。
func TestKindMappingMatchesRegistry(t *testing.T) {
	for _, kind := range notificationKinds {
		types, err := eventTypesOfKind(kind)
		if err != nil {
			t.Fatalf("分类 %s 反查失败: %v", kind, err)
		}
		if len(types) == 0 {
			t.Errorf("分类 %s 没有对应的事件类型，tab 永远是空的", kind)
		}
		for _, eventType := range types {
			if got := kindOfEventType(eventType); got != kind {
				t.Errorf("%s 反查分类 = %q，期望 %q", eventType, got, kind)
			}
		}
	}

	// 空分类表示「全部」，返回 nil 让查询不过滤。
	types, err := eventTypesOfKind("")
	if err != nil || types != nil {
		t.Errorf("空分类应当返回 (nil, nil)，实际 (%v, %v)", types, err)
	}
}

// TestFollowEventNotifiesFollowee 验证关注通知发给被关注的人，且跳转目标是他本人。
//
// 关注事件的 Target 是用户而不是文章，不能复用文章的那条逻辑：
// 复用会去 article_id = 用户 id 的文章里找作者，找到的是别人的文章或找不到。
func TestFollowEventNotifiesFollowee(t *testing.T) {
	ctx := context.Background()
	p := event.Payload{
		EventID: "evt-follow",
		Type:    event.TypeFollowCreated,
		ActorID: 3,
		Target:  event.Target{Type: event.TargetUser, ID: 8},
	}

	spec, err := resolveFollowCreated(ctx, nil, p)
	if err != nil {
		t.Fatalf("resolveFollowCreated 失败: %v", err)
	}
	if spec == nil {
		t.Fatal("关注应当产生一条通知")
	}
	if spec.UserID != 8 {
		t.Errorf("收件人期望被关注者 8，实际 %d", spec.UserID)
	}
	if spec.TargetType != event.TargetUser || spec.TargetID != 8 {
		t.Errorf("跳转目标期望 user/8，实际 %s/%d", spec.TargetType, spec.TargetID)
	}

	// Target 不是用户的事件格式不对，应当静默跳过而不是造一条错的通知。
	spec, err = resolveFollowCreated(ctx, nil, event.Payload{
		Type:   event.TypeFollowCreated,
		Target: event.Target{Type: event.TargetArticle, ID: 8},
	})
	if err != nil || spec != nil {
		t.Errorf("Target 不是用户时应当返回 (nil, nil)，实际 (%+v, %v)", spec, err)
	}
}

// TestMessageEventProducesNoNotification 验证私信事件在通知消费端被静默跳过。
//
// 私信有自己的未读计数与推送链路，如果这里也生成通知，
// 用户会在消息中心看到两份「新私信」红点。
// 注册成返回 nil 的 resolver（而不是不注册）还有一层作用：
// 不注册会走 "no notification handler" 告警分支，每来一条私信刷一行日志。
func TestMessageEventProducesNoNotification(t *testing.T) {
	handler, ok := notificationHandlers[event.TypeMessageCreated]
	if !ok {
		t.Fatal("message.created 必须注册 handler，否则每条私信都会打一条告警日志")
	}
	resolve := handler.resolve

	spec, err := resolve(context.Background(), nil, event.Payload{
		Type:    event.TypeMessageCreated,
		ActorID: 1,
		Target:  event.Target{Type: event.TargetUser, ID: 2},
		Created: &event.Target{Type: event.TargetMessage, ID: 3},
	})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if spec != nil {
		t.Errorf("私信不该产生通知，实际 spec = %+v", spec)
	}
}

// TestReplyNotifiesParentAuthor 验证回复通知发给被回复者，并跳到那条新回复。
//
// 这条守的是分流的正确性：回复的 Target 是评论，
// 如果仍然走 resolveArticleAction，通知会发到文章作者手里，被回复的人反而收不到。
func TestReplyNotifiesParentAuthor(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()

	articleAuthor := testutil.SeedUser(t, db, "article-author")
	commentAuthor := testutil.SeedUser(t, db, "comment-author")
	replier := testutil.SeedUser(t, db, "replier")
	article := testutil.SeedArticle(t, db, articleAuthor.ID, "被回复的文章")

	parent := &model.Comment{ArticleID: article.ID, UserID: commentAuthor.ID, Content: "父评论"}
	if err := db.Create(parent).Error; err != nil {
		t.Fatalf("插入父评论失败: %v", err)
	}

	p := event.Payload{
		EventID: "evt-reply",
		Type:    event.TypeCommentCreated,
		ActorID: replier.ID,
		Target:  event.Target{Type: event.TargetComment, ID: parent.ID},
		Created: &event.Target{Type: event.TargetComment, ID: 999},
	}
	spec, err := resolveCommentCreated(ctx, db, p)
	if err != nil {
		t.Fatalf("resolveCommentCreated 失败: %v", err)
	}
	if spec == nil {
		t.Fatal("回复应当产生一条通知")
	}
	if spec.UserID != commentAuthor.ID {
		t.Fatalf("收件人期望被回复者 %d，实际 %d", commentAuthor.ID, spec.UserID)
	}
	if spec.ArticleID != article.ID {
		t.Fatalf("article_id 期望 %d，实际 %d", article.ID, spec.ArticleID)
	}
	if spec.TargetType != event.TargetComment || spec.TargetID != 999 {
		t.Fatalf("跳转目标期望 comment/999，实际 %s/%d", spec.TargetType, spec.TargetID)
	}
}

// TestReplyToSelfProducesNothing 验证自己回复自己不产生通知。
func TestReplyToSelfProducesNothing(t *testing.T) {
	db := testutil.NewDB(t)
	articleAuthor := testutil.SeedUser(t, db, "self-author")
	article := testutil.SeedArticle(t, db, articleAuthor.ID, "自回复文章")

	parent := &model.Comment{ArticleID: article.ID, UserID: articleAuthor.ID, Content: "自己的评论"}
	if err := db.Create(parent).Error; err != nil {
		t.Fatalf("插入父评论失败: %v", err)
	}

	p := event.Payload{
		EventID: "evt-self",
		Type:    event.TypeCommentCreated,
		ActorID: articleAuthor.ID,
		Target:  event.Target{Type: event.TargetComment, ID: parent.ID},
	}
	spec, err := resolveCommentCreated(context.Background(), db, p)
	if err != nil {
		t.Fatalf("resolveCommentCreated 失败: %v", err)
	}
	if spec != nil {
		t.Fatalf("自己回复自己不该产生通知，实际 %+v", spec)
	}
}
