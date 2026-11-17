package report

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"forum-backend/internal/article"
	"forum-backend/internal/ban"
	"forum-backend/internal/comment"
	"forum-backend/internal/domain"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
	"forum-backend/internal/user"
)

// reportFixture 是一个用例需要的现场：干净库、配好真实依赖的 service、两个用户。
//
// 下架走的是 article / comment 两个服务真正的 Delete，不用假实现：
// 处置真正的副作用（软删、清缓存、移出热榜）就在那里面，
// 换成 stub 就测不到「下架之后文章还能不能查到」这件关键的事。
type reportFixture struct {
	db    *gorm.DB
	svc   *Service
	ban   *ban.Store
	alice *model.User // 举报人
	bob   *model.User // 内容作者
}

// newReportFixture 准备干净库与固定用户。每个用例都从空表开始。
func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()

	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	banStore := ban.NewStore(db)
	return &reportFixture{
		db:    db,
		svc:   NewService(db, banStore, article.NewArticleService(db, rdb, nil), comment.NewCommentService(db), user.NewUserService(db)),
		ban:   banStore,
		alice: testutil.SeedUser(t, db, "alice"),
		bob:   testutil.SeedUser(t, db, "bob"),
	}
}

// submitArticle 让 alice 举报 bob 的一篇文章，返回举报记录与被举报文章。
func (f *reportFixture) submitArticle(t *testing.T, reason string) (*model.Report, *model.Article) {
	t.Helper()

	a := testutil.SeedArticle(t, f.db, f.bob.ID, "被举报的文章")
	rep, err := f.svc.Submit(context.Background(), f.alice.ID, SubmitInput{
		TargetType: model.ReportTargetArticle,
		TargetID:   a.ID,
		Reason:     reason,
	})
	if err != nil {
		t.Fatalf("提交举报失败: %v", err)
	}
	return rep, a
}

// TestSubmitFillsTargetInfo 验证落库时把目标作者与所属文章一起记下来。
//
// 这两列是后续处置的依据：没有 TargetUserID 就没法「封禁作者」，
// 没有 ArticleID 管理端就不知道这条评论挂在哪个主题下面。
func TestSubmitFillsTargetInfo(t *testing.T) {
	f := newReportFixture(t)
	a := testutil.SeedArticle(t, f.db, f.bob.ID, "标题")

	rep, err := f.svc.Submit(context.Background(), f.alice.ID, SubmitInput{
		TargetType: model.ReportTargetArticle,
		TargetID:   a.ID,
		Reason:     "spam",
		Detail:     "  发了好几条广告  ",
	})
	if err != nil {
		t.Fatalf("提交举报失败: %v", err)
	}

	if rep.ReporterID != f.alice.ID {
		t.Errorf("举报人 = %d，期望 %d", rep.ReporterID, f.alice.ID)
	}
	if rep.TargetUserID != f.bob.ID {
		t.Errorf("目标作者 = %d，期望 %d", rep.TargetUserID, f.bob.ID)
	}
	if rep.ArticleID != a.ID {
		t.Errorf("所属文章 = %d，期望 %d", rep.ArticleID, a.ID)
	}
	if rep.Status != model.ReportStatusPending {
		t.Errorf("状态 = %q，期望 pending", rep.Status)
	}
	// 补充说明首尾空格要去掉：前端可能在 textarea 里带进来换行，
	// 原样存进去会让管理端看到一段莫名的空白。
	if rep.Detail != "发了好几条广告" {
		t.Errorf("补充说明 = %q，期望去掉首尾空格", rep.Detail)
	}
}

// TestSubmitRejectsSelf 验证不能举报自己。
// 自己举报自己没有可处置的对象，只会给管理端添一条永远判不清的待办。
func TestSubmitRejectsSelf(t *testing.T) {
	f := newReportFixture(t)
	a := testutil.SeedArticle(t, f.db, f.bob.ID, "自己的文章")

	_, err := f.svc.Submit(context.Background(), f.bob.ID, SubmitInput{
		TargetType: model.ReportTargetArticle,
		TargetID:   a.ID,
		Reason:     "spam",
	})
	if !errors.Is(err, domain.ErrSelfAction) {
		t.Fatalf("错误 = %v，期望 ErrSelfAction", err)
	}
}

