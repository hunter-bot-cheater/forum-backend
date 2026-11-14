package handler

import (
	"time"

	"forum-backend/internal/model"
)

// 本文件集中存放对外响应的结构体，以及从 model 到响应结构体的转换函数。
// 单独成文件是为了让「客户端能看见哪些字段」在目录树里一眼可查，
// 也避免把数据库模型直接交给 response.OK 序列化。

// userResp 是注册与登录接口对外返回的用户信息。
// 字段逐个列出，model.User 以后加字段不会自动出现在响应里。
// 这里刻意不写 Password：model.User 的 Password 带 json:"-"，
// 但 DTO 从源头就不给这个字段留位置，少一层依赖运气。
type userResp struct {
	ID        uint      `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// loginResp 是登录成功后 data 的形状。
// Token 与 User 平级，客户端解析一次就能拿到两份数据。
// User 用值类型，登录成功时一定有用户，不需要指针和 omitempty。
type loginResp struct {
	Token string   `json:"token"`
	User  userResp `json:"user"`
}

// toUserResp 把数据库模型转换成对外结构体。
// 转换只发生在 handler 层：service 继续只认识 model.User，
// 不需要 import handler 包，依赖方向保持单向。
func toUserResp(u *model.User) userResp {
	return userResp{
		ID:        u.ID,
		Username:  u.Username,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

type articleResp struct {
	ID        uint       `json:"id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Author    *userBrief `json:"author,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// userBrief 只暴露作者的两个字段，避免把整个 User 对象带出去。
type userBrief struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
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

// commentResp 是评论对外的形状，不直接序列化 model.Comment。
type commentResp struct {
	ID        uint       `json:"id"`
	ArticleID uint       `json:"article_id"`
	Content   string     `json:"content"`
	CreatedAt time.Time  `json:"created_at"`
	Author    *userBrief `json:"author,omitempty"` //Author 用指针，omitempty 才生效
}

// articleListResp 是文章列表接口 data 的形状。
// 字段写错由编译器拦截，别在响应里用 gin.H 拼字符串键。
type articleListResp struct {
	List []articleResp `json:"list"` //articleListResp.List 声明成切片，
	// 	handler 里用 make([]articleResp, 0, len(...)) 初始化，无结果时输出 []
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
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

// createArticleReq 是发布文章的请求体。
// 字段里刻意没有 UserID：作者 id 只从 JWT 取，客户端传 user_id 会被 encoding/json 忽略。
type createArticleReq struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// createCommentReq 是发表评论的请求体。
// 文章 id 来自 URL 路径，作者 id 来自 JWT，所以请求体只需要 content。
// 多出来的 article_id、user_id 会被 encoding/json 忽略，不会进入 service。
type createCommentReq struct {
	Content string `json:"content"`
}


// likeResp 是点赞与取消点赞接口 data 的形状。
type likeResp struct {
	ArticleID uint  `json:"article_id"`
	Count     int64 `json:"like_count"`
	Changed   bool  `json:"changed"`
	Liked     bool  `json:"liked"`
}

// hotArticleResp 是热榜里一条记录的对外形状。
type hotArticleResp struct {
	ArticleID uint    `json:"article_id"`
	Score     float64 `json:"score"`
	Title     string  `json:"title"`
	LikeCount int64   `json:"like_count"`
}

// hotListResp 让响应始终带一个 list 字段，空榜输出 []。
type hotListResp struct {
	List []hotArticleResp `json:"list"`
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


// notificationResp 是一条站内通知对外的形状。
// target_type 加 target_id 表达跳转目标，新增行为不用加字段。
// HTTP 列表与 WebSocket 推送形状一致，前端按 target_type 分支。
type notificationResp struct {
	ID         uint      `json:"id"`
	EventID    string    `json:"event_id"`
	ActorID    uint      `json:"actor_id"`
	Type       string    `json:"type"`
	ArticleID  uint      `json:"article_id"`
	TargetType string    `json:"target_type"`
	TargetID   uint      `json:"target_id"`
	IsRead     bool      `json:"is_read"`
	CreatedAt  time.Time `json:"created_at"`
}

// notificationListResp 让列表始终带 list，空结果输出 []。
type notificationListResp struct {
	List        []notificationResp `json:"list"`
	Total       int64              `json:"total"`
	UnreadCount int64              `json:"unread_count"`
	Page        int                `json:"page"`
	PageSize    int                `json:"page_size"`
}

// onlineStatsResp 是在线状态接口 data 的形状。
// 用结构体而不是 gin.H，字段拼错由编译器拦住。
type onlineStatsResp struct {
	OnlineUsers int `json:"online_users"`
	OnlineConns int `json:"online_conns"`
}
