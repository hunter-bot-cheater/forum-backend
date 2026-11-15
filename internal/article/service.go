// Package article 文章模块：发布、分页列表、详情（带缓存）、删除。
// 这一层是业务逻辑，只认识 model 和 domain 错误，不认识 gin 和 HTTP 状态码。
// 详情接口的缓存策略是本模块最复杂的部分：读缓存 → 合并并发回源 → 回填 → 失败降级，
// 相关常量与计数器在 cache.go。
package article

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/keys"
	"forum-backend/internal/model"
)

// 固定排序：排序片段写成常量，禁止把 c.Query("sort") 拼进 Order()。
// 用户输入直接进 SQL 就是注入口，排序字段尤其容易漏，因为它的值没法用占位符代替。
const articleListOrder = "created_at DESC, id DESC"

// articleListColumns 是列表查询要取的列：不取 content 全文，只截开头当摘要。
// 原因是一页 10 篇、单篇正文上限 50000 字节，取全文等于每次往内存搬几百 KB，
// 而列表只需要一小段开头。LEFT() 在 MySQL 里按字符计数，中文不会被截成半个。
// 上限取 dto.go 的 articleExcerptSourceLimit，两边共用一个数字，改一处就够。
var articleListColumns = fmt.Sprintf(
	"id, user_id, title, LEFT(content, %d) AS excerpt, created_at",
	articleExcerptSourceLimit,
)

// ArticleService 文章业务逻辑。
//
// 三个依赖各有分工：
//   - db：MySQL，真正的数据源
//   - rdb：Redis，只放详情缓存，挂了也要能降级跑
//   - group：把「同一个 key 的并发回源」合并成一次，只在当前进程内生效
//
// metrics 含 atomic 字段，所以 ArticleService 必须始终用指针传递，
// 拷贝值会让计数器各记各的。
type ArticleService struct {
	db      *gorm.DB
	rdb     *redis.Client
	group   singleflight.Group
	metrics cacheMetrics
}

// NewArticleService 由 router 在装配期调用一次。group 和 metrics 是零值可用类型，不用初始化。
func NewArticleService(db *gorm.DB, rdb *redis.Client) *ArticleService {
	return &ArticleService{db: db, rdb: rdb}
}

// Create 发布一篇文章。作者身份由中间件确认后传入，客户端无法指定。
// 返回的错误只有两种：domain.ErrInvalidInput（标题或正文不合法）、以及包装过的数据库错误。
func (s *ArticleService) Create(ctx context.Context, userID uint, title, content string) (*model.Article, error) {
	// 先去掉首尾空白，否则一串空格也能通过「非空」校验。
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)

	if title == "" || content == "" {
		return nil, fmt.Errorf("%w: title and content are required", domain.ErrInvalidInput)
	}
	// 上限按字节算（Go 的 len 对字符串取字节数，中文一个字占 3 字节）。
	// 正文 50000 字节够写长文，同时挡住「把整个文件贴进来」这类请求。
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

	// id 是写库后由 MySQL 自增生成的，理论上这个 id 不会有旧缓存。
	// 仍然删一次，是因为 MySQL 重启后自增计数按表内最大 id 重建，
	// 删掉过最大 id 的情况下新行可能复用到旧 id；一次 DEL 的成本远低于读到脏缓存的代价。
	s.InvalidateDetailCache(ctx, a.ID)
	return a, nil
}

