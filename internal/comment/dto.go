package comment

import (
	"time"

	"forum-backend/internal/model"
	"forum-backend/internal/user"
)

// 本文件集中存放评论模块对外响应的结构体，以及从 model 到响应结构体的转换函数。

// commentResp 是评论对外的形状，不直接序列化 model.Comment。
type commentResp struct {
	ID        uint            `json:"id"`
	ArticleID uint            `json:"article_id"`
	Content   string          `json:"content"`
	CreatedAt time.Time       `json:"created_at"`
	Author    *user.BriefResp `json:"author,omitempty"` // Author 用指针，omitempty 才生效

	// ParentID / RootID 让前端知道这条是回复谁、属于哪棵树。
	// 顶级评论两者都是 0。
	ParentID uint `json:"parent_id"`
	RootID   uint `json:"root_id"`

	// ReplyTo 是被回复者的展示信息，前端显示成「回复 @张三」。
	// 只在回复里有值；被回复者账号注销时省略整个字段。
	ReplyTo *user.BriefResp `json:"reply_to,omitempty"`

	// Replies 只出现在根评论上，装这一页展示的回复。
	// ReplyCount 是真实总数，可能大于 len(Replies)（被 maxRepliesPerRoot 截断）。
	//
	// 没有回复时这个字段会被省略，而不是输出 []：
	// encoding/json 的 omitempty 对空切片同样生效（长度 0 就省略），
	// 想输出 [] 就必须去掉 omitempty，但那样每条回复也会多一个 "replies": null。
	// 折中是保持省略，前端按「没有回复」处理时用 `item.replies ?? []`。
	Replies    []commentResp `json:"replies,omitempty"`
	ReplyCount int64         `json:"reply_count"`
}

// commentListResp 是评论列表接口 data 的形状，字段与文章列表对齐。
// List 里每一项都是根评论，回复挂在它自己的 replies 下。
type commentListResp struct {
	List     []commentResp `json:"list"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// toCommentResp 把评论模型转成对外结构体。
// m.User 只在 Preload 之后才有值；新建评论时没有 Preload，author 会被省略。
func toCommentResp(m *model.Comment) commentResp {
	resp := commentResp{
		ID:        m.ID,
		ArticleID: m.ArticleID,
		Content:   m.Content,
		CreatedAt: m.CreatedAt,
		ParentID:  m.ParentID,
		RootID:    m.RootID,
	}
	if m.User.ID != 0 {
		resp.Author = user.NewBriefRespPtr(user.NewBrief(&m.User))
	}
	return resp
}

// toCommentListResp 把评论树转成对外结构。
// 根评论带 replies 与 reply_count，回复本身不再嵌套（结构只有两层）。
func toCommentListResp(threads []Thread) []commentResp {
	items := make([]commentResp, 0, len(threads))
	for _, thread := range threads {
		root := toCommentResp(thread.Comment)
		root.ReplyCount = thread.ReplyTotal

		// 没有回复就保持 nil，让 omitempty 把字段整个省掉（见 commentResp.Replies 的说明）。
		// 直接赋空切片也能编译，但会让人误以为响应该带 "replies": []，而实际仍被省略。
		if len(thread.Replies) > 0 {
			replies := make([]commentResp, 0, len(thread.Replies))
			for _, item := range thread.Replies {
				reply := toCommentResp(item.Comment)
				// 被回复者放在这里而不是模型里：它来自父评论的作者，
				// 前端按它显示「回复 @张三」，账号注销时字段整个省略。
				reply.ReplyTo = user.NewBriefRespPtr(item.ReplyTo)
				replies = append(replies, reply)
			}
			root.Replies = replies
		}
		items = append(items, root)
	}
	return items
}

// commentAnchorResp 是「定位一条评论」接口的返回，只描述位置不给内容。
//
// 前端拿到 index 后自己折算页码，再照常调列表接口渲染，
// 这样定位接口不需要重复一份评论结构，也不会和列表的字段演进脱钩。
type commentAnchorResp struct {
	CommentID uint `json:"comment_id"`
	ArticleID uint `json:"article_id"`
	// RootID 是所属根评论的 id，定位到回复时前端靠它先找到外层容器。
	RootID uint `json:"root_id"`
	// Index 是所属根评论在文章里的序号，从 1 开始。
	Index int64 `json:"index"`
}

// createCommentReq 是发表评论的请求体。
// 文章 id 来自 URL 路径，作者 id 来自 JWT，所以请求体只需要 content。
// 多出来的 article_id、user_id 会被 encoding/json 忽略，不会进入 service。
type createCommentReq struct {
	Content string `json:"content"`

	// ParentID 大于 0 表示这是一条回复，指向被回复的评论。
	// 不填就是顶级评论，行为与加回复功能之前完全一致。
	ParentID uint `json:"parent_id"`
}
