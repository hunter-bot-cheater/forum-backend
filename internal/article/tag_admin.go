// 本文件是标签的管理侧操作：带 id 的分页列表、改名、合并。
// 与 tag.go 分开是因为读者不同：tag.go 服务正常的发帖与展示链路，
// 这里只有管理员会用到，而且要考虑「改一个词会影响成千上万篇文章的缓存」。
package article

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"forum-backend/internal/domain"
	"forum-backend/internal/metrics"
	"forum-backend/internal/model"
	"forum-backend/internal/paging"
)

// tagCacheInvalidateBatch 是批量删详情缓存时每批塞进 pipeline 的 key 数。
//
// 一个用了两年的标签可能挂在几千篇文章上，一条命令带几千个 key 会让
// 单个请求体大到 Redis 都要分几次读。500 是「一次往返处理足够多」与
// 「单个包不至于过大」之间的折中，批数再多也只是几毫秒的事。
const tagCacheInvalidateBatch = 500

// TagAdminItem 是管理端标签列表里的一条。
//
// 与公开的 TagCount 的区别是带 id：改名与合并都要按 id 定位，
// 名字在操作过程中正好是要被改掉的东西，不能当标识用。
type TagAdminItem struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// tagAdminListResp 是管理端标签列表的响应体。
// 单独定义而不是复用 tagListResp：那个只有 list，没有分页字段。
type tagAdminListResp struct {
	List     []TagAdminItem `json:"list"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

// TagRenameResult 是一次改名/合并的结果。
//
// Merged 告诉调用方「这次不只是改了个词，还把两个标签并成了一个」：
// 这两件事的后果差别很大，管理员需要知道自己刚才是不是动了别人的文章分类。
type TagRenameResult struct {
	TagID         uint   `json:"tag_id"`
	Name          string `json:"name"`
	Merged        bool   `json:"merged"`
	MovedArticles int64  `json:"moved_articles"`
}

// ListTagsForAdmin 分页列出全部标签，带 id 与文章数，供管理端定位要改的标签。
//
// keyword 按名字模糊匹配。没有文章的标签也会列出来（计数为 0）：
// 那正是最需要清理的一批，看不见就没法处理。
func (s *ArticleService) ListTagsForAdmin(ctx context.Context, keyword string, page, pageSize int) ([]TagAdminItem, int64, error) {
	q, err := paging.New(page, pageSize)
	if err != nil {
		return nil, 0, err
	}

	// 条件写在闭包里，Count 与分组查询共用一份，避免「总数按关键词过滤、
	// 列表却返回全部」这种只在第二页才发现的偏差。
	scope := func(tx *gorm.DB) *gorm.DB {
		keyword = strings.TrimSpace(keyword)
		if keyword != "" {
			tx = tx.Where("name LIKE ?", likePattern(keyword))
		}
		return tx
	}

	var total int64
	if err := scope(s.db.WithContext(ctx).Model(&model.Tag{})).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count tags: %w", err)
	}

	// LEFT JOIN 而不是 JOIN：没有关联行的标签计数是 0，但仍要出现在列表里。
	// 计数走 article_tags 的主键前缀 (article_id, tag_id) 反向扫不到，
	// 所以这里让 MySQL 按 tag_id 做一次索引查找；标签总量只有几百上千行，够用。
	items := make([]TagAdminItem, 0, q.PageSize)
	err = scope(s.db.WithContext(ctx).Model(&model.Tag{})).
		Select("tags.id AS id, tags.name AS name, COUNT(article_tags.article_id) AS count").
		Joins("LEFT JOIN article_tags ON article_tags.tag_id = tags.id").
		Group("tags.id, tags.name").
		Order("count DESC, tags.id ASC").
		Limit(q.PageSize).
		Offset(q.Offset()).
		Scan(&items).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list tags: %w", err)
	}
	return items, total, nil
}

// RenameTag 把标签 tagID 改名为 name；若 name 已被另一个标签占用，则合并两者。
//
// 合并是改名的必然分支，不是一个附加功能：管理员想把「golang」并到「Go」，
// 他要做的就是「把 golang 改名成 Go」，此时「Go」已经存在。
// 如果这里报「名字已存在」，他就得先把 Go 删掉（外键还不让删），操作根本无法完成。
func (s *ArticleService) RenameTag(ctx context.Context, tagID uint, name string) (TagRenameResult, error) {
	// 复用发帖时的清洗规则：不 trim 的话「 Go」会变成一个看起来一样、
	// 实际独立的新标签；长度上限也必须与 tags.name 的 varchar(32) 一致。
	names, err := normalizeTags([]string{name})
	if err != nil {
		return TagRenameResult{}, err
	}
	if len(names) == 0 {
		return TagRenameResult{}, fmt.Errorf("%w: tag name is empty", domain.ErrInvalidInput)
	}
	name = names[0]

	var result TagRenameResult
	// 受影响文章在事务里先取出来：合并会把它们的 tag_id 全改掉，
	// 事后再按源标签去查，一篇都查不到，缓存就永远清不掉。
	var affected []uint

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var source model.Tag
		// 加的是行锁（SELECT ... FOR UPDATE）而不是普通快照读，解决两件事：
		//  1. 两个管理员同时操作同一个标签时，后到的那个会等前一个提交，
		//     再读到「已经不存在」而返回 404，不会出现「接口回成功、数据没动」。
		//  2. 改名的同时有人发文章挂这个标签：插入 article_tags 的外键检查要
		//     拿 tags 行的共享锁，会排在我们后面，最后 DELETE tags 才不会撞 RESTRICT。
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", tagID).First(&source).Error; err != nil {
			// 查不到时换成 domain 哨兵：handler 的错误映射只认这一套，
			// 把 gorm 的错误原样往上抛会让「标签不存在」变成 500。
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("tag %d: %w", tagID, domain.ErrNotFound)
			}
			return fmt.Errorf("load tag %d: %w", tagID, err)
		}

		// 名字一字不差：什么都不会变，直接返回。
		// 幂等很重要——管理员连点两次按钮不该让第二次变成报错。
		// 放在查关联之前，是为了让「什么都没改」真的什么都不做：
		// 不查文章、不清缓存，重复点击不会把整批详情缓存白删一遍。
		if source.Name == name {
			result = TagRenameResult{TagID: source.ID, Name: source.Name}
			return nil
		}

		// 受影响文章在事务里先取出来：合并会把它们的 tag_id 全改掉，
		// 事后再按源标签去查，一篇都查不到，缓存就永远清不掉。
		if err := tx.Model(&model.ArticleTag{}).
			Where("tag_id = ?", tagID).
			Pluck("article_id", &affected).Error; err != nil {
			return fmt.Errorf("load articles of tag %d: %w", tagID, err)
		}

		// 按名字找目标。注意库里的排序规则不区分大小写，
		// 所以「Go」改成「go」查到的就是自己，下面走的是纯改名分支。
		var target model.Tag
		findErr := tx.Where("name = ?", name).First(&target).Error
		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			// 没人用过这个名字，就是普通改名。
			if err := tx.Model(&model.Tag{}).Where("id = ?", tagID).Update("name", name).Error; err != nil {
				return fmt.Errorf("rename tag %d to %q: %w", tagID, name, err)
			}
			result = TagRenameResult{TagID: source.ID, Name: name, MovedArticles: int64(len(affected))}
			return nil
		case findErr != nil:
			return fmt.Errorf("resolve tag %q: %w", name, findErr)
		case target.ID == source.ID:
			// 只是大小写或重音写法变了，同一行改个名即可。
			if err := tx.Model(&model.Tag{}).Where("id = ?", tagID).Update("name", name).Error; err != nil {
				return fmt.Errorf("rename tag %d to %q: %w", tagID, name, err)
			}
			result = TagRenameResult{TagID: source.ID, Name: name, MovedArticles: int64(len(affected))}
			return nil
		}

		// 合并：目标标签已经存在，把源标签的所有关联改挂到目标上，再删掉源标签。
		if err := mergeTagInto(tx, source.ID, target.ID); err != nil {
			return err
		}
		result = TagRenameResult{
			TagID:         target.ID,
			Name:          target.Name,
			Merged:        true,
			MovedArticles: int64(len(affected)),
		}
		return nil
	})
	if err != nil {
		return TagRenameResult{}, err
	}

	// 缓存清理放在事务外并且不参与错误返回：标签已经在库里改好了，
	// 让一次 Redis 抖动把接口变成 500，管理员会以为改失败了又点一次。
	// 最坏情况是详情页最多看到十分钟旧标签，等着 TTL 过期。
	s.invalidateDetailCaches(ctx, affected)
	return result, nil
}

// mergeTagInto 把 source 的全部文章关联改挂到 target，然后删掉 source 这一行。
//
// 调用方负责提供事务：这三步必须一起成立，中间断开就会出现
// 「两篇文章同一标签各留一行」或者「关联指向已删除的标签」。
func mergeTagInto(tx *gorm.DB, sourceID, targetID uint) error {
	// 先处理同时挂着两个标签的文章：那些行的 tag_id 从 source 改成 target 时
	// 会撞 (article_id, tag_id) 复合主键，事务直接失败。
	// 这类文章本来就已经有 target 了，删掉 source 那行正是合并想要的结果。
	sub := tx.Model(&model.ArticleTag{}).Select("article_id").Where("tag_id = ?", targetID)
	var duplicated []uint
	if err := tx.Model(&model.ArticleTag{}).
		Where("tag_id = ? AND article_id IN (?)", sourceID, sub).
		Pluck("article_id", &duplicated).Error; err != nil {
		return fmt.Errorf("find articles holding both tags %d and %d: %w", sourceID, targetID, err)
	}
	if len(duplicated) > 0 {
		if err := tx.Where("tag_id = ? AND article_id IN ?", sourceID, duplicated).
			Delete(&model.ArticleTag{}).Error; err != nil {
			return fmt.Errorf("drop duplicated links of tag %d: %w", sourceID, err)
		}
	}

	// 剩下的行整体改指向。用一条 UPDATE 而不是逐行读改写：
	// 这篇标签下的文章可能有几千篇，逐行会把一次管理操作拖成几十秒。
	if err := tx.Model(&model.ArticleTag{}).
		Where("tag_id = ?", sourceID).
		Update("tag_id", targetID).Error; err != nil {
		return fmt.Errorf("move articles from tag %d to %d: %w", sourceID, targetID, err)
	}

	// 走到这里 source 已经没有任何关联行，外键的 RESTRICT 不会再拦。
	if err := tx.Where("id = ?", sourceID).Delete(&model.Tag{}).Error; err != nil {
		return fmt.Errorf("delete merged tag %d: %w", sourceID, err)
	}
	return nil
}

// invalidateDetailCaches 批量删除一批文章的详情缓存。
//
// 用 pipeline 而不是逐条 Del：一次改名可能牵动几千篇文章，逐条往返要几秒钟，
// 而管理员点完按钮就盯着页面等。用 UNLINK 而不是 DEL：DEL 是在主线程上
// 同步释放内存，几千个 key 会把 Redis 卡住一小会儿，影响的是全站所有请求；
// UNLINK 只把 key 摘掉，内存交给后台线程回收。
//
// 某一批失败就停下并记日志：继续往后删没有意义（Redis 已经不健康了），
// 剩下的旧值由 TTL 兜底，最长十分钟。
func (s *ArticleService) invalidateDetailCaches(ctx context.Context, ids []uint) {
	for start := 0; start < len(ids); start += tagCacheInvalidateBatch {
		end := start + tagCacheInvalidateBatch
		if end > len(ids) {
			end = len(ids)
		}

		pipe := s.rdb.Pipeline()
		for _, id := range ids[start:end] {
			pipe.Unlink(ctx, articleDetailKey(id))
		}
		if _, err := pipe.Exec(ctx); err != nil {
			metrics.ArticleCacheEvents.WithLabelValues(cacheEventDegrade).Inc()
			slog.ErrorContext(ctx, "invalidate article caches after tag change",
				"done", start, "total", len(ids), "error", err)
			return
		}
	}
}
