package notification

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"forum-backend/internal/event"
	"forum-backend/internal/model"
)

// notifySpec 是一条通知该长什么样。
// resolver 只负责把领域事件翻译成这个结构，不碰落库与推送。
type notifySpec struct {
	UserID     uint
	Type       string
	ArticleID  uint
	TargetType string
	TargetID   uint
}

// resolver 把一种事件翻译成通知规格。
// 返回 nil 表示这条事件不产生通知，handle 会直接 ACK。
type resolver func(ctx context.Context, db *gorm.DB, p event.Payload) (*notifySpec, error)

// resolverByType 是通知消费端唯一的扩展点。
// 新增一种行为时做三件事：加事件类型常量、写一个 resolver、在这里注册一行。
// 已注册的两类事件共用文章动作的处理逻辑，因为作用对象都是文章。
//
// 未来接入关注事件时，写 resolveFollow 即可：它把 Target 里的用户 id 当收件人，
// 不走文章回查，也用不到 Created。handle 与落库代码一行都不用改。
//
// 未注册的类型会在 handle 里直接 ACK。Stream 里从此混进搜索同步用的
// article.created 时，通知消费者既不会误发通知，也不会把消息卡在 PEL。
var resolverByType = map[string]resolver{
	event.TypeLikeCreated:    resolveArticleAction,
	event.TypeCommentCreated: resolveArticleAction,
	// 私信明确不产生站内通知：它有自己的未读计数和推送链路
	// （message.PushWorker 消费同一个 Stream 的另一个消费组）。
	// 如果这里也建一条通知，用户会在消息中心看到两份「新私信」红点。
	// 注册成「返回 nil」而不是不注册，是为了让 handle 走正常路径，
	// 不注册会打出一条 "no notification resolver" 的告警，每来一条私信刷一行。
	event.TypeMessageCreated: resolveNoNotification,
}

// resolveNoNotification 表示「这条事件不产生通知」，handle 收到 nil 会直接 ACK。
func resolveNoNotification(context.Context, *gorm.DB, event.Payload) (*notifySpec, error) {
	return nil, nil
}

// resolveArticleAction 处理作用在文章上的动作。
// 收件人由文章作者决定，跳转对象交给 jumpTarget 决定。
func resolveArticleAction(ctx context.Context, db *gorm.DB, p event.Payload) (*notifySpec, error) {
	articleID := articleIDOf(p)
	if articleID == 0 {
		return nil, nil
	}

	var article model.Article
	err := db.WithContext(ctx).Select("id", "user_id").First(&article, articleID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 文章已删，没有收件人。当成处理成功，重试没有意义。
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load article %d: %w", articleID, err)
	}
	if article.UserID == p.ActorID {
		// 自己给自己点赞或评论，不通知自己。
		return nil, nil
	}

	jump := jumpTarget(p)
	spec := &notifySpec{
		UserID:     article.UserID,
		Type:       p.Type,
		ArticleID:  articleID,
		TargetType: jump.Type,
		TargetID:   jump.ID,
	}
	return spec, nil
}

// articleIDOf 取这条事件所属的文章。
// 文章上的动作把文章放在 Target，这是所有事件共用的位置。
// Target 不是文章时返回 0，由 handle 当成不产生通知。
func articleIDOf(p event.Payload) uint {
	if p.Target.Type == event.TargetArticle {
		return p.Target.ID
	}
	return 0
}

// jumpTarget 是前端点开通知后要定位到的对象。
// 有 Created 就用它，评论跳那条评论；没有就用 Target，点赞跳文章。
func jumpTarget(p event.Payload) event.Target {
	if p.Created != nil {
		return *p.Created
	}
	return p.Target
}
