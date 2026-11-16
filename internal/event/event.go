// Package event 定义领域事件契约，以及事务发件箱的写入口。
package event

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"forum-backend/internal/model"
)

const (
	TypeLikeCreated    = "like.created"
	TypeCommentCreated = "comment.created"
	TypeMessageCreated = "message.created"

	// TypeFollowCreated 是「关注通知」的事件名。
	//
	// 通知侧已经完全就绪：notificationHandlers 里注册了 resolveFollowCreated，
	// 分类映射也有 follow 一项，落库与推送走的是和点赞、评论同一套代码。
	// 还差最后一环：follow.Service 关注成功后只更新关系与计数，没有写 outbox 事件，
	// 所以现在不会有任何关注通知产生，消息中心的 follow tab 恒为 0。
	// 接入时只需在关注的业务事务里调一次 EmitCreated，其余不用改。
	TypeFollowCreated = "follow.created"

	StreamKey = "forum:events:v1"

	// NotifyGroup 是站内通知的消费组，组内多个实例分摊事件（一条事件只被一个实例处理）。
	NotifyGroup = "forum:notify:v1"

	// MessageGroup 是私信实时推送的消费组前缀。
	// 它和 NotifyGroup 的语义不同：推送必须让每个实例都拿到同一条消息，
	// 才能推给连在该实例上的收件人，所以实际组名是「前缀:实例标识」，
	// 每个实例一个组，各收一份。详见 message.PushWorker。
	MessageGroup = "forum:message:v1"
)

// 目标对象的种类。新增行为时只新增一个取值，Payload 的字段个数不变。
const (
	TargetArticle = "article"
	TargetComment = "comment"
	TargetMessage = "message"
	TargetUser    = "user"
)

// Target 是对一个领域对象的引用。
// Type 说明去哪张表找，ID 说明找哪一行。
type Target struct {
	Type string `json:"type"`
	ID   uint   `json:"id"`
}

// Payload 是放进 outbox 与 Stream 的同一份 JSON。
// 字段只带 ID，消费端需要展示文案时自己回查数据库。
//
// Target 是动作作用的对象，所有事件共用这一个含义：
// 点赞作用在文章，评论作用在文章，关注作用在用户，回复作用在被回复的评论。
//
// Created 是这次动作新建出来的实体，可选。
// 评论的 Created 是那条新评论，通知点进去要定位到它。
// 点赞不填，通知只需要跳到文章，多带一个 like id 没有消费方。
// 字段带 omitempty，缺失时整个键都不出现。
type Payload struct {
	EventID    string  `json:"event_id"`
	Type       string  `json:"type"`
	ActorID    uint    `json:"actor_id"`
	Target     Target  `json:"target"`
	Created    *Target `json:"created,omitempty"`
	OccurredAt int64   `json:"occurred_at"`
}

// NewPayload 生成一个带全局唯一 ID 的事件。
// 参数按语义分：谁做的、作用在谁身上、新建了什么。
// 新增一种行为时不用改这个签名，只要传入新的 Target。
func NewPayload(eventType string, actorID uint, target Target, created *Target) (Payload, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Payload{}, fmt.Errorf("generate event id: %w", err)
	}
	return Payload{
		EventID:    hex.EncodeToString(b),
		Type:       eventType,
		ActorID:    actorID,
		Target:     target,
		Created:    created,
		OccurredAt: time.Now().Unix(),
	}, nil
}

// Encode 把事件序列化成 outbox 表里保存的字符串。
func (p Payload) Encode() (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal event %s: %w", p.EventID, err)
	}
	return string(b), nil
}

// Decode 是消费端用的反序列化入口。
// 事件格式只有 Target 加可选 Created 这一种，解码不做字段补齐。
func Decode(raw string) (Payload, error) {
	var p Payload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Payload{}, fmt.Errorf("decode event payload: %w", err)
	}
	return p, nil
}

// Emit 把一个事件写进 outbox。tx 必须是业务事务的句柄。
// 调用方如果把普通的 *gorm.DB 传进来，事件和业务写入就不在同一个事务里了，
// 这一章要守的保证会立刻失效。
func Emit(ctx context.Context, tx *gorm.DB, p Payload) error {
	payload, err := p.Encode()
	if err != nil {
		return err
	}
	row := &model.OutboxEvent{
		EventID:       p.EventID,
		EventType:     p.Type,
		AggregateType: p.Target.Type,
		AggregateID:   p.Target.ID,
		Payload:       payload,
		Status:        model.OutboxPending,
	}
	if err := tx.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("insert outbox event %s: %w", p.EventID, err)
	}
	return nil
}

// EmitCreated 是创建类事件共用的发事件入口。
// 两步合成一步：先生成带唯一 ID 的 Payload，再写进当前事务的 outbox。
// 参数里没有任何具体业务字段，新增关注、收藏、回复等事件都复用这个签名。
// 非创建类事件可以直接用 NewPayload 加 Emit，不必挤进这个函数。
// tx 必须来自业务事务，事件才能和业务写入一起提交或一起回滚。
func EmitCreated(ctx context.Context, tx *gorm.DB, eventType string, actorID uint, target Target, created *Target) error {
	p, err := NewPayload(eventType, actorID, target, created)
	if err != nil {
		return err
	}
	return Emit(ctx, tx, p)
}