// TestSubmitRejectsBadInput 验证类型、分类、长度三处的校验。
// 分类是白名单：放开自由文本的话，管理端拿到的是一堆需要逐条读的句子，
// 也没法统计「这个人被举报最多的是哪一类」。
func TestSubmitRejectsBadInput(t *testing.T) {
	f := newReportFixture(t)
	a := testutil.SeedArticle(t, f.db, f.bob.ID, "标题")
	ctx := context.Background()

	cases := []struct {
		name string
		in   SubmitInput
	}{
		{"未知目标类型", SubmitInput{TargetType: "user", TargetID: a.ID, Reason: "spam"}},
		{"目标 id 为 0", SubmitInput{TargetType: model.ReportTargetArticle, TargetID: 0, Reason: "spam"}},
		{"未知分类", SubmitInput{TargetType: model.ReportTargetArticle, TargetID: a.ID, Reason: "因为我不喜欢"}},
		{"补充说明超长", SubmitInput{TargetType: model.ReportTargetArticle, TargetID: a.ID, Reason: "other", Detail: repeatRunes(201)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.Submit(ctx, f.alice.ID, tc.in); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("错误 = %v，期望 ErrInvalidInput", err)
			}
		})
	}
}

// TestSubmitRejectsMissingTarget 验证举报不存在或已被删的内容返回 404。
// 已软删的文章查不到，所以「内容下架后又被举报」会走这条路径。
func TestSubmitRejectsMissingTarget(t *testing.T) {
	f := newReportFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Submit(ctx, f.alice.ID, SubmitInput{
		TargetType: model.ReportTargetArticle,
		TargetID:   99999,
		Reason:     "spam",
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误 = %v，期望 ErrNotFound", err)
	}
}

// TestSubmitBlocksDuplicatePending 验证同一个人对同一条内容只能有一条待处理举报，
// 且处置完之后可以再报——内容被恢复后重新违规是真实存在的场景。
func TestSubmitBlocksDuplicatePending(t *testing.T) {
	f := newReportFixture(t)
	ctx := context.Background()
	rep, a := f.submitArticle(t, "spam")

	_, err := f.svc.Submit(ctx, f.alice.ID, SubmitInput{
		TargetType: model.ReportTargetArticle,
		TargetID:   a.ID,
		Reason:     "abuse",
	})
	if !errors.Is(err, domain.ErrAlreadyReported) {
		t.Fatalf("重复举报错误 = %v，期望 ErrAlreadyReported", err)
	}

	// 另一个用户举报同一条内容不受影响：待办是按「举报人」去重的，
	// 多人举报同一篇能帮助管理员判断影响面。
	carol := testutil.SeedUser(t, f.db, "carol")
	if _, err := f.svc.Submit(ctx, carol.ID, SubmitInput{
		TargetType: model.ReportTargetArticle,
		TargetID:   a.ID,
		Reason:     "abuse",
	}); err != nil {
		t.Fatalf("另一个用户举报同一内容失败: %v", err)
	}

	// 把第一条置为已处置后再报一次，应当成功。
	if err := f.db.Model(&model.Report{}).Where("id = ?", rep.ID).
		Update("status", model.ReportStatusDismissed).Error; err != nil {
		t.Fatalf("更新状态失败: %v", err)
	}
	if _, err := f.svc.Submit(ctx, f.alice.ID, SubmitInput{
		TargetType: model.ReportTargetArticle,
		TargetID:   a.ID,
		Reason:     "spam",
	}); err != nil {
		t.Fatalf("处置后再次举报失败: %v", err)
	}
}

// TestHandleDismissKeepsContent 验证驳回只改举报状态，内容原样保留。
// 误报是常态，驳回必须是一条「什么都不做」的路径。
func TestHandleDismissKeepsContent(t *testing.T) {
	f := newReportFixture(t)
	rep, a := f.submitArticle(t, "spam")

	if err := f.svc.Handle(context.Background(), f.alice.ID, rep.ID, HandleInput{Action: "dismiss"}); err != nil {
		t.Fatalf("驳回失败: %v", err)
	}

	var got model.Report
	if err := f.db.First(&got, rep.ID).Error; err != nil {
		t.Fatalf("读取举报失败: %v", err)
	}
	if got.Status != model.ReportStatusDismissed {
		t.Errorf("状态 = %q，期望 dismissed", got.Status)
	}
	if got.HandledBy != f.alice.ID || got.HandledAt == nil {
		t.Errorf("处置人/时间未记录: handled_by=%d handled_at=%v", got.HandledBy, got.HandledAt)
	}

	var kept model.Article
	if err := f.db.First(&kept, a.ID).Error; err != nil {
		t.Fatalf("驳回后文章不该被删: %v", err)
	}
}

// TestHandleRemoveTakesDownArticle 验证 remove 下架内容但不动作者。
func TestHandleRemoveTakesDownArticle(t *testing.T) {
	f := newReportFixture(t)
	rep, a := f.submitArticle(t, "porn")

	if err := f.svc.Handle(context.Background(), f.alice.ID, rep.ID, HandleInput{Action: "remove"}); err != nil {
		t.Fatalf("下架失败: %v", err)
	}

	var got model.Report
	if err := f.db.First(&got, rep.ID).Error; err != nil {
		t.Fatalf("读取举报失败: %v", err)
	}
	if got.Status != model.ReportStatusRemoved {
		t.Errorf("状态 = %q，期望 removed", got.Status)
	}

	// 普通查询查不到 = 软删生效；Unscoped 能查到 = 数据还在，为以后恢复留了余地。
	if err := f.db.First(&model.Article{}, a.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("下架后文章仍可查到，err = %v", err)
	}
	var raw model.Article
	if err := f.db.Unscoped().First(&raw, a.ID).Error; err != nil {
		t.Fatalf("软删后原始行应当仍在: %v", err)
	}
	if !raw.DeletedAt.Valid {
		t.Error("deleted_at 未写入，说明是硬删而不是软删")
	}

	// 作者本身没被处罚。
	if until, err := f.ban.BannedUntil(context.Background(), f.bob.ID); err != nil || !until.IsZero() {
		t.Errorf("remove 不该封禁作者，until = %v, err = %v", until, err)
	}
}

// TestHandleBanTakesDownAndBansAuthor 验证 ban 是「下架 + 封禁」两件事一起做。
func TestHandleBanTakesDownAndBansAuthor(t *testing.T) {
	f := newReportFixture(t)
	rep, a := f.submitArticle(t, "illegal")

	before := time.Now()
	if err := f.svc.Handle(context.Background(), f.alice.ID, rep.ID, HandleInput{Action: "ban", BanDays: 7}); err != nil {
		t.Fatalf("封禁失败: %v", err)
	}

	var got model.Report
	if err := f.db.First(&got, rep.ID).Error; err != nil {
		t.Fatalf("读取举报失败: %v", err)
	}
	if got.Status != model.ReportStatusBanned {
		t.Errorf("状态 = %q，期望 banned", got.Status)
	}
	if err := f.db.First(&model.Article{}, a.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("封禁应当连带下架内容，err = %v", err)
	}

	until, err := f.ban.BannedUntil(context.Background(), f.bob.ID)
	if err != nil {
		t.Fatalf("读取封禁状态失败: %v", err)
	}
	// 7 天允许一分钟的误差：用例里算一次期望值，服务里又算一次当前时间。
	expected := before.Add(7 * 24 * time.Hour)
	if diff := until.Sub(expected); diff < -time.Minute || diff > time.Minute {
		t.Errorf("解除时间 = %v，期望约 %v", until, expected)
	}
}

// TestHandleBanRequiresDays 验证封禁必须给天数。
// 不给天数就默认永久是最危险的默认值，所以这里要求显式指定。
func TestHandleBanRequiresDays(t *testing.T) {
	f := newReportFixture(t)
	rep, _ := f.submitArticle(t, "spam")
	ctx := context.Background()

	for _, days := range []int{0, -1, maxBanDays + 1} {
		if err := f.svc.Handle(ctx, f.alice.ID, rep.ID, HandleInput{Action: "ban", BanDays: days}); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("ban_days=%d 错误 = %v，期望 ErrInvalidInput", days, err)
		}
	}
}

