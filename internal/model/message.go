// Package model 定义 GORM 数据模型。
package model

import "time"

// Conversation 是两个人之间的一对一会话。
//
// 为什么不把「会话」省掉、直接拿 messages 表按对方 id 分组：
// 会话列表要按「最近一条消息」排序并分页，还要带未读数。
// 单表方案得每次 GROUP BY 再排序，用户发了几千条之后这条查询就没法看了；
// 会话表把「最后一次互动时间和未读数」变成一行可以直接索引的数据。
//
// PairKey 是归一化后的 "小ID:大ID"，唯一索引保证两个人之间只有一条会话。
// 归一化（而不是存成 (发起人, 接收人) 两个方向）是为了让「A 给 B 发」和
// 「B 给 A 发」落到同一行，否则会话列表里会出现两个来回跳的条目。
//
// UserAID 恒为较小的 id，UserBID 恒为较大的 id，UnreadA / UnreadB 分别对应
// 两者的未读数。这样未读数只有一处来源，不需要每次去 count messages。
type Conversation struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	PairKey string `gorm:"size:41;not null;uniqueIndex:uk_conv_pair" json:"-"`

	// 两个方向各建一个复合索引：(用户, 最后消息时间)。
	// 会话列表的查询是「我参与的会话按时间倒序」，没有 (user, time) 复合索引
	// 就只能先按用户过滤再 filesort，会话一多排序开销会盖过查询本身。
	UserAID uint `gorm:"not null;index:idx_conv_user_a,priority:1" json:"user_a_id"`
	UserBID uint `gorm:"not null;index:idx_conv_user_b,priority:1" json:"user_b_id"`

	// LastMessageID 指向 messages 表最新一条，供会话列表展示摘要时直接取用。
	// 不建外键：消息和会话在同一事务里写，外键只会让批量删除变复杂。
	LastMessageID uint      `gorm:"not null;default:0" json:"last_message_id"`
	LastMessageAt time.Time `gorm:"index:idx_conv_user_a,priority:2;index:idx_conv_user_b,priority:2" json:"last_message_at"`

	// 未读数按「谁没读」分别记，发消息时给接收方那个 +1，标记已读时清零。
	// 不实时 count messages 的原因：会话列表一页 20 条就是 20 次 count，
	// 而这两个数字的更新点和消息写入在同一事务里，不额外引入不一致。
	UnreadA int `gorm:"not null;default:0" json:"unread_a"`
	UnreadB int `gorm:"not null;default:0" json:"unread_b"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Message 是一条私信。
//
// ConversationID 上的索引同时服务「按会话取消息列表」和「按会话批量标记已读」。
// 列表按 id 倒序翻页：InnoDB 的二级索引末尾自带主键，所以
// (conversation_id) 单列索引就能支持 (conversation_id, id DESC) 的有序扫描，
// 不需要再单独给 id 建索引。
//
// ReadAt 用指针区分「没读过」和「读过」：零值时间在 Go 里是 0001-01-01，
// 直接存进去会让「已读时间」看起来极其荒谬，也没法和真正的未读区分开。
type Message struct {
	ID             uint `gorm:"primaryKey" json:"id"`
	ConversationID uint `gorm:"not null;index:idx_msg_conv;uniqueIndex:uk_msg_client,priority:1" json:"conversation_id"`
	SenderID       uint `gorm:"not null" json:"sender_id"`
	ReceiverID     uint `gorm:"not null;index:idx_msg_receiver" json:"receiver_id"`

	// ClientMsgID 是客户端为「这一次发送」生成的唯一串（UUID 即可），用于幂等。
	//
	// 为什么必须是指针：唯一索引对 NULL 不生效，对空字符串生效。
	// 如果老客户端不传这个字段、库里存成 ''，同一会话的每一条消息都会撞
	// (conversation_id, '') 这个唯一键，第二条开始全部失败。
	// 用 *string 存 NULL，MySQL 允许任意多行 NULL 共存，旧调用方完全不受影响。
	ClientMsgID *string `gorm:"size:64;uniqueIndex:uk_msg_client,priority:2" json:"-"`

	Content   string     `gorm:"size:1000;not null" json:"content"`
	ReadAt    *time.Time `json:"read_at"`
	CreatedAt time.Time  `json:"created_at"`
}
