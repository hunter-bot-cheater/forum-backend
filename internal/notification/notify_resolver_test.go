package notification

import (
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
func TestResolverByTypeRegistry(t *testing.T) {
	registered := []string{event.TypeLikeCreated, event.TypeCommentCreated}
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
