// Package comment 实现文章评论的增删查。
package comment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/event"
	"forum-backend/internal/model"
	"forum-backend/internal/paging"
	"forum-backend/internal/textutil"
	"forum-backend/internal/user"
)

// 评论列表的固定排序：按创建时间升序，同秒创建的记录用 id 决定先后。
// 放在本文件而不是 article.go，因为它只服务评论查询。
const commentListOrder = "created_at ASC, id ASC"

// maxCommentContentBytes 是一条评论的字节上限，与 comments.content 的 text 列对齐。
// 用字节而不是字符：text 列的容量本身就是字节数，按字符判会让长中文评论绕过上限。
const maxCommentContentBytes = 10000

// preloadAuthor 限定 Preload("User") 只取展示作者需要的列。
// 不带参数的 Preload 会发 SELECT *，把 bcrypt 密码哈希读进内存；
// 评论列表一页 20 条，等于白搬 20 个哈希，而且这些字段 DTO 根本用不到。
func preloadAuthor(db *gorm.DB) *gorm.DB {
	return db.Select("id", "username", "nickname", "avatar_url")
}

// CommentService 评论业务逻辑。方法名统一用动词：List / Create / Delete。
type CommentService struct {
	db *gorm.DB
}

// NewCommentService 由装配层在启动时调用一次。
func NewCommentService(db *gorm.DB) *CommentService {
	return &CommentService{db: db}
}

// maxRepliesPerRoot 是**每条**根评论最多带多少条回复。
//
// 不设上限时，一条爆款评论下面几百条回复会跟着列表一起回来，
// 一页 20 条根评论就可能拉出上万行。截断后靠 ReplyTotal 告诉前端「还有多少条」。
//
// 配额是按根评论算的，不是整页共用一个数：早先写成
// Limit(len(roots) * maxRepliesPerRoot)，一条热帖的回复就能把整页配额吃光，
// 排在它后面的根评论一条回复都带不回来，而 reply_count 又显示几百，
// 前端会以为回复丢了。SQL 侧见 replyRankSubquery。
const maxRepliesPerRoot = 200

// ReplyItem 是一条回复，外加它回复的那个人。
// 单独包一层是因为「回复 @谁」不在 Comment 表里：被回复者要靠 ParentID 回查，
// 让 model.Comment 挂一个展示字段会污染模型（Save 时容易被当成列写回库）。
type ReplyItem struct {
	Comment *model.Comment
	// ReplyTo 是被回复者的展示信息，父评论作者注销时为零值。
	ReplyTo user.Brief
}

// Thread 是一条根评论连同它下面的回复。
// Replies 只装本页展示的部分，ReplyTotal 是真实总数，前端据此决定要不要显示「查看全部」。
type Thread struct {
	Comment    *model.Comment
	Replies    []ReplyItem
	ReplyTotal int64
}

// List 按文章分页查根评论（顶级评论），并为这一页的每条根评论补上回复。
//
// 分页只对根评论做：回复不参与分页，否则「第三条回复跑到第二页去了」，
// 前端拼不回一棵完整的树。
func (s *CommentService) List(ctx context.Context, articleID uint, page, pageSize int) ([]Thread, int64, error) {
	// 分页兜底校验放在 service，将来有内部调用绕过 handler 时也拦得住。
	// 上下界与偏移量统一由 paging 提供，避免和别的列表接口各写一套数字。
	q, err := paging.New(page, pageSize)
	if err != nil {
		return nil, 0, err
	}

	// 先确认文章还在。评论行本身不会跟着文章一起被标记，
	// 不校验的话 /articles/9/comments 仍会把已删文章的评论全部列出来，
	// 作者以为删干净了，实际任何人都还能读。
	// 用 Count 而不是 First：这里只要「在不在」，不必把 50KB 的正文取回内存。
	// Model(&model.Article{}) 让 GORM 自动带上 deleted_at IS NULL。
	var articleExists int64
	if err := s.db.WithContext(ctx).Model(&model.Article{}).
		Where("id = ?", articleID).Count(&articleExists).Error; err != nil {
		return nil, 0, fmt.Errorf("check article %d: %w", articleID, err)
	}
	if articleExists == 0 {
		return nil, 0, fmt.Errorf("article %d: %w", articleID, domain.ErrNotFound)
	}

	// Count 与 Find 各自开一条新链，不复用带 Where 的中间变量，避免条件叠加。
	// 只数根评论：列表按根评论分页，总数也必须按根评论算，否则页数对不上。
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Comment{}).
		Where("article_id = ? AND root_id = ?", articleID, 0).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count comments of article %d: %w", articleID, err)
	}
	if total == 0 {
		return []Thread{}, 0, nil
	}

	// 初始化成空切片：页越界时 Find 不填充，JSON 仍是 [] 而不是 null。
	roots := make([]*model.Comment, 0)
	err = s.db.WithContext(ctx).
		Preload("User", preloadAuthor).
		Where("article_id = ? AND root_id = ?", articleID, 0).
		Order(commentListOrder).
		Offset(q.Offset()).Limit(q.PageSize).
		Find(&roots).Error
	if err != nil {
		return nil, total, fmt.Errorf("list comments of article %d: %w", articleID, err)
	}
	if len(roots) == 0 {
		return []Thread{}, total, nil
	}

	threads, err := s.attachReplies(ctx, roots)
	if err != nil {
		return nil, total, err
	}
	return threads, total, nil
}

