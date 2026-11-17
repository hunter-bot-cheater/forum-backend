// Package report 处理内容举报与处置：用户举报文章/评论，管理员驳回、下架或下架并封禁作者。
//
// 与 article、comment 两个模块的关系是「只调用、不反向依赖」：
// 处置动作复用它们已有的 Delete（作者身份由举报记录里的 TargetUserID 提供），
// 这样缓存失效、清点赞集合、移出热榜这些副作用不用在举报模块里再实现一遍。
package report

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"forum-backend/internal/ban"
	"forum-backend/internal/domain"
	"forum-backend/internal/model"
	"forum-backend/internal/paging"
	"forum-backend/internal/textutil"
	"forum-backend/internal/user"
)

// 处置动作。三个值对应三种结论，不存在「只封不禁言」这类组合：
// 只封人不删内容会让违规内容继续挂着，只删内容不封人则对刷屏账号没有威慑力，
// 所以 remove 与 ban 都自带下架。
const (
	ActionDismiss = "dismiss" // 驳回：内容没违规
	ActionRemove  = "remove"  // 下架内容，不动用户
	ActionBan     = "ban"     // 下架内容并封禁作者
)

const (
	// detailMaxRunes 是补充说明的字数上限。库里的列是 varchar(500)，
	// 中文按字符计，200 字留出足够余量，也够把一件事说清楚。
	detailMaxRunes = 200
	// excerptRunes 是列表里评论摘要的长度。约两行，足够判断有没有问题，
	// 又不至于让一页 20 条变成 20 篇长文。
	excerptRunes = 60
	// maxBanDays 与 3650 天（十年）对齐。请求里不提供「永久」，
	// 详见 dto.go 里 handleReq.BanDays 的说明。
	maxBanDays = 3650
)

// reportReasons 是允许的举报分类。
// 用白名单而不是自由文本：分类能被聚合和统计，自由文本只能一条条读。
var reportReasons = map[string]struct{}{
	"spam":    {}, // 广告、刷屏
	"abuse":   {}, // 人身攻击、辱骂
	"porn":    {}, // 色情低俗
	"illegal": {}, // 违法违规
	"other":   {}, // 其他，靠 Detail 说明
}

// ContentRemover 下架一篇内容，签名与 article / comment 两个服务的 Delete 一致。
//
// 用接口而不是直接 import 那两个包：本模块只需要「删掉它」这一件事，
// 而 article 的构造函数牵着 Redis 与 LLM 客户端，为调一个方法把整套依赖带进来没有必要。
type ContentRemover interface {
	Delete(ctx context.Context, id, userID uint) error
}

// UserDirectory 批量取用户展示信息，由 user.UserService 实现。
// 管理端列表一页要显示举报人与被举报作者，逐个查会变成 2N 次查询。
type UserDirectory interface {
	Briefs(ctx context.Context, ids []uint) (map[uint]user.Brief, error)
}

// Entry 是管理端列表的一行，已补齐三张表的信息。
//
// 放在 service 层而不是直接返回响应结构体：补齐这些字段要查用户、文章、评论三张表，
// 属于「组装数据」；handler 只负责把 user.Brief 转成响应结构体。
type Entry struct {
	Report       model.Report
	Reporter     user.Brief
	TargetAuthor user.Brief
	TargetTitle  string
	Excerpt      string
	TargetExists bool
}

// Service 是举报模块的业务入口。
type Service struct {
	db       *gorm.DB
	ban      *ban.Store
	articles ContentRemover
	comments ContentRemover
	users    UserDirectory
}

// NewService 注入数据库、封禁存储、两个内容下架器与用户资料查询，由 router 装配。
func NewService(db *gorm.DB, banStore *ban.Store, articles, comments ContentRemover, users UserDirectory) *Service {
	return &Service{db: db, ban: banStore, articles: articles, comments: comments, users: users}
}

// SubmitInput 是提交举报的入参，handler 解析请求体后原样传入。
type SubmitInput struct {
	TargetType string
	TargetID   uint
	Reason     string
	Detail     string
}

