// Package like 点赞模块：点赞、取消点赞、查是否点过赞，并顺带把热榜分数同步过去。
//
// 一次点赞要动三处状态，它们的分工是：
//   - MySQL 的 article_likes 表：唯一真相，进程重启也不丢
//   - Redis 的 SET（keys.LikeSetKey）：记录「谁点过这篇」，查是否点过走它
//   - Redis 的 ZSET（keys.HotArticlesKey）：热榜榜单，分数就是点赞数
//
// 更新顺序固定为「先写数据库，再动缓存」：数据库失败要整体失败并把错误返回给客户端，
// 缓存失败只记日志，因为榜单和集合都能靠回源或重建补回来。
package like

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/article"
	"forum-backend/internal/domain"
	"forum-backend/internal/event"
	"forum-backend/internal/keys"
	"forum-backend/internal/model"
)

// LikeService 点赞业务逻辑。
// 持有 articleSvc 只为了调它的 InvalidateDetailCache：
// 点赞数一变，文章详情缓存里的 like_count 就是旧值，必须让它失效。
type LikeService struct {
	db         *gorm.DB
	rdb        *redis.Client
	articleSvc *article.ArticleService
}

// NewLikeService 由 router 在装配期调用一次，三个依赖都由外部创建好再传进来。
func NewLikeService(db *gorm.DB, rdb *redis.Client, articleSvc *article.ArticleService) *LikeService {
	return &LikeService{db: db, rdb: rdb, articleSvc: articleSvc}
}

// LikeResult 是一次点赞操作的结论，handler 直接把它转成对外 DTO。
//
// Changed 表示这次调用有没有真的改到数据：重复点赞、取消一个没点过的赞，都是 false。
// Liked 表示操作后的最终状态，客户端应该用它决定按钮要不要点亮；
// 只看 Changed 的话，重复点赞会得到「没变」，但按钮其实应该是亮的。
type LikeResult struct {
	ArticleID uint  `json:"article_id"`
	Count     int64 `json:"like_count"`
	Changed   bool  `json:"changed"`
	Liked     bool  `json:"liked"`
}

// Like 处理「点赞」，等价于 apply(add=true)。
func (s *LikeService) Like(ctx context.Context, articleID, userID uint) (LikeResult, error) {
	return s.apply(ctx, articleID, userID, true)
}

// Unlike 处理「取消点赞」，等价于 apply(add=false)。
// 取消一个本来就没点过的赞不会报错，返回 Changed=false，保持接口幂等。
func (s *LikeService) Unlike(ctx context.Context, articleID, userID uint) (LikeResult, error) {
	return s.apply(ctx, articleID, userID, false)
}

// apply 是点赞与取消共用的流程，add 决定这次是插入还是删除关系。
//
// 顺序不能随意调：
//  1. 先确认文章存在。不确认的话，点赞一条已删的文章会被外键拒绝，
//     客户端拿到的是 500，而不是应该的 404。
//  2. 写关系。这一步在事务里，失败就整体结束，后面的缓存不该动。
//  3. 关系真的变了才删详情缓存。没变还去删，等于白白制造一次缓存未命中。
//  4. 同步 Redis 的点赞集合与热榜分数。这两步失败只记日志，不影响接口成功。
//  5. 最后数一遍真实点赞数，作为本次响应的 like_count。
func (s *LikeService) apply(ctx context.Context, articleID, userID uint, add bool) (LikeResult, error) {
	if err := s.articleExists(ctx, articleID); err != nil {
		return LikeResult{}, err
	}

	changed, err := s.writeRelation(ctx, articleID, userID, add)
	if err != nil {
		return LikeResult{}, err
	}

	// 关系变了，详情缓存里的 like_count 就成了旧值。数据库已经提交，
	// 删缓存失败只记日志（InvalidateDetailCache 内部处理），靠 TTL 兜底。
	if changed {
		s.articleSvc.InvalidateDetailCache(ctx, articleID)
	}

	// 集合记录「谁点过」，用于 HasLiked 的快速路径。
	s.syncLikeSet(ctx, articleID, userID, add)

	count, err := s.countLikes(ctx, articleID)
	if err != nil {
		return LikeResult{}, err
	}

	// 用刚数出来的真实值覆盖热榜分数，让榜单能跟上 MySQL 的变化，
	// 也让 Redis 被清空后能靠下一次点赞自动补回来。
	s.syncHotScore(ctx, articleID, count)

	return LikeResult{
		ArticleID: articleID,
		Count:     count,
		Changed:   changed,
		Liked:     add,
	}, nil
}

