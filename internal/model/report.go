// Package model 定义 GORM 数据模型。
package model

import "time"

// 举报的目标类型。用字符串而不是枚举数字：库里直接看得懂，
// 新增类型不用改既有行的含义，前端传的也是这两串。
const (
	ReportTargetArticle = "article"
	ReportTargetComment = "comment"
)

// 举报的处理状态。pending 是唯一需要管理员关注的状态，
// 其余三个都表示「这条举报已经有结论了」，区别只在处置方式。
const (
	ReportStatusPending   = "pending"   // 待处理
	ReportStatusDismissed = "dismissed" // 驳回：管理员判定没有违规
	ReportStatusRemoved   = "removed"   // 已下架内容，但用户本身没被处罚
	ReportStatusBanned    = "banned"    // 已下架内容并封禁作者
)

// Report 一条举报记录。
//
// 目标用 TargetType + TargetID 表达，不建多态外键：文章和评论分属两张表，
// 一个列没法同时指向两者。代价是数据库无法替我们保证「目标存在」，
// 所以提交时由服务层查一次内容，顺带取出 TargetUserID 与 ArticleID。
//
// TargetUserID 冗余了被举报内容的作者：处置时要「下架内容并封禁作者」，
// 有了这一列，管理员点一次按钮不需要再去文章表/评论表反查作者是谁。
// 它也是「这个人被举报过几次」的统计入口。
//
// ArticleID 让管理端列表能显示「这条评论属于哪篇文章」，
// 文章类举报时它等于 TargetID，评论类举报时指向评论所在的文章。
//
// 三个状态列（Status / HandledBy / HandledAt）与处置动作一起写，
// 靠 `WHERE status = 'pending'` 的条件更新保证同一条举报只会被处置一次。
type Report struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// idx_report_reporter 服务于「我刚才是不是已经举报过这条」的重复检查。
	ReporterID uint   `gorm:"not null;index:idx_report_reporter,priority:1" json:"reporter_id"`
	TargetType string `gorm:"size:16;not null;index:idx_report_reporter,priority:2" json:"target_type"`
	TargetID   uint   `gorm:"not null;index:idx_report_reporter,priority:3" json:"target_id"`
	// TargetUserID 与 ArticleID 都建了索引：前者给「按作者聚合同类举报」，
	// 后者给「这篇文章下有多少条内容被举报」。
	TargetUserID uint   `gorm:"not null;index:idx_report_target_user" json:"target_user_id"`
	ArticleID    uint   `gorm:"not null;default:0;index:idx_report_article" json:"article_id"`
	Reason       string `gorm:"size:32;not null" json:"reason"`
	Detail       string `gorm:"size:500;not null;default:''" json:"detail"`
	// 状态与创建时间组成联合索引：管理端默认只看 pending 并按时间倒序，
	// 单列索引在「先按状态过滤再排序」时仍要回表排序。
	Status    string     `gorm:"size:16;not null;default:'pending';index:idx_report_status_created,priority:1" json:"status"`
	HandledBy uint       `gorm:"not null;default:0" json:"handled_by"`
	HandledAt *time.Time `json:"handled_at"`
	CreatedAt time.Time  `gorm:"index:idx_report_status_created,priority:2" json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}