// Submit 记录一条举报，返回落库后的记录。
//
// 返回的错误有四种：domain.ErrInvalidInput（类型、分类、长度不合法）、
// domain.ErrNotFound（被举报的内容不存在或已被删）、
// domain.ErrSelfAction（举报自己）、domain.ErrAlreadyReported（已有待处理的举报）。
func (s *Service) Submit(ctx context.Context, reporterID uint, in SubmitInput) (*model.Report, error) {
	// 先去掉说明的首尾空白，再做长度校验。
	// 顺序反过来的话，200 个字后面跟几个空格就会被判成超长，
	// 而真正落库时又会把这些空格去掉，提示和结果对不上。
	in.Detail = strings.TrimSpace(in.Detail)
	if err := validateSubmit(in); err != nil {
		return nil, err
	}

	targetUserID, articleID, err := s.locateTarget(ctx, in.TargetType, in.TargetID)
	if err != nil {
		return nil, err
	}
	if targetUserID == reporterID {
		// 举报自己没有任何处置可能，只会给管理端添一条待办。
		// 复用 ErrSelfAction 而不是新造哨兵：文案「不能对自己执行这个操作」在这里同样成立。
		return nil, fmt.Errorf("report own content: %w", domain.ErrSelfAction)
	}

	// 同一个人对同一条内容只保留一条待处理举报。
	//
	// 这里是「先查后插」，并发下两个请求可能都通过检查插成两行。
	// 没有加唯一索引，因为处置完之后应当允许再举报（内容可能被恢复后重新违规），
	// MySQL 也没有「仅 pending 时唯一」这种部分索引。重复两行对管理端只是多一条待办，
	// 不会导致错误处置，不值得为它引入额外一张去重表。
	var pending int64
	if err := s.db.WithContext(ctx).Model(&model.Report{}).
		Where("reporter_id = ? AND target_type = ? AND target_id = ? AND status = ?",
			reporterID, in.TargetType, in.TargetID, model.ReportStatusPending).
		Count(&pending).Error; err != nil {
		return nil, fmt.Errorf("count pending reports: %w", err)
	}
	if pending > 0 {
		return nil, domain.ErrAlreadyReported
	}

	rep := &model.Report{
		ReporterID:   reporterID,
		TargetType:   in.TargetType,
		TargetID:     in.TargetID,
		TargetUserID: targetUserID,
		ArticleID:    articleID,
		Reason:       in.Reason,
		Detail:       in.Detail,
		Status:       model.ReportStatusPending,
	}
	if err := s.db.WithContext(ctx).Create(rep).Error; err != nil {
		return nil, fmt.Errorf("create report: %w", err)
	}
	return rep, nil
}

// List 返回指定状态的一页举报，供管理端使用。
//
// status 为空时默认只看 pending：管理员打开页面想处理的是待办，
// 已处置的记录只在主动筛「看看之前怎么判的」时才需要。
func (s *Service) List(ctx context.Context, status string, page, pageSize int) ([]Entry, int64, error) {
	query, err := paging.New(page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	if status == "" {
		status = model.ReportStatusPending
	}
	if !isKnownStatus(status) {
		return nil, 0, fmt.Errorf("%w: unknown status %q", domain.ErrInvalidInput, status)
	}

	// count 与取页分两条链构造。GORM 的链式调用会把条件挂在同一个 statement 上，
	// 复用同一条链先 Count 再 Find 时，Count 会改写 Select 与 Limit，
	// 拿到的行数或字段不一定还是想要的那份。
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Report{}).
		Where("status = ?", status).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count reports by status %s: %w", status, err)
	}

	var rows []model.Report
	if err := s.db.WithContext(ctx).
		Where("status = ?", status).
		// id 作为次序键是必要的：同一秒里插入的多条举报靠 created_at 排不出稳定顺序，
		// 翻页时会出现同一条重复出现或漏掉。
		Order("created_at DESC, id DESC").
		Limit(query.PageSize).Offset(query.Offset()).
		Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list reports by status %s: %w", status, err)
	}
	if len(rows) == 0 {
		return []Entry{}, total, nil
	}

	entries, err := s.enrich(ctx, rows)
	if err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// HandleInput 是处置一条举报的入参。
