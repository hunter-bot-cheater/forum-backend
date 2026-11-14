package article

import (
	"time"

	"forum-backend/internal/model"
)

// 本文件集中存放文章模块对外响应的结构体，以及从 model 到响应结构体的转换函数。

// userBrief 只暴露作者的两个字段，避免把整个 User 对象带出去。
// 评论模块也会定义自己的 userBrief（按模块内聚，不跨包共享）。
type userBrief struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
}

// articleResp 是文章列表与详情共用的对外形状。
type articleResp struct {
	ID        uint       `json:"id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Author    *userBrief `json:"author,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// toArticleResp 把数据库模型转成对外结构体，转换仍留在 handler 层。
func toArticleResp(a *model.Article) articleResp {
	resp := articleResp{
		ID:        a.ID,
		Title:     a.Title,
		Content:   a.Content,
		CreatedAt: a.CreatedAt,
	}
	if a.User.ID != 0 {
		resp.Author = &userBrief{
			ID:       a.User.ID,
			Username: a.User.Username,
		}
	}
	return resp
}

// articleListResp 是文章列表接口 data 的形状。
// 字段写错由编译器拦截，别在响应里用 gin.H 拼字符串键。
type articleListResp struct {
	List     []articleResp `json:"list"` // articleListResp.List 声明成切片，
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// createArticleReq 是发布文章的请求体。
// 字段里刻意没有 UserID：作者 id 只从 JWT 取，客户端传 user_id 会被 encoding/json 忽略。
type createArticleReq struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// articleDetailResp 是详情接口专用的响应，比列表多一个点赞数。
// 匿名嵌入 articleResp，encoding/json 会把里面的字段平铺到同一层，
// 输出仍是 id、title、content、author、created_at 加一个 like_count。
// 列表与新建接口继续用 articleResp，不带这个字段。
type articleDetailResp struct {
	articleResp
	LikeCount int64 `json:"like_count"`
}

// toArticleDetailResp 把详情模型转成对外结构体。
// LikeCount 由 queryDetail 在回源时算好，随文章一起进缓存。
func toArticleDetailResp(a *model.Article) articleDetailResp {
	return articleDetailResp{
		articleResp: toArticleResp(a),
		LikeCount:   a.LikeCount,
	}
}
