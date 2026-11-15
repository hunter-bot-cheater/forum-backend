package article

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"forum-backend/internal/domain"
	"log"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/keys"
	"forum-backend/internal/model"
)

// 固定排序：排序片段写成常量，禁止把 c.Query("sort") 拼进 Order()。
// 用户输入直接进 SQL 就是注入口。
const articleListOrder = "created_at DESC, id DESC"

// ArticleService 文章业务逻辑。方法名统一用动词，receiver 已经说明类型。
type ArticleService struct {
	db      *gorm.DB
	rdb     *redis.Client
	group   singleflight.Group // 合并同一 key 的并发回源，只在当前进程内生效
	metrics cacheMetrics       // 缓存计数器；含 atomic 字段，service 必须一直用指针传递
}

func NewArticleService(db *gorm.DB, rdb *redis.Client) *ArticleService {
	return &ArticleService{db: db, rdb: rdb}
}

// Create 发布一篇文章。作者身份由中间件提供，客户端无法指定。
func (s *ArticleService) Create(ctx context.Context, userID uint, title, content string) (*model.Article, error) {
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)

	if title == "" || content == "" {
		return nil, fmt.Errorf("%w: title and content are required", domain.ErrInvalidInput)
	}
	if len(title) > 128 || len(content) > 50000 {
		return nil, fmt.Errorf("%w: title or content too long", domain.ErrInvalidInput)
	}
	a := &model.Article{
		UserID:  userID,
		Title:   title,
		Content: content,
	}
	if err := s.db.WithContext(ctx).Create(a).Error; err != nil {
		return nil, fmt.Errorf("create article for user %d: %w", userID, err)

	}
	//id 由 MySQL 自增生成，写库成功后删这个 key
	s.InvalidateDetailCache(ctx, a.ID)
	return a, nil
}