type HandleInput struct {
	Action  string
	BanDays int
}

// Handle 处置一条举报：驳回、下架或下架并封禁作者。
//
// adminID 来自 token，会写进 HandledBy，用来回答「这条是谁判的」。
// 返回的错误有四种：ErrInvalidInput（动作或天数不合法）、ErrNotFound（举报不存在）、
// ErrReportHandled（已被处理过）、以及下架/封禁时的数据库错误。
func (s *Service) Handle(ctx context.Context, adminID, reportID uint, in HandleInput) error {
	action, err := normalizeAction(in.Action)
	if err != nil {
		return err
	}
	if action == ActionBan && (in.BanDays < 1 || in.BanDays > maxBanDays) {
		return fmt.Errorf("%w: ban_days must be between 1 and %d", domain.ErrInvalidInput, maxBanDays)
	}

	var rep model.Report
	if err := s.db.WithContext(ctx).First(&rep, reportID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("report %d: %w", reportID, domain.ErrNotFound)
		}
		return fmt.Errorf("load report %d: %w", reportID, err)
	}

	// 这里特意不先判断 rep.Status 是不是 pending。
	//
	// 那次判断看着能提前返回，实际挡不住并发：两个管理员可能都读到 pending。
	// 唯一有效的是下面那条带 status 条件的 UPDATE，而多一次预检会让它在测试里
	// 「看起来被覆盖了」——把预检删掉，重复处置的用例才真正压在这条更新上。
	// 两种情况对外结果完全一样：非 pending 时更新命中 0 行，同样返回 ErrReportHandled。

	// 先抢占状态，再执行副作用，这个顺序是为了让「谁来处置」在动手之前就定下来。
	//
	// 反过来（先下架再更新状态）在两人同时操作时会自相矛盾：
	// A 点驳回、B 点封禁，两条路径都先执行了各自的副作用，
	// 然后 B 的条件更新失败——最终举报记录写着「驳回」，内容却已经被删、作者也被封了。
	// 先抢占则只有一个赢家，输家拿到 409 且什么都没做。
	//
	// 代价是副作用失败时状态已经改了。那种情况下把状态退回 pending，
	// 管理员刷新后能重新处置；不回退的话这条举报会永久卡在「已处置」，而内容其实还在。
	res := s.db.WithContext(ctx).Model(&model.Report{}).
		Where("id = ? AND status = ?", reportID, model.ReportStatusPending).
		Updates(map[string]any{
			"status":     statusOf(action),
			"handled_by": adminID,
			"handled_at": time.Now(),
		})
	if res.Error != nil {
		return fmt.Errorf("claim report %d: %w", reportID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("report %d: %w", reportID, domain.ErrReportHandled)
	}

	switch action {
	case ActionRemove:
		if err := s.takeDown(ctx, rep); err != nil {
			s.releaseClaim(ctx, reportID, adminID, err)
			return err
		}
	case ActionBan:
		if err := s.takeDown(ctx, rep); err != nil {
			s.releaseClaim(ctx, reportID, adminID, err)
			return err
		}
		until := time.Now().Add(time.Duration(in.BanDays) * 24 * time.Hour)
		if err := s.ban.Ban(ctx, rep.TargetUserID, until, banReason(rep)); err != nil {
			s.releaseClaim(ctx, reportID, adminID, err)
			return err
		}
	}
	return nil
}

// releaseClaim 把抢占失败的那次处置退回待处理，让管理员能重新点一次。
//
// 两个条件都不能省：id 定位这一行，handled_by 确保只回滚自己刚写的那次——
// 虽然状态已经不是 pending，理论上不会有别人动它，但这条更新本身不值钱，
// 加上这一层可以避免「回滚误伤」这种极难排查的事故。
//
// 回滚失败只记错误日志，不再向上返回：调用方已经在处理副作用失败的错误，
// 再包一层只会把「文章没删掉」这个真正的原因埋起来。此时状态会停在已处置，
// 管理员看到列表里没有它，可以按 id 找开发查日志，不至于静默丢单。
func (s *Service) releaseClaim(ctx context.Context, reportID, adminID uint, cause error) {
	err := s.db.WithContext(ctx).Model(&model.Report{}).
		Where("id = ? AND handled_by = ? AND status <> ?", reportID, adminID, model.ReportStatusPending).
		Updates(map[string]any{
			"status":     model.ReportStatusPending,
			"handled_by": 0,
			"handled_at": nil,
		}).Error
	if err != nil {
		slog.ErrorContext(ctx, "release report claim",
			"report_id", reportID, "admin_id", adminID, "cause", cause, "error", err)
	}
}