// TestHandleTwiceConflicts 验证同一条举报只能处置一次。
//
// 这是条件更新守住的语义：两位管理员同时打开列表时，
// 后点的那个人必须知道「别人已经判过了」，而不是静默覆盖前一个人的结论。
func TestHandleTwiceConflicts(t *testing.T) {
	f := newReportFixture(t)
	rep, _ := f.submitArticle(t, "spam")
	ctx := context.Background()

	if err := f.svc.Handle(ctx, f.alice.ID, rep.ID, HandleInput{Action: "dismiss"}); err != nil {
		t.Fatalf("第一次处置失败: %v", err)
	}
	if err := f.svc.Handle(ctx, f.bob.ID, rep.ID, HandleInput{Action: "remove"}); !errors.Is(err, domain.ErrReportHandled) {
		t.Fatalf("第二次处置错误 = %v，期望 ErrReportHandled", err)
	}
}

// TestHandleRemoveComment 验证评论类举报走的是评论的下架逻辑。
// 评论与文章是两套服务，把目标类型判断写反了在这里会暴露。
func TestHandleRemoveComment(t *testing.T) {
	f := newReportFixture(t)
	ctx := context.Background()

	a := testutil.SeedArticle(t, f.db, f.bob.ID, "标题")
	c := &model.Comment{ArticleID: a.ID, UserID: f.bob.ID, Content: "这是一条广告"}
	if err := f.db.Create(c).Error; err != nil {
		t.Fatalf("插入评论失败: %v", err)
	}

	rep, err := f.svc.Submit(ctx, f.alice.ID, SubmitInput{
		TargetType: model.ReportTargetComment,
		TargetID:   c.ID,
		Reason:     "spam",
	})
	if err != nil {
		t.Fatalf("提交评论举报失败: %v", err)
	}
	// 评论类举报的 ArticleID 要指向评论所在的文章，管理端靠它拼详情链接。
	if rep.ArticleID != a.ID {
		t.Errorf("所属文章 = %d，期望 %d", rep.ArticleID, a.ID)
	}

	if err := f.svc.Handle(ctx, f.alice.ID, rep.ID, HandleInput{Action: "remove"}); err != nil {
		t.Fatalf("下架评论失败: %v", err)
	}
	if err := f.db.First(&model.Comment{}, c.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("评论未被下架，err = %v", err)
	}
	// 文章本身不该被牵连。
	if err := f.db.First(&model.Article{}, a.ID).Error; err != nil {
		t.Errorf("下架评论不该删文章: %v", err)
	}
}