// List 返回分页文章列表与总数。page 从 1 开始。
func (s *ArticleService) List(ctx context.Context, page, pageSize int) ([]*model.Article, int64, error) {
	if page < 1 {
		return nil, 0, fmt.Errorf("%w: page must be >= 1", domain.ErrInvalidInput)

	}
	if pageSize < 1 || pageSize > 50 {
		return nil, 0, fmt.Errorf("%w: page_size must be between 1 and 50", domain.ErrInvalidInput)
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Article{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count articles: %w", err)
	}

	if total == 0 {
		return []*model.Article{}, 0, nil
	}

	articles := make([]*model.Article, 0)
	err := s.db.WithContext(ctx).Preload("User").Order(articleListOrder).Offset((page - 1) * pageSize).Limit(pageSize).Find(&articles).Error
	if err != nil {
		return nil, total, fmt.Errorf("list articles page=%d size=%d: %w", page, pageSize, err)
	}
	return articles, total, nil
}

// Delete 只允许文章作者删除，并在事务里清掉该文章的评论。
func (s *ArticleService) Delete(ctx context.Context, id, userID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { //闭包里所有使用 tx 发起的操作（tx.Delete 等），都在这个事务的管辖范围内
		// 先清子表。comments.article_id 上有 RESTRICT 外键，先删文章会被拒绝。
		if err := tx.Where("article_id = ?", id).Delete(&model.Comment{}).Error; err != nil {
			return err // 返回错误让整个事务回滚
		}
		// 点赞关系外键是 RESTRICT。
		// 必须在删文章之前清掉，否则数据库会拒绝删除。
		if err := tx.Where("article_id = ?", id).Delete(&model.ArticleLike{}).Error; err != nil {
			return err // 返回错误让整个事务回滚，评论也一起恢复
		}

		// 把所有权放进 WHERE，数据库一次判断，避免「先查再删」之间的竞态。
		res := tx.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Article{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 文章不存在与无权删除对外合并成同一个错误，避免泄露归属。
			// 此时评论已被删，返回错误会让事务把评论一并回滚。
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete article %d: %w", id, err)
	}
	//Delete 的事务成功返回 后再删缓存，避免事务回滚后缓存被误删。
	s.InvalidateDetailCache(ctx, id)

	// 4.6 步补：文章删掉后，点赞 Set 与热榜 ZSET 里的投影也要清掉。
	// 这两个 key 由 like / hot 模块拥有，拼法统一收在 keys 包，这里直接引用。
	if err := s.rdb.Del(ctx, keys.LikeSetKey(id)).Err(); err != nil {
		log.Printf("drop like set for article %d: %v", id, err)
	}
	if err := s.rdb.ZRem(ctx, keys.HotArticlesKey, strconv.FormatUint(uint64(id), 10)).Err(); err != nil {
		log.Printf("drop hot member for article %d: %v", id, err)
	}
	return nil
}

//先无锁查缓存，未命中去排队；排队时防雪崩取消，进门后二次确认；
// 查库成功顺手回填，查无此文写空防穿透；最终拿到结果，唤醒众生

// Detail 先读缓存，未命中时合并并发请求、回源 MySQL、回填 Redis。
func (s *ArticleService) Detail(ctx context.Context, id uint) (*model.Article, error) {
	key := articleDetailKey(id)

	// 第一遍读缓存：绝大多数请求在这一步就返回了，不会进 singleflight。
	// 把整个 Detail 都塞进 Do 会让命中缓存的请求也排队，反而变慢。
	a, handled, err := s.readArticleCache(ctx, key)
	if err != nil {
		return nil, err
	}
	if handled {
		return a, nil
	}

	// 未命中才进合并。第一个请求执行 fn，其余同 key 请求等它的结果。
	v, err, shared := s.group.Do(key, func() (any, error) {
		// 脱离调用方的取消信号，但保留 ctx 里的值。
		// fn 的闭包捕获的是第一个请求者的 ctx，它一旦被取消，
		// 所有等待者都会拿到同一个 context.Canceled
		// WithoutCancel 再套一个 3 秒超时，避免这次回源无限跑下去。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()

		// 双重检查：等待者被唤醒时，第一个请求可能已经把结果写进缓存了。
		// 不查这一遍，稍晚到达的请求会各自再回源一次，合并就白做了。
		cached, handled, cacheErr := s.readArticleCache(loadCtx, key)
		if cacheErr != nil {
			return nil, cacheErr
		}
		if handled {
			return cached, nil
		}

		// 到这里才真正查库。计数器 +1。
		s.metrics.Loads.Add(1)
		loaded, err := s.queryDetail(loadCtx, id)
		if err != nil {
			// 只有「确实不存在」能写空标记。数据库故障绝不能写成空标记，
			// 否则故障期间查过的 id 会在恢复后继续错 60 秒。
			if errors.Is(err, domain.ErrNotFound) {
				s.markArticleAbsent(loadCtx, key) //写入 articleEmptySentinel 哨兵值
			}
			return nil, err
		}

		// 回填失败只记日志，不能把这次成功的数据库查询变成接口失败。
		s.writeArticleCache(loadCtx, key, loaded)
		return loaded, nil
	})

	if shared {
		// shared 只表示结果被多个请求领取，不代表成功。
		s.metrics.Shares.Add(1)
	}
	if err != nil {
		return nil, err
	}

	// fn 只可能返回 *model.Article。用带检查的类型断言，
	// 万一以后改了 fn 的返回类型，这里给出明确错误而不是 panic。
	article, ok := v.(*model.Article)
	if !ok {
		return nil, fmt.Errorf("cache loader returned unexpected type %T", v)
	}
	return article, nil
}

// readArticleCache 读一次详情缓存，并判断这次请求还需不需要回源。
// handled 为 true 表示「已经有结论」：要么是文章，要么是确定的不存在。
// Redis 故障、key 不存在、缓存内容损坏都返回 handled=false，让调用方回源；
// 它们不该让接口直接失败，所以不计入 error。
func (s *ArticleService) readArticleCache(ctx context.Context, key string) (*model.Article, bool, error) {
	val, err := s.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		// 正常未命中，不是故障。
		s.metrics.Misses.Add(1)
		return nil, false, nil
	}
	if err != nil {
		// 断连、超时等：降级回源，同时留下日志与计数证据。
		s.metrics.Misses.Add(1)
		s.metrics.Degrades.Add(1)
		log.Printf("read article cache %s: %v", key, err)
		return nil, false, nil
	}

	// 先比空标记，再谈反序列化。顺序不能反：
	// 哨兵不是合法 JSON，交给 Unmarshal 只会得到一次没必要的解析错误。
	if val == articleEmptySentinel {
		s.metrics.EmptyHits.Add(1)
		return nil, true, domain.ErrNotFound
	}

	var a model.Article
	if err := json.Unmarshal([]byte(val), &a); err != nil {
		// 坏缓存自愈：删掉它，否则每次请求都会重复读到坏值。
		s.metrics.Degrades.Add(1)
		log.Printf("invalid article cache %s: %v", key, err)
		_ = s.rdb.Del(ctx, key).Err()
		return nil, false, nil
	}

	s.metrics.Hits.Add(1)
	return &a, true, nil
}

// writeArticleCache 把查到的文章写回缓存。
// 回填失败不能改变本次数据库查询的结果，所以没有返回值，只记日志和计数器。
func (s *ArticleService) writeArticleCache(ctx context.Context, key string, a *model.Article) {
	data, err := json.Marshal(a)
	if err != nil {
		// model.Article 全是普通字段，正常情况不会失败；仍要显式处理，别吞掉。
		log.Printf("marshal article cache %s: %v", key, err)
		return
	}
	// 每次调用 articleDetailTTL() 都会重新取随机数，这是防雪崩的关键。
	if err := s.rdb.Set(ctx, key, data, articleDetailTTL()).Err(); err != nil {
		s.metrics.Degrades.Add(1)
		log.Printf("write article cache %s: %v", key, err)
		return
	}
	s.metrics.Writes.Add(1)
}

// markArticleAbsent 写入「查无此文」的空标记，只给短 TTL。
// 调用前必须已经用 errors.Is(err, domain.ErrNotFound) 确认过原因。
func (s *ArticleService) markArticleAbsent(ctx context.Context, key string) {
	if err := s.rdb.Set(ctx, key, articleEmptySentinel, articleEmptyTTL).Err(); err != nil {
		s.metrics.Degrades.Add(1)
		log.Printf("write empty article cache %s: %v", key, err)
	}
}

// queryDetail 只负责查库，并把 GORM 的「没查到」翻成项目内部的 domain.ErrNotFound。
// 在这一层转换，handler 的错误映射就不用认识 gorm.ErrRecordNotFound，
// 依赖方向保持 service 向外、handler 只认 service 的错误。
func (s *ArticleService) queryDetail(ctx context.Context, id uint) (*model.Article, error) {
	var a model.Article
	err := s.db.WithContext(ctx).Preload("User").First(&a, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("article %d: %w", id, domain.ErrNotFound)
		}
		return nil, fmt.Errorf("query article %d: %w", id, err)
	}

	// 详情要显示点赞数。articles.like_count 列始终不维护，
	// 这里从 article_likes 现算一次，装进内存字段随文章一起写进缓存。
	var likeCount int64
	if err := s.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Where("article_id = ?", id).Count(&likeCount).Error; err != nil {
		return nil, fmt.Errorf("count article likes %d: %w", id, err)
	}
	a.LikeCount = likeCount
	return &a, nil
}

// InvalidateDetailCache 删除详情缓存。数据库写入或事务提交成功之后才调用。
// 导出是因为会改动详情内容的写路径不止 ArticleService 自己：
// LikeService 点赞与取消成功后也要调用它。
// 空标记和真实文章用的是同一个 key，所以这一句也能清掉空标记。
// 失败只记录，不改变已提交的数据库结果；TTL 是最后一道兜底。
func (s *ArticleService) InvalidateDetailCache(ctx context.Context, id uint) {
	key := articleDetailKey(id)
	if err := s.rdb.Del(ctx, key).Err(); err != nil {
		s.metrics.Degrades.Add(1)
		log.Printf("invalidate article cache %s: %v", key, err)
	}
}

// CacheStats 返回当前进程的缓存计数快照，供本地验收与将来的监控接入使用。
// 进程重启后计数归零，它不是持久化指标。
func (s *ArticleService) CacheStats() CacheStats {
	return s.metrics.snapshot()
}