// validateSubmit 校验举报的必填项与取值范围。
func validateSubmit(in SubmitInput) error {
	if in.TargetType != model.ReportTargetArticle && in.TargetType != model.ReportTargetComment {
		return fmt.Errorf("%w: unknown target_type %q", domain.ErrInvalidInput, in.TargetType)
	}
	if in.TargetID == 0 {
		return fmt.Errorf("%w: target_id is required", domain.ErrInvalidInput)
	}
	if _, ok := reportReasons[in.Reason]; !ok {
		return fmt.Errorf("%w: unknown reason %q", domain.ErrInvalidInput, in.Reason)
	}
	if textutil.TooLongRunes(in.Detail, detailMaxRunes) {
		return fmt.Errorf("%w: detail exceeds %d characters", domain.ErrInvalidInput, detailMaxRunes)
	}
	return nil
}

// locateTarget 查出被举报内容的作者与所属文章 id。
//
// 顺带完成「目标是否存在」的检查：查询走 GORM 默认作用域，
// 已经被软删的内容查不到，于是举报一条已下架的内容会得到 404。
func (s *Service) locateTarget(ctx context.Context, targetType string, targetID uint) (targetUserID, articleID uint, err error) {
	if targetType == model.ReportTargetArticle {
		var a model.Article
		if err := s.db.WithContext(ctx).Select("id", "user_id").First(&a, targetID).Error; err != nil {
			return 0, 0, wrapTargetErr("article", targetID, err)
		}
		// 文章类举报的 ArticleID 就是它自己，管理端直接拿它拼详情链接。
		return a.UserID, a.ID, nil
	}

	var c model.Comment
	if err := s.db.WithContext(ctx).Select("id", "user_id", "article_id").First(&c, targetID).Error; err != nil {
		return 0, 0, wrapTargetErr("comment", targetID, err)
	}
	return c.UserID, c.ArticleID, nil
}

// wrapTargetErr 把「查不到」翻译成 domain.ErrNotFound，其余错误原样包装。
//
// 不区分「刚被删」与「从来不存在」：对举报人来说结果一样，
// 而区分开来等于提供了一个探测「某个 id 是否存在过」的接口。
func wrapTargetErr(kind string, id uint, err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("report target %s %d: %w", kind, id, domain.ErrNotFound)
	}
	return fmt.Errorf("load report target %s %d: %w", kind, id, err)
}

// enrich 给一页举报补齐举报人、作者与内容信息。
//
// 三张表各查一次（用户、文章、评论），用 IN 批量取，避免 2N+1 次查询。
func (s *Service) enrich(ctx context.Context, rows []model.Report) ([]Entry, error) {
	userIDs := make([]uint, 0, len(rows)*2)
	articleIDs := make([]uint, 0, len(rows)*2)
	commentIDs := make([]uint, 0, len(rows))
	for i := range rows {
		userIDs = append(userIDs, rows[i].ReporterID, rows[i].TargetUserID)
		// 文章类举报时 ArticleID 等于 TargetID，评论类举报时它指向评论所在文章，
		// 两种情况都要取标题，所以统一加进 articleIDs。
		if rows[i].ArticleID != 0 {
			articleIDs = append(articleIDs, rows[i].ArticleID)
		}
		if rows[i].TargetType == model.ReportTargetComment {
			commentIDs = append(commentIDs, rows[i].TargetID)
		}
	}

	briefs, err := s.users.Briefs(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("load report users: %w", err)
	}
	articles, err := s.articleTitles(ctx, articleIDs)
	if err != nil {
		return nil, err
	}
	comments, err := s.commentContents(ctx, commentIDs)
	if err != nil {
		return nil, err
	}

	entries := make([]Entry, 0, len(rows))
	for i := range rows {
		r := rows[i]
		e := Entry{
			Report:       r,
			Reporter:     briefs[r.ReporterID],
			TargetAuthor: briefs[r.TargetUserID],
			TargetTitle:  articles[r.ArticleID],
		}
		if r.TargetType == model.ReportTargetComment {
			if c, ok := comments[r.TargetID]; ok {
				e.Excerpt = textutil.TruncateRunes(c, excerptRunes)
				e.TargetExists = true
			}
			entries = append(entries, e)
			continue
		}
		// 文章类举报：标题存在就说明文章还在。标题是发布时的必填项，
		// 库里不存在空标题的文章，所以用「查到标题」当存在性判据是安全的。
		e.TargetExists = e.TargetTitle != ""
		entries = append(entries, e)
	}
	return entries, nil
}

