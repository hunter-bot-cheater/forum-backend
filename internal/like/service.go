package like

import (
	"context"
	"errors"
	"fmt"
	"forum-backend/internal/domain"
	"log"
	"strconv"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/article"
	"forum-backend/internal/event"
	"forum-backend/internal/keys"
	"forum-backend/internal/model"
)

type LikeService struct {
	db         *gorm.DB
	rdb        *redis.Client
	articleSvc *article.ArticleService
}

func NewLikeService(db *gorm.DB, rdb *redis.Client, articleSvc *article.ArticleService) *LikeService {
	return &LikeService{db: db, rdb: rdb, articleSvc: articleSvc}
}

type LikeResult struct {
	ArticleID uint  `json:"article_id"`
	Count     int64 `json:"like_count"`
	Changed   bool  `json:"changed"`
	Liked     bool  `json:"liked"`
}

func (s *LikeService) Like(ctx context.Context, articleID, userID uint) (LikeResult, error) {
	return s.apply(ctx, articleID, userID, true)
}

func (s *LikeService) Unlike(ctx context.Context, articleID, userID uint) (LikeResult, error) {
	return s.apply(ctx, articleID, userID, false)
}

// apply 是点赞与取消共用的流程：确认文章存在、写数据库、同步投影、数一遍。
// add 决定这次是插入还是删除，也直接决定响应里的 Liked。
func (s *LikeService) apply(ctx context.Context, articleID, userID uint, add bool) (LikeResult, error) {
	if err := s.articleExists(ctx, articleID); err != nil {
		return LikeResult{}, err
	}

	changed, err := s.writeRelation(ctx, articleID, userID, add)
	if err != nil {
		return LikeResult{}, err
	}
	if changed {
		// 关系真的变了，详情缓存里的 like_count 就成了旧值。
		// 数据库已经提交，删失败只记日志，靠 TTL 兜底。
		s.articleSvc.InvalidateDetailCache(ctx, articleID)
	}

	s.syncLikeSet(ctx, articleID, userID, add)

	count, err := s.countLikes(ctx, articleID)
	if err != nil {
		return LikeResult{}, err
	}
	s.syncHotScore(ctx, articleID, count) //让 Redis 里的“热门文章排行榜（ZSET）”能实时跟上 MySQL 的数据变化，
	//同时保证哪怕 Redis 崩溃重启，数据也能自动恢复（自愈
	return LikeResult{
		ArticleID: articleID,
		Count:     count,
		Changed:   changed,
		Liked:     add,
	}, nil
}

// writeRelation 把点赞关系与领域事件放进同一个事务。
// 只有真正新增了一行、且这次是点赞，才写事件；
// 重复点赞与取消点赞都不通知作者。
func (s *LikeService) writeRelation(ctx context.Context, articleID, userID uint, add bool) (bool, error) {
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if add {
			err := tx.Create(&model.ArticleLike{ArticleID: articleID, UserID: userID}).Error
			if err == nil {
				changed = true
			} else if errors.Is(err, gorm.ErrDuplicatedKey) {
				return nil //说明唯一索引拦住了这一次操作 重复点赞了
			} else {
				return fmt.Errorf("insert article like: %w", err)
			}
		} else {
			res := tx.Where("article_id = ? AND user_id = ?", articleID, userID).
				Delete(&model.ArticleLike{})
			if res.Error != nil {
				return fmt.Errorf("delete article like: %w", res.Error)
			}
			changed = res.RowsAffected > 0
		}

		if !changed || !add { //重复点赞 或者 取消点赞 不发通知 直接结束
			return nil
		}
		// 点赞作用在文章上，Target 就是这篇文章。
		// Created 不填，通知跳文章即可，like id 没有消费方。
		target := event.Target{Type: event.TargetArticle, ID: articleID}
		return event.EmitCreated(ctx, tx, event.TypeLikeCreated, userID, target, nil)
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

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
		log.Printf("sync like set %s: %v", key, err)
	}
}

func (s *LikeService) countLikes(ctx context.Context, articleID uint) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Where("article_id = ?", articleID).Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count article likes %d: %w", articleID, err)
	}
	return count, nil
}

func (s *LikeService) HasLiked(ctx context.Context, articleID, userID uint) (bool, error) {
	key := keys.LikeSetKey(articleID)
	member := strconv.FormatUint(uint64(userID), 10)
	hit, err := s.rdb.SIsMember(ctx, key, member).Result()
	if err == nil && hit {
		return true, nil
	}
	if err != nil {
		log.Printf("check like set %s: %v", key, err)
	}

	var count int64
	if err := s.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Where("article_id = ? AND user_id = ?", articleID, userID).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check article like: %w", err)
	}
	if count > 0 {
		if err := s.rdb.SAdd(ctx, key, member).Err(); err != nil {
			log.Printf("repair like set %s: %v", key, err)
		}
	}
	return count > 0, nil
}

// syncHotScore 把热度写成分数绝对值。
// count 来自 MySQL，重复调用、Redis 清空、之前算错的分都会在下一次写入时被纠正。
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
		log.Printf("sync hot score %s: %v", member, err)
	}
}