// TestListEnrichesRows 验证管理端列表补齐了举报人、作者、标题与摘要。
//
// 管理员要在一屏里判断「该不该下架」，缺任何一项他都要多点开一次，
// 而这类操作一天可能上百次。
func TestListEnrichesRows(t *testing.T) {
	f := newReportFixture(t)
	ctx := context.Background()

	a := testutil.SeedArticle(t, f.db, f.bob.ID, "一篇讲索引的文章")
	c := &model.Comment{ArticleID: a.ID, UserID: f.bob.ID, Content: "评论正文"}
	if err := f.db.Create(c).Error; err != nil {
		t.Fatalf("插入评论失败: %v", err)
	}

	if _, err := f.svc.Submit(ctx, f.alice.ID, SubmitInput{
		TargetType: model.ReportTargetArticle, TargetID: a.ID, Reason: "spam",
	}); err != nil {
		t.Fatalf("举报文章失败: %v", err)
	}
	if _, err := f.svc.Submit(ctx, f.alice.ID, SubmitInput{
		TargetType: model.ReportTargetComment, TargetID: c.ID, Reason: "abuse",
	}); err != nil {
		t.Fatalf("举报评论失败: %v", err)
	}

	entries, total, err := f.svc.List(ctx, "", 1, 10)
	if err != nil {
		t.Fatalf("读取列表失败: %v", err)
	}
	if total != 2 || len(entries) != 2 {
		t.Fatalf("total = %d，条目数 = %d，期望都是 2", total, len(entries))
	}

	byType := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byType[e.Report.TargetType] = e
	}

	articleEntry := byType[model.ReportTargetArticle]
	if articleEntry.TargetTitle != "一篇讲索引的文章" {
		t.Errorf("文章标题 = %q", articleEntry.TargetTitle)
	}
	if !articleEntry.TargetExists {
		t.Error("文章还在，TargetExists 应为 true")
	}
	if articleEntry.Reporter.Username != "alice" || articleEntry.TargetAuthor.Username != "bob" {
		t.Errorf("举报人/作者 = %q/%q，期望 alice/bob", articleEntry.Reporter.Username, articleEntry.TargetAuthor.Username)
	}

	commentEntry := byType[model.ReportTargetComment]
	if commentEntry.Excerpt != "评论正文" {
		t.Errorf("评论摘要 = %q", commentEntry.Excerpt)
	}
	// 评论类举报也要带文章标题，否则管理员不知道这条评论挂在什么主题下面。
	if commentEntry.TargetTitle != "一篇讲索引的文章" {
		t.Errorf("评论所属文章标题 = %q", commentEntry.TargetTitle)
	}
}