// articleTitles 批量取文章标题，key 是文章 id。
// 已软删的文章查不到，于是调用方拿不到标题，自然就被判成「内容不存在」。
func (s *Service) articleTitles(ctx context.Context, ids []uint) (map[uint]string, error) {
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []model.Article
	if err := s.db.WithContext(ctx).Select("id", "title").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load reported article titles: %w", err)
	}
	for i := range rows {
		out[rows[i].ID] = rows[i].Title
	}
	return out, nil
}

// commentContents 批量取被举报评论的正文，key 是评论 id。
func (s *Service) commentContents(ctx context.Context, ids []uint) (map[uint]string, error) {
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []model.Comment
	if err := s.db.WithContext(ctx).Select("id", "content").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load reported comments: %w", err)
	}
	for i := range rows {
		out[rows[i].ID] = rows[i].Content
	}
	return out, nil
}

// takeDown 下架被举报的内容。
//
// 以 TargetUserID 作为作者传给 Delete：那边用 `WHERE id = ? AND user_id = ?`
// 一次判断归属并删除，管理员手上只有举报记录，正好有作者 id。
// 内容已经被作者自删或另一位管理员下架时返回 ErrNotFound，这里忽略——
// 管理员要的结果「这条内容不可见」已经达成，报错只会让他重复点。
func (s *Service) takeDown(ctx context.Context, rep model.Report) error {
	var err error
	if rep.TargetType == model.ReportTargetArticle {
		err = s.articles.Delete(ctx, rep.TargetID, rep.TargetUserID)
	} else {
		err = s.comments.Delete(ctx, rep.TargetID, rep.TargetUserID)
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("take down %s %d: %w", rep.TargetType, rep.TargetID, err)
	}
	return nil
}

// normalizeAction 把动作转成小写并校验。
// 容错大小写是为了让 curl 和手写前端都能用，不必背三个大小写敏感的字符串。
func normalizeAction(action string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(action))
	switch normalized {
	case ActionDismiss, ActionRemove, ActionBan:
		return normalized, nil
	default:
		return "", fmt.Errorf("%w: unknown action %q", domain.ErrInvalidInput, action)
	}
}

// statusOf 把动作映射成落库的状态。
// 调用方已经用 normalizeAction 校验过，这里不会再落入 default。
func statusOf(action string) string {
	switch action {
	case ActionRemove:
		return model.ReportStatusRemoved
	case ActionBan:
		return model.ReportStatusBanned
	default:
		return model.ReportStatusDismissed
	}
}

// banReason 生成写进 users.ban_reason 的说明，给管理员日后追溯用。
// 存举报 id 而不是原文：原文可能很长，而举报记录本身一直在库里，
// 顺着 id 就能查到当时是谁举报的什么内容。
func banReason(rep model.Report) string {
	return fmt.Sprintf("report #%d reason=%s", rep.ID, rep.Reason)
}

// isKnownStatus 判断状态是否是四个合法值之一。
func isKnownStatus(status string) bool {
	switch status {
	case model.ReportStatusPending, model.ReportStatusDismissed, model.ReportStatusRemoved, model.ReportStatusBanned:
		return true
	default:
		return false
	}
}
