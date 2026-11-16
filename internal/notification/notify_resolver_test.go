package notification

import (
	"context"
	"testing"

	"forum-backend/internal/event"
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
// TestResolverByTypeRegistry 盯住注册表本身：注册了就一定要能取到 resolver，
// 没注册的类型则保持缺失（handle 会 ACK，不会误发通知）。
func TestResolverByTypeRegistry(t *testing.T) {
	registered := []string{event.TypeLikeCreated, event.TypeCommentCreated, event.TypeMessageCreated}
	for _, key := range registered {
		if resolverByType[key] == nil {
			t.Errorf("event type %s has no resolver", key)
		}
	}
	if len(resolverByType) != len(registered) {
		t.Errorf("registry size = %d, want %d", len(resolverByType), len(registered))
	}

	// 搜索同步一类的事件没有注册，handle 会直接 ACK，不会误发通知。
	unregistered := []string{"article.created", "follow.created"}
	for _, key := range unregistered {
		if resolverByType[key] != nil {
			t.Errorf("event type %s should not have a resolver yet", key)
		}
	}
}

// TestMessageEventProducesNoNotification 验证私信事件在通知消费端被静默跳过。
//
// 私信有自己的未读计数与推送链路，如果这里也生成通知，
// 用户会在消息中心看到两份「新私信」红点。
// 注册成返回 nil 的 resolver（而不是不注册）还有一层作用：
// 不注册会走 "no notification resolver" 告警分支，每来一条私信刷一行日志。
func TestMessageEventProducesNoNotification(t *testing.T) {
	resolve, ok := resolverByType[event.TypeMessageCreated]
	if !ok {
		t.Fatal("message.created 必须注册 resolver，否则每条私信都会打一条告警日志")
	}

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