// TestListMarksTakenDownContent 验证内容被下架后，列表里 TargetExists 变成 false。
// 管理员靠这个字段判断「这条不用再点了」，否则会对同一篇文章反复下架然后拿到 404。
func TestListMarksTakenDownContent(t *testing.T) {
	f := newReportFixture(t)
	ctx := context.Background()

	rep, _ := f.submitArticle(t, "spam")
	if err := f.svc.Handle(ctx, f.alice.ID, rep.ID, HandleInput{Action: "remove"}); err != nil {
		t.Fatalf("下架失败: %v", err)
	}

	entries, _, err := f.svc.List(ctx, model.ReportStatusRemoved, 1, 10)
	if err != nil {
		t.Fatalf("读取列表失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("条目数 = %d，期望 1", len(entries))
	}
	if entries[0].TargetExists {
		t.Error("内容已下架，TargetExists 应为 false")
	}
}

// failingRemover 是永远失败的下架器，用来验证「副作用失败后举报要退回待处理」。
// 真实环境里这种失败极罕见（同一个数据库刚写成功过），但一旦发生而不回退，
// 这条举报就会永久停在已处置，管理员既看不到它也没法再点一次。
type failingRemover struct{}

func (failingRemover) Delete(context.Context, uint, uint) error {
	return errors.New("下架失败")
}

// TestHandleRestoresPendingWhenSideEffectFails 验证副作用失败时状态回滚。
func TestHandleRestoresPendingWhenSideEffectFails(t *testing.T) {
	f := newReportFixture(t)
	ctx := context.Background()
	rep, _ := f.submitArticle(t, "spam")

	// 换成一个下架必然失败的服务，其余依赖用真实的。
	broken := NewService(f.db, f.ban, failingRemover{}, failingRemover{}, user.NewUserService(f.db))
	err := broken.Handle(ctx, f.alice.ID, rep.ID, HandleInput{Action: "remove"})
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误 = %v，期望下架失败的错误", err)
	}

	var got model.Report
	if err := f.db.First(&got, rep.ID).Error; err != nil {
		t.Fatalf("读取举报失败: %v", err)
	}
	if got.Status != model.ReportStatusPending {
		t.Errorf("状态 = %q，期望退回 pending", got.Status)
	}
	if got.HandledBy != 0 || got.HandledAt != nil {
		t.Errorf("回滚后处置人/时间应清空，实际 %d / %v", got.HandledBy, got.HandledAt)
	}
}

// repeatRunes 造一个指定字数的说明文本，用来测长度上限。
// 单独包一层是为了让用例里那行不被构造切片的代码淹没。
func repeatRunes(n int) string {
	buf := make([]rune, n)
	for i := range buf {
		buf[i] = '字'
	}
	return string(buf)
}
