package message

import (
	"time"

	"forum-backend/internal/user"
)

// sendReq 是发私信的请求体。
// 收件人来自 URL 路径，发件人来自 JWT，所以正文里只需要 content。
// 多传的 sender_id 会被 encoding/json 忽略，进不了 service。
type sendReq struct {
	Content string `json:"content"`

	// ClientMsgID 是客户端生成的幂等键（建议 UUID），可留空。
	// 留空表示这条消息不参与去重；填了就保证「同一个 key 只落一条」，
	// 弱网重试、用户双击发送都不会产生重复气泡。
	ClientMsgID string `json:"client_msg_id"`
}

// messageResp 是一条消息对外的形状。
// 不直接序列化 model.Message：ConversationID 内部用、ReadAt 是数据库列名口径，
// 而且 Mine 需要服务端算，模型里没有这个字段。
type messageResp struct {
	ID        uint       `json:"id"`
	SenderID  uint       `json:"sender_id"`
	Content   string     `json:"content"`
	ReadAt    *time.Time `json:"read_at"`
	CreatedAt time.Time  `json:"created_at"`
	// Mine 表示这条是不是我发的，前端据此决定气泡靠左还是靠右。
	// 让服务端算：前端拿自己的 id 和 SenderID 比也能算，但会话列表里
	// 「最后一条是不是我发的」也要用同一个判断，两处写容易不一致。
	Mine bool `json:"mine"`
	// ClientMsgID 回显客户端传的幂等键，前端用它把「先画出来的灰气泡」
	// 换成服务端这条（拿到真实 id 和时间）。没传就不输出这个字段。
	ClientMsgID string `json:"client_msg_id,omitempty"`
}

// conversationResp 是会话列表里的一行。
type conversationResp struct {
	Peer        user.BriefResp `json:"peer"`
	LastMessage *messageResp   `json:"last_message"`
	Unread      int            `json:"unread"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// conversationListResp 是会话列表接口 data 的形状，字段与其它列表接口对齐。
type conversationListResp struct {
	List     []conversationResp `json:"list"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

// messageListResp 是消息列表接口 data 的形状。
//
// 带 Peer 是为了让「从主页点私信、进入与某人的聊天页」只需要一个请求：
// 顶栏要头像昵称，列表要消息，分开取会让首屏等两个请求。
// 会话还不存在（没聊过）时 Peer 也有值，空聊天页的顶栏照样能画。
//
// 分页用游标而不是页码：NextBeforeID 是本页最早一条的 id，
// 前端往上翻时把它原样作为 before_id 传回来。聊天记录只会往后追加，
// 页码会随着新消息漂移，游标不会。
type messageListResp struct {
	List         []messageResp  `json:"list"`
	HasMore      bool           `json:"has_more"`
	NextBeforeID uint           `json:"next_before_id"`
	Peer         user.BriefResp `json:"peer"`
}

// unreadResp 是未读角标的形状。
// 单独包一层对象而不是直接回一个数字：以后要在同一个接口里
// 加上「未读通知数」或「按会话拆分」时，不用改响应结构。
type unreadResp struct {
	Unread int64 `json:"unread"`
}

// readResp 是标记已读的结果，返回清掉了多少条。
type readResp struct {
	Cleared int64 `json:"cleared"`
}

// toMessageResp 把 service 的结果转成对外结构体。
// 纯函数，不做任何 IO：查询都在 handler 交给 service 之前就完成了。
func toMessageResp(item MessageItem) messageResp {
	m := item.Message
	resp := messageResp{
		ID:        m.ID,
		SenderID:  m.SenderID,
		Content:   m.Content,
		ReadAt:    m.ReadAt,
		CreatedAt: m.CreatedAt,
		Mine:      item.Mine,
	}
	// 库里存的是 NULL 表示没传，对外统一成「不输出这个字段」，
	// 免得前端拿到 null 还要和空串再区分一次。
	if m.ClientMsgID != nil {
		resp.ClientMsgID = *m.ClientMsgID
	}
	return resp
}

// toConversationResp 转换会话列表的一行。
//
// userID 用来判断最后一条消息是不是我发的：会话列表和聊天窗口用的是
// 同一条「靠左还是靠右」规则，口径只在这里算一次。
func toConversationResp(item ConversationItem, userID uint) conversationResp {
	resp := conversationResp{
		Peer:      user.NewBriefResp(item.Peer),
		Unread:    item.Unread,
		UpdatedAt: item.UpdatedAt,
	}
	if item.LastMessage != nil {
		last := toMessageResp(MessageItem{
			Message: *item.LastMessage,
			Mine:    item.LastMessage.SenderID == userID,
		})
		resp.LastMessage = &last
	}
	return resp
}

// toMessageListResp 组装消息列表的分页外壳。
// peer 从页面数据里取，和消息一起回，前端一次请求就能画出整个聊天页。
func toMessageListResp(page *MessagePage) messageListResp {
	list := make([]messageResp, 0, len(page.Items))
	for _, item := range page.Items {
		list = append(list, toMessageResp(item))
	}
	return messageListResp{
		List:         list,
		HasMore:      page.HasMore,
		NextBeforeID: page.NextBeforeID,
		Peer:         user.NewBriefResp(page.Peer),
	}
}

// toConversationListResp 组装会话列表的分页外壳。
func toConversationListResp(items []ConversationItem, total int64, page, pageSize int, userID uint) conversationListResp {
	list := make([]conversationResp, 0, len(items))
	for _, item := range items {
		list = append(list, toConversationResp(item, userID))
	}
	return conversationListResp{List: list, Total: total, Page: page, PageSize: pageSize}
}
