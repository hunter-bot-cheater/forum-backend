package report

import (
	"time"

	"forum-backend/internal/user"
)

// 本文件集中存放举报模块对外的请求与响应结构体。
// 与其它模块一致：service 只认识 model，model 到响应的转换发生在 handler 层。

// submitReq 是 POST /api/reports 的请求体。
// 没有 reporter_id：举报人只从 token 取，客户端传什么都不作数，
// 否则谁都能替别人举报，把封禁额度浪费在人身攻击上。
type submitReq struct {
	// TargetType 只能是 article 或 comment，由服务层校验。
	TargetType string `json:"target_type" binding:"required"`
	TargetID   uint   `json:"target_id" binding:"required"`
	// Reason 是分类而不是自由文本：分类能聚合出「这个作者反复被举报什么」，
	// 自由文本只能一条条读，管理端一多就没人看了。
	Reason string `json:"reason" binding:"required"`
	// Detail 是补充说明，可空。给「其他」这一类留一个说清楚的机会。
	Detail string `json:"detail"`
}

// submitResp 是举报成功的回执。
// 只回 id 与状态，不回显整条记录：举报人不需要再看一遍自己填的原文，
// 前端拿 id 也只是为了把按钮切成「已举报」。
type submitResp struct {
	ID     uint   `json:"id"`
	Status string `json:"status"`
}

// reportItem 是管理端列表的一行，也是这个模块信息量最大的结构体。
//
// 管理员的判断依据是「谁举报了谁、举报什么、内容长什么样」，
// 所以这里把三方信息拼在一起，避免他逐个点开才能判断。
type reportItem struct {
	ID         uint   `json:"id"`
	TargetType string `json:"target_type"`
	TargetID   uint   `json:"target_id"`
	// ArticleID 让前端能直接给出「查看所在文章」的链接，评论类举报时尤其有用。
	ArticleID uint `json:"article_id"`
	// TargetTitle 是文章标题；评论类举报时是评论所在文章的标题。
	TargetTitle string `json:"target_title"`
	// Excerpt 是被举报评论的正文摘要。文章类举报留空：
	// 正文可能几千字，列表里塞不下，管理员点进详情看更准。
	Excerpt string `json:"excerpt"`
	// TargetExists 为 false 表示内容已经被删（作者自删或另一位管理员下架）。
	// 管理员据此可以直接驳回，不用再点一次下架。
	TargetExists bool            `json:"target_exists"`
	Reporter     *user.BriefResp `json:"reporter,omitempty"`
	TargetAuthor *user.BriefResp `json:"target_author,omitempty"`
	Reason       string          `json:"reason"`
	Detail       string          `json:"detail"`
	Status       string          `json:"status"`
	CreatedAt    time.Time       `json:"created_at"`
	HandledAt    *time.Time      `json:"handled_at"`
}

// reportListResp 与其它列表接口保持同样的四个字段。
// 不抽泛型的原因见 paging 包末尾的注释：swag 解析不了泛型实例，接口文档会变空。
type reportListResp struct {
	List     []reportItem `json:"list"`
	Total    int64        `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}

// handleReq 是 PUT /api/admin/reports/:id 的请求体。
type handleReq struct {
	// Action 只能是 dismiss / remove / ban，大小写由服务层归一。
	Action string `json:"action" binding:"required"`
	// BanDays 只在 action=ban 时读，范围 1..3650。
	//
	// 要「永久」就填 3650，不设 0 或负数表示永久：那种约定太容易被误传，
	// 而一次误封永久账号是没法靠回滚补救的——人不来了，得先找到他才能解。
	BanDays int `json:"ban_days"`
}

// toReportItem 把 service 组装好的条目转成响应结构体。
//
// 放在 handler 层：service 返回的是 user.Brief，响应要的是 user.BriefResp，
// 这个「展示名回退到用户名」的转换全项目只在一处发生，不能在 service 里再抄一遍。
func toReportItem(e Entry) reportItem {
	return reportItem{
		ID:           e.Report.ID,
		TargetType:   e.Report.TargetType,
		TargetID:     e.Report.TargetID,
		ArticleID:    e.Report.ArticleID,
		TargetTitle:  e.TargetTitle,
		Excerpt:      e.Excerpt,
		TargetExists: e.TargetExists,
		Reporter:     user.NewBriefRespPtr(e.Reporter),
		TargetAuthor: user.NewBriefRespPtr(e.TargetAuthor),
		Reason:       e.Report.Reason,
		Detail:       e.Report.Detail,
		Status:       e.Report.Status,
		CreatedAt:    e.Report.CreatedAt,
		HandledAt:    e.Report.HandledAt,
	}
}