// Anchor 描述一条评论在文章评论列表里的位置，供「从通知点进来直接落到那条评论」使用。
//
// 只回位置不回内容：内容由评论列表接口给，前端拿到页码后再按页拉取。
// 位置之所以要在服务端算，是因为列表按根评论分页，
// 前端手上只有一个评论 id，自己数不出它在第几页。
type Anchor struct {
	CommentID uint
	ArticleID uint
	// RootID 是它所属的根评论；它自己就是根评论时等于 CommentID。
	RootID uint
	// Index 是该根评论在文章里的序号，排序与 commentListOrder 一致，从 1 开始。
	// 前端折算页码：page = ceil(Index / page_size)，page_size 用前端自己的，
	// 服务端不掺和，避免将来前端改成一页 30 条还得动这个接口。
	Index int64
}

// Locate 查一条评论的定位信息。评论不存在（含所属根评论已删）时返回 domain.ErrNotFound。
func (s *CommentService) Locate(ctx context.Context, id uint) (Anchor, error) {
	// 只取定位要用的列，不 Preload 作者：这个接口不渲染任何内容。
	var target model.Comment
	err := s.db.WithContext(ctx).
		Select("id", "article_id", "root_id", "created_at").
		First(&target, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Anchor{}, fmt.Errorf("locate comment %d: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return Anchor{}, fmt.Errorf("locate comment %d: %w", id, err)
	}

	// 回复一律定位到它所属的根评论：回复不参与分页，跟着根评论一起下发，
	// 所以要先知道是哪一棵树，才能算出树的位置。
	anchor := Anchor{CommentID: target.ID, ArticleID: target.ArticleID, RootID: target.ID}
	rootCreatedAt := target.CreatedAt
	if target.RootID != 0 {
		// 根评论的 created_at 单独查一次，而不是拿目标的：两者不是同一行。
		// 用库里的值做下面的比较，顺带避开 Go 侧时间精度与 DATETIME 列不一致的问题。
		var root model.Comment
		err = s.db.WithContext(ctx).Select("id", "created_at").First(&root, target.RootID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 删根评论会级联删回复，理论上走不到这里；真出现就按「这条评论也没了」处理，
			// 前端统一落到「评论已删除」的提示上。
			return Anchor{}, fmt.Errorf("locate root %d: %w", target.RootID, domain.ErrNotFound)
		}
		if err != nil {
			return Anchor{}, fmt.Errorf("locate root %d: %w", target.RootID, err)
		}
		anchor.RootID = root.ID
		rootCreatedAt = root.CreatedAt
	}

	// 序号 = 排在它前面的根评论条数 + 1，用 COUNT 算，不把评论查回来数：
	// 一条热帖几千条评论，为了知道位置全查回来没有意义。
	// 条件必须与 commentListOrder 严格对应（created_at ASC, id ASC）：
	// 少了 id 这个平手项，同一秒创建的多条评论会算出同一个序号。
	var before int64
	err = s.db.WithContext(ctx).Model(&model.Comment{}).
		Where("article_id = ? AND root_id = 0 AND (created_at < ? OR (created_at = ? AND id <= ?))",
			anchor.ArticleID, rootCreatedAt, rootCreatedAt, anchor.RootID).
		Count(&before).Error
	if err != nil {
		return Anchor{}, fmt.Errorf("count roots before comment %d: %w", anchor.RootID, err)
	}
	anchor.Index = before
	return anchor, nil
}

// attachReplies 给一页根评论补上回复、回复对象与各自的回复总数。
//
// 三次查询代替逐条查：回复一次 IN、被回复者一次 IN、计数一次 GROUP BY，
// 不管本页有几条根评论都是固定 3 次往返。
func (s *CommentService) attachReplies(ctx context.Context, roots []*model.Comment) ([]Thread, error) {
	rootIDs := make([]uint, 0, len(roots))
	for _, root := range roots {
		rootIDs = append(rootIDs, root.ID)
	}

	// 回复按 id 升序：对话要按发生顺序读，最新的在最下面。
	//
	// 用窗口函数按根评论各取前 maxRepliesPerRoot 条，不能退化成全局 Limit：
	// 全局 Limit 是整页共享的配额，被一条热帖吃光后其余根评论就空了。
	// ROW_NUMBER() 是 MySQL 8 的窗口函数，项目要求本来就是 8.x；
	// 走 idx_comment_root(root_id, parent_id)，二级索引尾部自带主键，
	// 所以 PARTITION BY root_id ORDER BY id 能顺着索引读，不必额外排序。
	ranked := s.db.WithContext(ctx).Model(&model.Comment{}).
		Select("id, ROW_NUMBER() OVER (PARTITION BY root_id ORDER BY id ASC) AS rn").
		Where("root_id IN ?", rootIDs)

	replies := make([]*model.Comment, 0)
	err := s.db.WithContext(ctx).
		Preload("User", preloadAuthor).
		Where("id IN (?)", s.db.WithContext(ctx).
			Table("(?) AS ranked", ranked).
			Select("id").
			Where("rn <= ?", maxRepliesPerRoot)).
		Order("id ASC").
		Find(&replies).Error
	if err != nil {
		return nil, fmt.Errorf("list replies of roots %v: %w", rootIDs, err)
	}

	// 被回复者：把回复的 ParentID 收集起来一次查回，顺手 Preload 作者。
	// 父评论可能就是根评论本身（已在 roots 里），也一并查了，逻辑更简单。
	parentIDs := make([]uint, 0, len(replies))
	for _, reply := range replies {
		if reply.ParentID > 0 {
			parentIDs = append(parentIDs, reply.ParentID)
		}
	}
	replyTo := make(map[uint]user.Brief, len(parentIDs))
	if len(parentIDs) > 0 {
		parents := make([]*model.Comment, 0)
		err = s.db.WithContext(ctx).
			Preload("User", preloadAuthor).
			Select("id", "user_id").
			Where("id IN ?", parentIDs).
			Find(&parents).Error
		if err != nil {
			return nil, fmt.Errorf("load replied comments %v: %w", parentIDs, err)
		}
		for _, parent := range parents {
			// 父评论作者可能已注销：User 零值时不放进 map，
			// 前端拿不到 reply_to 就只显示内容，不会出现空白名字。
			if parent.User.ID == 0 {
				continue
			}
			replyTo[parent.ID] = user.NewBrief(&parent.User)
		}
	}

	// 每条根评论的真实回复数，哪怕本页只带了前 200 条，也要让前端知道总数。
	type rootCount struct {
		RootID uint
		Count  int64
	}
	counts := make([]rootCount, 0, len(rootIDs))
	err = s.db.WithContext(ctx).Model(&model.Comment{}).
		Select("root_id, COUNT(*) AS count").
		Where("root_id IN ?", rootIDs).
		Group("root_id").
		Scan(&counts).Error
	if err != nil {
		return nil, fmt.Errorf("count replies of roots %v: %w", rootIDs, err)
	}
	countByRoot := make(map[uint]int64, len(counts))
	for _, c := range counts {
		countByRoot[c.RootID] = c.Count
	}

	// 按根评论归组，同时保持 roots 的原始顺序（前端直接按这个顺序渲染）。
	replyByRoot := make(map[uint][]ReplyItem, len(rootIDs))
	for _, reply := range replies {
		item := ReplyItem{Comment: reply}
		if brief, ok := replyTo[reply.ParentID]; ok {
			item.ReplyTo = brief
		}
		replyByRoot[reply.RootID] = append(replyByRoot[reply.RootID], item)
	}

	threads := make([]Thread, 0, len(roots))
	for _, root := range roots {
		thread := Thread{
			Comment:    root,
			Replies:    replyByRoot[root.ID],
			ReplyTotal: countByRoot[root.ID],
		}
		// Replies 可能是 nil（没有回复），统一成空切片，前端不用判 null。
		if thread.Replies == nil {
			thread.Replies = []ReplyItem{}
		}
		threads = append(threads, thread)
	}
	return threads, nil
}

// Create 在指定文章下发表一条评论或回复，并在同一事务里写出领域事件。
// articleID 来自 URL 路径，userID 来自 JWT，parentID 与 content 来自请求体。
// parentID 为 0 是顶级评论，大于 0 表示回复那条评论（可以是回复的回复）。
// 返回新建的评论模型（不含作者），错误已转成 domain 哨兵值供 handler 映射。
func (s *CommentService) Create(ctx context.Context, articleID, userID, parentID uint, content string) (*model.Comment, error) {
	// 与文章 Create 同样的规则：去空白、判空、限长，都留在 service 层。
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("%w: content is required", domain.ErrInvalidInput)
	}
	// 上限按字节判：comments.content 是 text 列，容量就是 65535 字节。
	// 10000 字节约 3300 个汉字，足够一条评论，同时挡住贴长文的行为。
	// 这里不能按字符判，否则 10000 个汉字是 3 万字节，看似合规但仍可能撞列上限。
	if textutil.TooLongBytes(content, maxCommentContentBytes) {
		return nil, fmt.Errorf("%w: content too long", domain.ErrInvalidInput)
	}

	// 先查文章是否存在，目的是给客户端一个明确的 404。
	// 这一步不是并发安全的最终保证：查完到插入之间文章可能被删掉，
	// 真正的兜底是 comments.article_id 上的 RESTRICT 外键，见 03 章 6.2。
	var article model.Article
	if err := s.db.WithContext(ctx).First(&article, articleID).Error; err != nil {
		// gorm.ErrRecordNotFound 没有在 mapServiceError 里映射，
		// 直接抛出去会变成 500。这里在 service 边界转成项目统一的 domain.ErrNotFound。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("check article %d: %w", articleID, domain.ErrNotFound)
		}
		return nil, fmt.Errorf("check article %d: %w", articleID, err)
	}

	// 回复的父评论必须存在，而且必须属于同一篇文章：
	// 不校验的话，可以把评论挂到别的文章的评论下面，树就串了。
	rootID := uint(0)
	if parentID > 0 {
		var parent model.Comment
		err := s.db.WithContext(ctx).
			Select("id", "article_id", "root_id").
			First(&parent, parentID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("check parent comment %d: %w", parentID, domain.ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("check parent comment %d: %w", parentID, err)
		}
		if parent.ArticleID != articleID {
			return nil, fmt.Errorf("%w: parent comment belongs to another article", domain.ErrInvalidInput)
		}
		// 回复的回复统一挂回根评论：两层结构才能一次 IN 查完，
		// 也让「回复 @A」的展示位置始终在根评论下面，不会越缩越深。
		if parent.RootID != 0 {
			rootID = parent.RootID
		} else {
			rootID = parent.ID
		}
	}

	m := &model.Comment{
		ArticleID: articleID,
		UserID:    userID,
		Content:   content,
		ParentID:  parentID,
		RootID:    rootID,
	}

	// 评论和事件必须一起提交：事务回滚时事件也不会留下，
	// 否则会出现「通知说有人评论了，但评论根本不存在」。
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(m).Error; err != nil {
			return fmt.Errorf("create comment for article %d: %w", articleID, err)
		}
		// m.ID 由 tx.Create(m) 回填，必须在 INSERT 之后才能生成事件。
		//
		// 顶级评论的 Target 是文章，通知发给文章作者；
		// 回复的 Target 是被回复的那条评论，通知改发给被回复的人（见 notify_resolver）。
		// Created 始终是这条新评论，通知点进去要定位到它而不是父评论。
		target := event.Target{Type: event.TargetArticle, ID: articleID}
		if parentID > 0 {
			target = event.Target{Type: event.TargetComment, ID: parentID}
		}
		created := event.Target{Type: event.TargetComment, ID: m.ID}
		return event.EmitCreated(ctx, tx, event.TypeCommentCreated, userID, target, &created)
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// Delete 只允许评论作者删除自己那条评论。
//
// 删根评论会连它下面的回复一起删：留着会变成点不进父级的孤儿回复，
// 列表里也找不到归属。删回复只删那一条，别的回复不受影响。
func (s *CommentService) Delete(ctx context.Context, id, userID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 所有权直接放进 WHERE：数据库一次判断，避免「先查再删」之间的竞态。
		res := tx.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Comment{})
		if res.Error != nil {
			return fmt.Errorf("delete comment %d: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			// 不存在与无权删除对外合并成 404，不泄露这条评论是否属于别人。
			return domain.ErrNotFound
		}

		// 上面删除的可能是根评论，也可能是回复。按 root_id 删只会命中「以它为根」的回复，
		// 对回复本身是空操作（没有谁的 root_id 指向一条回复），所以不需要额外判断层级。
		if err := tx.Where("root_id = ?", id).Delete(&model.Comment{}).Error; err != nil {
			return fmt.Errorf("delete replies of comment %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}
