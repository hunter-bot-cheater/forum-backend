// Package notification 放站内通知相关的 handler、service 与后台任务：
// 把 outbox 事件投进 Redis Stream、消费事件生成站内通知并推送给在线收件人。
package notification

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
	"forum-backend/internal/paging"
	"forum-backend/internal/user"
)

// NotificationService 负责站内通知的读取与已读标记。
// 通知的写入不在这里：写入发生在 NotificationWorker 消费事件时。
type NotificationService struct {
	db *gorm.DB

	// users 用来把通知里的 actor_id 翻译成可展示的昵称头像。
	// 列表只有 id 的话，前端只能显示「有人赞了你」，等于没告诉用户是谁。
	users *user.UserService
}

// NewNotificationService 由装配层（router 的 Deps）在启动时调用一次。
func NewNotificationService(db *gorm.DB, users *user.UserService) *NotificationService {
	return &NotificationService{db: db, users: users}
}

// Item 是一条通知加上触发者的展示信息。
//
// 用结构体包起来而不是让 model.Notification 带一个 Actor 字段：
// 模型是数据库行的镜像，混进联表结果以后，任何一次 Save/Updates
// 都可能把这份展示数据当成列写回库。
type Item struct {
	Notice *model.Notification
	// Actor 拿不到时是零值（账号已注销），前端按「有人」兜底。
	Actor user.Brief
	// ArticleTitle 是互动发生在哪篇文章。
	// 通知表只存 article_id，前端拿不到标题就只能显示「赞了你的文章」，
	// 用户必须点进去才知道是哪篇。文章已删时为空串，前端不带标题。
	ArticleTitle string
}

// 通知分类。库里存的是事件类型（like.created），前端 tab 用的是分类（like），
// 中间这一层映射在 notify_resolver.go 的 notificationHandlers 里，
// 和 resolver 共用一张表：分类比事件类型粗，一种分类可能对应多个事件类型，
// 让前端自己去拆事件类型字符串，后端一改事件名前端就跟着坏。
const (
	KindLike    = "like"
	KindComment = "comment"
	KindFollow  = "follow"
)

// UnreadSummary 是各分类的未读数，对应前端每个 tab 上的红点数字。
// Total 是全部未读，和 /notifications 返回的 unread_count 口径一致。
type UnreadSummary struct {
	Total  int64
	ByKind map[string]int64
}

// UnreadSummary 按事件类型分组统计未读数。
//
// 用一条 GROUP BY 代替「每个分类查一次」：分类以后只会变多，
// 每加一类就多一次往返，而且几个数字之间可能来自不同时刻，看起来会互相矛盾。
func (s *NotificationService) UnreadSummary(ctx context.Context, userID uint) (UnreadSummary, error) {
	// 分组结果先落到这个临时结构，类型名与 SELECT 的别名对应。
	type typeCount struct {
		Type  string
		Count int64
	}
	rows := make([]typeCount, 0, len(notificationKinds))

	err := s.db.WithContext(ctx).Model(&model.Notification{}).
		Select("type, COUNT(*) AS count").
		Where("user_id = ? AND is_read = ?", userID, false).
		Group("type").
		Scan(&rows).Error
	if err != nil {
		return UnreadSummary{}, fmt.Errorf("count unread notifications by type: %w", err)
	}

	summary := UnreadSummary{ByKind: make(map[string]int64, len(notificationKinds))}
	// 先把所有分类补成 0，前端拿到的 map 永远是完整的一份，不用判 undefined。
	for _, kind := range notificationKinds {
		summary.ByKind[kind] = 0
	}
	for _, row := range rows {
		summary.Total += row.Count
		kind := kindOfEventType(row.Type)
		if kind == "" {
			// 未登记分类的事件类型只计入总数，不塞进任何 tab。
			continue
		}
		summary.ByKind[kind] += row.Count
	}
	return summary, nil
}

// List 返回当前用户的通知分页、总数与未读数。
// kind 为空表示「全部」，否则只返回该分类（like/comment/follow）。
// 返回值顺序是：这一页的数据、总数、未读数、错误。
//
// 三条查询各自新开一条链，不复用带 Where 的中间变量：
// gorm 的链式调用会把条件累积在同一个 *gorm.DB 上，
// 复用会让第二条 Count 也带上 Limit/Offset，算出来的总数是错的。
func (s *NotificationService) List(ctx context.Context, userID uint, kind string, page, pageSize int) ([]Item, int64, int64, error) {
	q, err := paging.New(page, pageSize)
	if err != nil {
		return nil, 0, 0, err
	}

	// 分类过滤先翻译成事件类型集合。未知分类直接拒绝：
	// 静默当成「全部」会让前端 tab 看起来点了没反应。
	types, err := eventTypesOfKind(kind)
	if err != nil {
		return nil, 0, 0, err
	}

	// total / unread / list 三条链各自独立构造，过滤条件用同一个函数加，
	// 避免「列表过滤了、计数没过滤」导致 tab 上的数字和列表长度对不上。
	totalQuery := s.db.WithContext(ctx).Model(&model.Notification{}).Where("user_id = ?", userID)
	unreadQuery := s.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false)
	listQuery := s.db.WithContext(ctx).Where("user_id = ?", userID)
	if len(types) > 0 {
		totalQuery = totalQuery.Where("type IN ?", types)
		unreadQuery = unreadQuery.Where("type IN ?", types)
		listQuery = listQuery.Where("type IN ?", types)
	}

	// 总数：只用来给前端算总页数。
	var total int64
	if err := totalQuery.Count(&total).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("count notifications: %w", err)
	}

	// 未读数：前端红点用，走 idx_notify_user_read 索引。
	var unread int64
	if err := unreadQuery.Count(&unread).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("count unread notifications: %w", err)
	}

	// 本页数据：按 id 倒序，id 自增所以等价于按时间倒序，且比 created_at 排序稳定。
	list := make([]*model.Notification, 0)
	err = listQuery.
		Order("id DESC").
		Offset(q.Offset()).Limit(q.PageSize).
		Find(&list).Error
	if err != nil {
		return nil, 0, 0, fmt.Errorf("list notifications: %w", err)
	}

	// 展示信息单独一次批量查，不 join users：
	// 通知表按用户分页，join 会让 MySQL 先去连表再排序，
	// 而这里拿到一页 id 之后再 IN 一次是固定 2 次往返，且不影响索引选择。
	items, err := s.attachActors(ctx, list)
	if err != nil {
		return nil, 0, 0, err
	}

	// 文章标题同样批量补。放在演员之后：两次都是按本页 id 的 IN 查询，
	// 顺序对结果没影响，但先拿演员能保证 items 一定已经就绪。
	if err := s.attachArticleTitles(ctx, items); err != nil {
		return nil, 0, 0, err
	}
	return items, total, unread, nil
}