// writeRelation 把点赞关系与领域事件放进同一个事务。
//
// 为什么要事务：点赞成功要给作者发通知，通知来自 outbox 表里的那条事件。
// 关系写进去但事件没写，作者永远收不到；事件写了但关系回滚，就会通知一条不存在的点赞。
//
// 只有「真的新增了一行」且这次是点赞才写事件：
// 重复点赞（唯一索引拦下）和取消点赞都不该打扰作者。
func (s *LikeService) writeRelation(ctx context.Context, articleID, userID uint, add bool) (bool, error) {
	changed := false

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if add {
			// 直接插入，重复点赞交给 (article_id, user_id) 的唯一索引拦。
			// 不先 SELECT 再 INSERT：两步之间有竞态，并发双击会同时查到「没点过」。
			// 三个分支用 switch 而不是 if-else 链：这里判断的是「err 有几种形态」，
			// 写成平铺的分支更清楚，也免得以后加一种错误时缩进越套越深。
			err := tx.Create(&model.ArticleLike{ArticleID: articleID, UserID: userID}).Error
			switch {
			case err == nil:
				changed = true
			case errors.Is(err, gorm.ErrDuplicatedKey):
				// 已经点过赞，不是错误。changed 保持 false，后面不会发通知。
				return nil
			default:
				return fmt.Errorf("insert article like: %w", err)
			}
		} else {
			// 取消点赞：删不到行说明本来就没点过，同样不算错误。
			res := tx.Where("article_id = ? AND user_id = ?", articleID, userID).
				Delete(&model.ArticleLike{})
			if res.Error != nil {
				return fmt.Errorf("delete article like: %w", res.Error)
			}
			changed = res.RowsAffected > 0
			// 取消点赞不发通知，删到没删到都一样，直接结束事务。
			return nil
		}

		// 走到这里说明这次真的新增了关系：重复点赞在上面已经提前 return 了。
		// 点赞作用在文章上，所以事件的目标是这篇文章。
		// Created 留空：点开通知跳到文章就够了，点赞记录本身的 id 没有消费方。
		target := event.Target{Type: event.TargetArticle, ID: articleID}
		return event.EmitCreated(ctx, tx, event.TypeLikeCreated, userID, target, nil)
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// articleExists 用一次 COUNT 判断文章在不在。
// 只查 id 是否存在，不把整篇文章读出来，也不走详情缓存：
// 这里要的是「能不能点赞」这个事实，不需要文章内容。
func (s *LikeService) articleExists(ctx context.Context, articleID uint) error {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.Article{}).
		Where("id = ?", articleID).Count(&count).Error
	if err != nil {
		return fmt.Errorf("check article %d: %w", articleID, err)
	}
	if count == 0 {
		return fmt.Errorf("article %d: %w", articleID, domain.ErrNotFound)
	}
	return nil
}

// syncLikeSet 把这次操作同步到 Redis 的点赞集合里。
// 失败只记日志：集合只是加速查询的投影，丢了 HasLiked 会回源 MySQL 再补回来。
func (s *LikeService) syncLikeSet(ctx context.Context, articleID, userID uint, add bool) {
	key := keys.LikeSetKey(articleID)
	member := strconv.FormatUint(uint64(userID), 10)

	var err error
	if add {
		err = s.rdb.SAdd(ctx, key, member).Err()
	} else {
		err = s.rdb.SRem(ctx, key, member).Err()
	}
	if err != nil {
		slog.WarnContext(ctx, "sync like set", "key", key, "error", err)
	}
}

// countLikes 数一遍文章当前的真实点赞数。
//
// 为什么不直接读 Redis 集合的 SCARD：集合是投影，可能因为 Redis 被清空、
// 同步失败而缺成员，用它当返回值和热榜分数会让错误一直扩散。
// 数 MySQL 是准的，代价是一条走了索引的 COUNT，点赞接口的频率下可以接受。
func (s *LikeService) countLikes(ctx context.Context, articleID uint) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Where("article_id = ?", articleID).Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count article likes %d: %w", articleID, err)
	}
	return count, nil
}

// HasLiked 判断某个用户有没有点过这篇，先查 Redis 集合。
//
// 集合命中就直接返回 true，这是绝大多数情况。
// 未命中时不能直接判定「没点过」：集合可能缺成员（Redis 重启、同步失败），
// 所以要回源 MySQL 确认，确认点过就顺手把成员补进集合。
//
// 已知边界：如果关系在 MySQL 里被删掉、但集合里的成员还在（没经过本模块的删除路径），
// 这里会误判为「点过」。正常业务只有 Unlike 会删关系，它会同步 SRem，所以不会出现。
func (s *LikeService) HasLiked(ctx context.Context, articleID, userID uint) (bool, error) {
	key := keys.LikeSetKey(articleID)
	member := strconv.FormatUint(uint64(userID), 10)

	hit, err := s.rdb.SIsMember(ctx, key, member).Result()
	if err == nil && hit {
		return true, nil
	}
	if err != nil {
		// Redis 故障不当成失败：往下走 MySQL，接口照常可用。
		slog.WarnContext(ctx, "check like set", "key", key, "error", err)
	}

	var count int64
	if err := s.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Where("article_id = ? AND user_id = ?", articleID, userID).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check article like: %w", err)
	}

	// 回填：确认点过就把成员补进集合，下次查询走快速路径。
	if count > 0 {
		if err := s.rdb.SAdd(ctx, key, member).Err(); err != nil {
			slog.WarnContext(ctx, "repair like set", "key", key, "error", err)
		}
	}
	return count > 0, nil
}

// syncHotScore 把热榜里这篇文章的分数覆盖成绝对值。
//
// count 来自 MySQL，所以重复调用、Redis 清空、之前算错的分都会在下一次写入时被纠正。
// count 为 0 时直接删掉这一个成员，不做整榜扫描。
//
// 写绝对值还顺带补住了 rebuild 的一个窄缝：重建从 SQL 读完到 RENAME 换榜之间，
// 落在这段间隙里的 ZINCRBY 会被新榜覆盖掉。这段间隙通常是毫秒级，
// 而它造成的偏差会在该文章下一次被点赞时，用 MySQL 的真实计数覆盖回来。
// 如果这里改成 ZINCRBY 增量累加，这条自我修正的路径就断了。
func (s *LikeService) syncHotScore(ctx context.Context, articleID uint, count int64) {
	member := strconv.FormatUint(uint64(articleID), 10)

	var err error
	if count > 0 {
		err = s.rdb.ZAdd(ctx, keys.HotArticlesKey, redis.Z{Score: float64(count), Member: member}).Err()
	} else {
		err = s.rdb.ZRem(ctx, keys.HotArticlesKey, member).Err()
	}
	if err != nil {
		slog.WarnContext(ctx, "sync hot score", "member", member, "error", err)
	}
}
