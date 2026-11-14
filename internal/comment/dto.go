package comment

import (
	"time"

	"forum-backend/internal/model"
)

// 本文件集中存放评论模块对外响应的结构体，以及从 model 到响应结构体的转换函数。

// userBrief 只暴露作者的两个字段，避免把整个 User 对象带出去。
// 文章模块也有自己的 userBrief（按模块内聚，不跨包共享）。
type userBrief struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
}

// commentResp 是评论对外的形状，不直接序列化 model.Comment。
type commentResp struct {
	ID        uint       `json:"id"`
	ArticleID uint       `json:"article_id"`
	Content   string     `json:"content"`
	CreatedAt time.Time  `json:"created_at"`
	Author    *userBrief `json:"author,omitempty"` // Author 用指针，omitempty 才生效
}

// commentListResp 是评论列表接口 data 的形状，字段与文章列表对齐。
type commentListResp struct {
	List     []commentResp `json:"list"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// toCommentResp 把评论模型转成对外结构体。
func toCommentResp(m *model.Comment) commentResp {
	resp := commentResp{
		ID:        m.ID,
		ArticleID: m.ArticleID,
		Content:   m.Content,
		CreatedAt: m.CreatedAt,
	}
	if m.User.ID != 0 {
		resp.Author = &userBrief{ID: m.User.ID, Username: m.User.Username}
	}
	return resp
}

// createCommentReq 是发表评论的请求体。
// 文章 id 来自 URL 路径，作者 id 来自 JWT，所以请求体只需要 content。
// 多出来的 article_id、user_id 会被 encoding/json 忽略，不会进入 service。
type createCommentReq struct {
	Content string `json:"content"`
}