// List 返回一页文章和总数，page 从 1 开始。
//
// 这里做了两次查询：先 Count 总数（前端要算总页数），再按 Offset 取当前页。
// 数据量大时 Count 会成为瓶颈，那时该换成游标分页或用缓存存总数，现在不值得。
//
// 取回的 article.Content 是空的（列表查询不取全文），正文开头被塞进 Excerpt。
// 想拿全文必须走 Detail，别在列表结果上读 Content。
func (s *ArticleService) List(ctx context.Context, page, pageSize int) ([]*model.Article, int64, error) {
	// handler 层已经用 httputil.ParsePage 校验过一遍，这里再校验是为了保住 service 的边界：
	// 别的地方（比如以后的后台任务、命令行工具）直接调 service 时也不会传进非法值。
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

	// 总数为 0 时直接返回空切片，省掉一次没有意义的查询。
	// 注意返回的是空切片而不是 nil：nil 会被 JSON 序列化成 null，
	// 前端拿到的 data.list 就不是数组了，遍历会报错。
	if total == 0 {
		return []*model.Article{}, 0, nil
	}

	articles := make([]*model.Article, 0)
	// Preload("User") 会额外发一条 SELECT 把作者信息一起查出来，
	// 否则序列化时 a.User 是空结构体，列表里会看不到作者名字。
	// Select 只取列表需要的列，正文只截开头（见 articleListColumns）。
	// Offset 跳过前面几页，Limit 取当前页大小；排序由常量固定，不接收用户输入。
	err := s.db.WithContext(ctx).
		Select(articleListColumns).
		Preload("User").
		Order(articleListOrder).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&articles).Error
	if err != nil {
		return nil, total, fmt.Errorf("list articles page=%d size=%d: %w", page, pageSize, err)
	}
	return articles, total, nil
}

// Delete 删除文章，只允许作者本人操作，并连带清掉该文章的评论与点赞关系。
//
// 三件事必须一起成功：清评论、清点赞关系、删文章本身。
// 所以整体放在一个事务里，任何一步失败都回滚，不会留下「文章没了但评论还在」的半成品。
func (s *ArticleService) Delete(ctx context.Context, id, userID uint) error {
	// Transaction 的回调里必须用参数 tx 发 SQL，不能再用 s.db：
	// 用 s.db 就跑到事务外面去了，回滚时那部分不会撤销。
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先清子表。comments.article_id 上是 RESTRICT 外键，还有子记录时删文章会被数据库直接拒绝。
		if err := tx.Where("article_id = ?", id).Delete(&model.Comment{}).Error; err != nil {
			return err
		}
		// 点赞关系同样是 RESTRICT 外键，顺序不能反。
		if err := tx.Where("article_id = ?", id).Delete(&model.ArticleLike{}).Error; err != nil {
			return err
		}

		// 把所有权也放进 WHERE，让数据库一次判断「是不是作者」并删除，
		// 避免「先查是不是作者、再删」这两步之间文章被别人改动的竞态。
		res := tx.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Article{})
		if res.Error != nil {
			return res.Error
		}
		// 影响行数为 0 有两种可能：文章不存在，或者存在但不是这个人的。
		// 对外合并成同一个 404，避免泄露「这篇文章属于谁」。
		// 此刻评论和点赞已经被删了，返回错误会让整个事务回滚，不会留下脏数据。
		if res.RowsAffected == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete article %d: %w", id, err)
	}

	// 缓存必须在事务提交成功之后再删。写在事务里的话，一旦回滚，
	// 数据库里文章还在，缓存却被删了（虽然只是多一次回源，但逻辑上是错的）。
	s.InvalidateDetailCache(ctx, id)

	// 还要清掉 like 与 hot 模块维护的两个 Redis 投影，否则点赞数和热榜里会留下幽灵条目。
	// 这两个 key 由别的模块拥有，拼法统一收在 keys 包，这里只引用不自己拼。
	// 清理失败只记日志：文章已经删了，不能让缓存清理失败把接口变成 500。
	if err := s.rdb.Del(ctx, keys.LikeSetKey(id)).Err(); err != nil {
		log.Printf("drop like set for article %d: %v", id, err)
	}
	if err := s.rdb.ZRem(ctx, keys.HotArticlesKey, strconv.FormatUint(uint64(id), 10)).Err(); err != nil {
		log.Printf("drop hot member for article %d: %v", id, err)
	}
	return nil
}

