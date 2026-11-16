package notification

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
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

// eventHandler 描述「一种事件在通知模块里怎么处理」，两件事写在一起：
//   - kind：这条事件归到哪个分类（前端 tab 与分类未读数用）；
//   - resolve：把事件翻译成通知规格，返回 nil 表示不进消息中心。
//
// 合成一张表而不是两份 map，是因为它们永远要一起改：
// 新增一种事件时，漏配 resolve 会让通知不产生，漏配 kind 会让它只出现在「全部」里、
// 点进分类 tab 却看不到；两份表分开放时，这两种漏配都不会报错。
type eventHandler struct {
	kind    string
	resolve resolver
}

// notificationHandlers 是通知消费端唯一的扩展点。
// 新增一种行为时做两件事：加事件类型常量、在这里注册一行。
//
// 未注册的类型会在 handle 里直接 ACK。Stream 里从此混进搜索同步用的
// article.created 时，通知消费者既不会误发通知，也不会把消息卡在 PEL。
var notificationHandlers = map[string]eventHandler{
	event.TypeLikeCreated: {kind: KindLike, resolve: resolveArticleAction},
	event.TypeCommentCreated: {
		kind:    KindComment,
		resolve: resolveCommentCreated,
	},
	event.TypeFollowCreated: {kind: KindFollow, resolve: resolveFollowCreated},
	// 私信明确不产生站内通知：它有自己的未读计数和推送链路
	// （message.PushWorker 消费同一个 Stream 的另一个消费组）。
	// 如果这里也建一条通知，用户会在消息中心看到两份「新私信」红点。
	// 注册成「返回 nil」而不是不注册，是为了让 handle 走正常路径，
	// 不注册会打出一条 "no notification resolver" 的告警，每来一条私信刷一行。
	// kind 留空：它不属于任何分类 tab，只计入全部未读。
	event.TypeMessageCreated: {kind: "", resolve: resolveNoNotification},
}

// notificationKinds 是所有分类，顺序稳定（map 的遍历顺序随机，前端 tab 会乱跳）。
// 它决定 /notifications/summary 返回哪些键，也是「未知分类」的判定依据。
var notificationKinds = []string{KindLike, KindComment, KindFollow}

// kindOfEventType 把事件类型归到分类，未登记的返回空串。
// 未登记的通知仍然会出现在「全部」里，只是不会进任何分类 tab，
// 这样新增事件类型时不会因为漏配映射而在列表里消失。
func kindOfEventType(eventType string) string {
	handler, ok := notificationHandlers[eventType]
	if !ok {
		return ""
	}
	return handler.kind
}

// eventTypesOfKind 把分类翻译成查询用的类型集合，空分类返回 nil 表示不过滤。
//
// 从注册表反查而不是另存一张 map：注册表是唯一事实来源，
// 反查出来的集合不可能和 resolver 的表对不上。
func eventTypesOfKind(kind string) ([]string, error) {
	if kind == "" {
		return nil, nil
	}

	// 先确认这是已知分类。不确认的话，写错一个字母会得到一个空集合，
	// 查询变成 type IN ()，列表永远是空的，但接口回 200，看起来像「没有数据」。
	known := false
	for _, k := range notificationKinds {
		if k == kind {
			known = true
			break
		}
	}
	if !known {
		return nil, fmt.Errorf("%w: unknown notification kind %q", domain.ErrInvalidInput, kind)
	}

	types := make([]string, 0, len(notificationHandlers))
	for eventType, handler := range notificationHandlers {
		if handler.kind == kind {
			types = append(types, eventType)
		}
	}
	return types, nil
}

// resolveNoNotification 表示「这条事件不产生通知」，handle 收到 nil 会直接 ACK。
func resolveNoNotification(context.Context, *gorm.DB, event.Payload) (*notifySpec, error) {
	return nil, nil
}

// resolveCommentCreated 按「评论作用在谁身上」分发收件人。
//
// 顶级评论的 Target 是文章，通知文章作者（沿用 resolveArticleAction）；
// 回复的 Target 是被回复的那条评论，收件人改成那条评论的作者。
// 两者共用同一个事件类型，区别只在 Target.Type，所以必须在这里分流：
// 不分流的话，回复会继续通知文章作者，被回复的人反而收不到。
//
// 已知取舍：回复不再顺带通知文章作者。文章很热时每条回复都通知作者会刷屏，
// 需要「作者也收全部评论」时再按用户设置加，不在这一层硬编码。
func resolveCommentCreated(ctx context.Context, db *gorm.DB, p event.Payload) (*notifySpec, error) {
	if p.Target.Type != event.TargetComment {
		return resolveArticleAction(ctx, db, p)
	}

	var parent model.Comment
	err := db.WithContext(ctx).
		Select("id", "article_id", "user_id").
		First(&parent, p.Target.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 父评论已删：没有收件人，重试也没意义。
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load parent comment %d: %w", p.Target.ID, err)
	}
	if parent.UserID == p.ActorID {
		// 自己回复自己，不通知。
		return nil, nil
	}

	// 默认跳父评论；有 Created（这条新回复）就跳新回复，
	// 用户点开通知能直接看到自己写的那条，而不是它上面的父评论。
	targetID := parent.ID
	if p.Created != nil && p.Created.ID != 0 {
		targetID = p.Created.ID
	}

	return &notifySpec{
		UserID:     parent.UserID,
		Type:       p.Type,
		ArticleID:  parent.ArticleID,
		TargetType: event.TargetComment,
		TargetID:   targetID,
	}, nil
}

// resolveFollowCreated 处理「有人关注了你」。
//
// 关注事件不需要回查数据库：Target 就是被关注的人，他一定存在
// （follow.Service 在建立关系前校验过），Created 也没有对应的实体。
// 不查库还有一个好处——关注是高频动作，每条都多一次 SELECT 不划算。
//
// 目前 follow.Service 还没有发出这个事件，resolver 先就位：
// 接入时只需在关注成功后补一次 outbox 写入，这一层不用再改，
// 分类 tab 与未读数也会自动开始工作（注册表里已经登记了 kind）。
func resolveFollowCreated(_ context.Context, _ *gorm.DB, p event.Payload) (*notifySpec, error) {
	if p.Target.Type != event.TargetUser || p.Target.ID == 0 {
		// Target 不是用户，说明事件格式和预期不符，当成不产生通知。
		return nil, nil
	}
	if p.Target.ID == p.ActorID {
		// 关注自己：业务层已经拦住，这里再挡一次，免得脏数据造出一条「你关注了你」。
		return nil, nil
	}

	return &notifySpec{
		UserID:     p.Target.ID,
		Type:       p.Type,
		TargetType: event.TargetUser,
		TargetID:   p.Target.ID,
	}, nil
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