// attachArticleTitles 给一页通知补上「互动发生在哪篇文章」。
//
// 和 attachActors 同一个思路：先收集本页的 article_id 去重，再一次 IN 查完。
// 不把标题塞进分页查询的 join 里，避免连表影响 idx_notify_user_read 的选择。
//
// 查不到的 id（文章已删）保持空串：通知本身要保留，前端只显示动作不带标题，
// 点进去由详情页的 404 空态兜住。
func (s *NotificationService) attachArticleTitles(ctx context.Context, items []Item) error {
	// article_id 为 0 的是非文章域事件（例如关注），不参与查询。
	ids := make([]uint, 0, len(items))
	seen := make(map[uint]struct{}, len(items))
	for _, item := range items {
		id := item.Notice.ArticleID
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}

	// 只取 id 与 title：正文单篇上限 50000 字节，
	// 为了一行标题把整页正文搬回来没有意义。
	type articleTitleRow struct {
		ID    uint
		Title string
	}
	rows := make([]articleTitleRow, 0, len(ids))
	if err := s.db.WithContext(ctx).Model(&model.Article{}).
		Select("id, title").
		Where("id IN ?", ids).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("load notification article titles: %w", err)
	}

	titles := make(map[uint]string, len(rows))
	for _, row := range rows {
		titles[row.ID] = row.Title
	}
	// 用索引回写：Item 是值类型，range 出来的副本改了不会影响切片里的元素。
	for i := range items {
		items[i].ArticleTitle = titles[items[i].Notice.ArticleID]
	}
	return nil
}

// attachActors 给一页通知补上触发者的昵称头像。
// 顺序必须和入参一致：前端按这个顺序渲染列表，重排会让分页看起来错位。
func (s *NotificationService) attachActors(ctx context.Context, notices []*model.Notification) ([]Item, error) {
	items := make([]Item, 0, len(notices))
	if len(notices) == 0 {
		return items, nil
	}

	// 先把 actor_id 收集成切片，Briefs 内部会去重并一次 IN 查完。
	ids := make([]uint, 0, len(notices))
	for _, notice := range notices {
		ids = append(ids, notice.ActorID)
	}
	briefs, err := s.users.Briefs(ctx, ids)
	if err != nil {
		// 展示信息查不出来就让整个列表失败，而不是回一页「有人」：
		// 消息中心的主要用途就是看「谁」，缺了主语这一页没有价值，
		// 但静默降级会让人以为是数据丢了，排查成本更高。
		return nil, fmt.Errorf("load notification actors: %w", err)
	}

	for _, notice := range notices {
		item := Item{Notice: notice}
		brief, ok := briefs[notice.ActorID]
		if ok {
			item.Actor = brief
		}
		items = append(items, item)
	}
	return items, nil
}

// MarkAllRead 把当前用户的未读通知一次改完，返回受影响行数。
// 一条 UPDATE 搞定，不逐条回写；前端拿 affected 判断要不要提示「没有未读」。
func (s *NotificationService) MarkAllRead(ctx context.Context, userID uint) (int64, error) {
	res := s.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Update("is_read", true)
	if res.Error != nil {
		return 0, fmt.Errorf("mark notifications read: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// MarkRead 把一条通知标记为已读，返回受影响行数（已读过是 0）。
//
// 用户点开一条消息就该消掉它自己的红点，不然只能整页「全部已读」，
// 会把还没看的那些也一起清掉。
//
// 先查再改而不是直接 UPDATE：MySQL 在「新值与旧值相同」时返回 RowsAffected=0，
// 只看影响行数分不清「已经读过」和「不是我的通知」，
// 前者应当安静成功，后者必须 404。多一次主键查询换一个确定的语义，值得。
func (s *NotificationService) MarkRead(ctx context.Context, userID, id uint) (int64, error) {
	var notice model.Notification
	err := s.db.WithContext(ctx).
		Select("id", "is_read").
		Where("id = ? AND user_id = ?", id, userID).
		First(&notice).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 不存在与不属于这个用户合并成 404，不泄露「这条通知属于别人」。
		return 0, fmt.Errorf("notification %d: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("load notification %d: %w", id, err)
	}
	if notice.IsRead {
		// 重复点击、多端同时点开都是正常情况，幂等返回 0。
		return 0, nil
	}

	res := s.db.WithContext(ctx).Model(&model.Notification{}).
		Where("id = ? AND user_id = ? AND is_read = ?", id, userID, false).
		Update("is_read", true)
	if res.Error != nil {
		return 0, fmt.Errorf("mark notification %d read: %w", id, res.Error)
	}
	return res.RowsAffected, nil
}