// Detail 读一篇文章详情，缓存优先。
//
// 完整路径分五步：
//  1. 先无锁读一次缓存，命中就返回（绝大多数请求走到这里）
//  2. 未命中才进 singleflight 排队，同一个 id 只放一个请求去查库
//  3. 进门后二次读缓存，防止等排队期间别人已经回填好了
//  4. 查库成功后回填缓存；查到「确实不存在」写一个短 TTL 的空标记防穿透
//  5. 结果广播给所有等待者
func (s *ArticleService) Detail(ctx context.Context, id uint) (*model.Article, error) {
	key := articleDetailKey(id)

	// 第一遍读缓存：把整个 Detail 都塞进 Do 的话，命中缓存的请求也要排队等锁，反而更慢。
	a, handled, err := s.readArticleCache(ctx, key)
	if err != nil {
		return nil, err
	}
	if handled {
		return a, nil
	}

	// 未命中才进合并。第一个请求执行闭包，其余同 key 的请求阻塞等它的结果，
	// 拿到的是同一份返回值，不会各自打一次数据库。
	v, err, shared := s.group.Do(key, func() (any, error) {
		// 为什么要脱离调用方的 ctx：闭包捕获的是第一个请求者的 ctx，
		// 它一旦取消（用户关页面、客户端超时），所有等待者都会一起收到 context.Canceled。
		// WithoutCancel 去掉取消信号但保留 ctx 里的值，再套 3 秒超时，
		// 防止这次回源无限期跑下去。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()

		// 双重检查：等待者被唤醒时，第一个请求可能已经把结果写进缓存了。
		// 少了这一遍，稍晚到达的请求会各自再回源一次，合并就白做了。
		cached, handled, cacheErr := s.readArticleCache(loadCtx, key)
		if cacheErr != nil {
			return nil, cacheErr
		}
		if handled {
			return cached, nil
		}

		// 到这里才真正查库。
		s.metrics.Loads.Add(1)
		loaded, err := s.queryDetail(loadCtx, id)
		if err != nil {
			// 只有「确实不存在」才能写空标记。数据库故障绝不能写成空标记，
			// 否则故障期间查过的 id 会在恢复后继续错 60 秒（把故障固化成了「没有这篇」）。
			if errors.Is(err, domain.ErrNotFound) {
				s.markArticleAbsent(loadCtx, key)
			}
			return nil, err
		}

		// 回填失败只记日志，不能把这次成功的数据库查询变成接口失败。
		s.writeArticleCache(loadCtx, key, loaded)
		return loaded, nil
	})

	if shared {
		// shared 只表示结果被多个请求领取，不代表成功，所以只是计数，不影响返回。
		s.metrics.Shares.Add(1)
	}
	if err != nil {
		return nil, err
	}

	// 闭包返回的是 any。这里用带检查的类型断言，
	// 万一以后改了闭包的返回类型，会得到明确错误而不是 panic。
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
		// redis.Nil 是「key 不存在」的正常未命中，不是故障，别当错误往上抛。
		s.metrics.Misses.Add(1)
		return nil, false, nil
	}
	if err != nil {
		// 断连、超时等：降级回源，同时留下日志与计数证据，别静默吃掉。
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
		// 坏缓存自愈：删掉它，否则每次请求都会重复读到坏值、重复报错。
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
		// model.Article 全是普通字段，正常不会失败；仍要显式处理，别吞掉。
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
// 调用前必须已经用 errors.Is(err, domain.ErrNotFound) 确认过原因，
// 否则会把数据库故障误记成「这篇文章不存在」。
func (s *ArticleService) markArticleAbsent(ctx context.Context, key string) {
	if err := s.rdb.Set(ctx, key, articleEmptySentinel, articleEmptyTTL).Err(); err != nil {
		s.metrics.Degrades.Add(1)
		log.Printf("write empty article cache %s: %v", key, err)
	}
}

// queryDetail 只负责查库，并把 GORM 的「没查到」翻成项目内部的 domain.ErrNotFound。
// 在这一层转换，handler 的错误映射就不用认识 gorm.ErrRecordNotFound，
// 依赖方向保持 service 往外、handler 只认 domain 错误。
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
	// 这里从 article_likes 现算一次，装进内存字段随文章一起写进缓存，
	// 后续命中缓存的请求就不用再查了。
	var likeCount int64
	if err := s.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Where("article_id = ?", id).Count(&likeCount).Error; err != nil {
		return nil, fmt.Errorf("count article likes %d: %w", id, err)
	}
	a.LikeCount = likeCount
	return &a, nil
}

// InvalidateDetailCache 删除详情缓存，必须在数据库写入或事务提交成功之后调用。
//
// 导出是因为改动详情内容的写路径不止本文件：LikeService 点赞与取消成功后也要调用它。
// 空标记和真实文章用的是同一个 key，所以这一句同时也能清掉空标记。
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
