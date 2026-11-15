package hot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/keys"
	"forum-backend/internal/model"
)

// hotReadyKey 记的是「已经按 MySQL 全量建过一次」，不是缓存值。
// ZSET 一条数据都没有时 Redis 会把 key 删掉，
// 只看 ZSET 分不清「榜单真空」和「投影丢失」。
// 这个独立的 String key 由 rebuild 写入，写入侧的 syncHotScore 不碰它，
// 空榜时也照样存在，下一次读就不会再回源。
//
// 它必须带 TTL。永不过期意味着这个榜单会一直停留在第一次重建的那一刻，
// 之后新出现的爆款文章永远进不来，榜单变成「历史榜」。
// 过期即宣告投影失效，下一次读触发一次重建，代价是每小时多一次轻量 GROUP BY。
// TTL 只给 ready，不给 ZSET：ZSET 若先于 ready 消失，
// 中间会出现「key 没了但标记还在」的窗口，那一窗口里的榜单会静默变空。
const hotReadyKey = "hot:articles:v1:ready"

// hotReadyTTL 兼顾新鲜度与重建频率。太短则重建次数上去，
// 太长则榜单更新慢；一小时是论坛热度量级下比较合适的折中。
const hotReadyTTL = time.Hour

// hotTempKey 是重建时的落料区。重建期间所有 ZADD 写在这里，
// 写完之后用一条 RENAME 原子换掉正式 key。
// 直接在正式 key 上 Del 再 ZAdd，中间会有一段榜单为空的窗口，
// 并发读会看到空榜。
const hotTempKey = "hot:articles:v1:tmp"

// hotRebuildTop 是重建时从 MySQL 捞取的上限。
// 对外接口最多要 100 条，这里取 5 倍余量：
// 榜单之外的冷门文章被点赞时，syncHotScore 会把它补进 ZSET，
// 不必等下一轮重建才能上榜。
const hotRebuildTop = 500

// hotCooldownKey 是重建失败后的退避标记。
// 没有它，一次超过超时预算的重建会变成雪崩：
// 每个请求都发现 ready 缺失，每个都去尝试重建，每个都失败。
// 冷却期内直接走 MySQL 降级，让数据库喘口气。
const hotCooldownKey = "hot:articles:v1:cooldown"

// hotCooldownTTL 取 30 秒，比一次重建的超时预算（3 秒）大一个数量级，
// 足够把突发的重建尝试压下去，又不至于让临时的网络抖动挡住太久。
const hotCooldownTTL = 30 * time.Second

// HotArticle 是热榜里的一条，service 查好标题后返回给 handler。
// Score 与 LikeCount 现在存的是同一个数（点赞数），分开是给以后换热度算法留位置：
// 那时 Score 会是加权结果，LikeCount 仍是原始点赞数。
type HotArticle struct {
	ArticleID uint    `json:"article_id"`
	Score     float64 `json:"score"`
	Title     string  `json:"title"`
	LikeCount int64   `json:"like_count"`
}

// hotRow 是 MySQL 聚合查询的落点：一篇怎么排、得了多少分。
// 列名用 gorm tag 指定，因为 COUNT(*) 的别名是 SQL 里写的 score，不是结构体字段名推导出来的。
type hotRow struct {
	ArticleID uint  `gorm:"column:article_id"`
	Score     int64 `gorm:"column:score"`
}

// HotService 热榜业务逻辑。
type HotService struct {
	db  *gorm.DB
	rdb *redis.Client
	// group 把同一时刻发现「投影丢失」的请求合并成一次重建。
	// singleflight.Group 零值可用，不需要初始化。
	group singleflight.Group
}

// NewHotService 由 router 在装配期调用一次。
func NewHotService(db *gorm.DB, rdb *redis.Client) *HotService {
	return &HotService{db: db, rdb: rdb}
}

// checkTopN 校验条数范围，1 到 100。
// 上限 100 是给 Redis 的 ZREVRANGE 和后面的 IN 查询定的：
// 再大就会把一次接口请求变成几百行数据，也会让 loadByIDs 的 IN 列表变长。
func checkTopN(n int) error {
	if n < 1 || n > 100 {
		return fmt.Errorf("%w: limit must be between 1 and 100", domain.ErrInvalidInput)
	}
	return nil
}

// Top 返回热榜前 n 名，优先读 ZSET。
//
// 三条路径，按成本从低到高：
//  1. ready 在：一次 EXISTS + 一次 ZREVRANGE 就返回，线上绝大多数请求走这里
//  2. ready 不在：进 ensureHotZSet 重建一次，重建好再读
//  3. Redis 出错或重建失败：降级到 GROUP BY 直接查 MySQL，接口仍可用，只是慢
func (s *HotService) Top(ctx context.Context, n int) ([]HotArticle, error) {
	if err := checkTopN(n); err != nil {
		return nil, err
	}

	// ready 存在说明榜单已经按 MySQL 建过一次（哪怕建出来是 0 条）。
	// 有它就能确定「榜是空的」是真实情况，而不是投影丢了。
	ready, err := s.rdb.Exists(ctx, hotReadyKey).Result()
	if err != nil {
		// Redis 报错（断连、超时）直接降级，不让缓存故障变成接口故障。
		log.Printf("check hot ready key: %v", err)
		return s.TopFromMySQL(ctx, n)
	}

	if ready == 0 {
		// 刚启动、Redis 被清空、或者 ready 的 TTL 到期，都需要重建。
		// 冷却期内不再试，直接降级，避免一次失败的重建被反复触发。
		cool, err := s.rdb.Exists(ctx, hotCooldownKey).Result()
		if err == nil && cool > 0 {
			return s.TopFromMySQL(ctx, n)
		}
		if err := s.ensureHotZSet(ctx); err != nil {
			log.Printf("rebuild hot zset: %v", err)
			s.markCooldown(ctx)
			return s.TopFromMySQL(ctx, n)
		}
	}

	// ZREVRANGE 按分数从大到小取前 n 个，WITHSCORES 把分数一起带回来，
	// 省掉为每条再查一次 ZSCORE。
	pairs, err := s.rdb.ZRevRangeWithScores(ctx, keys.HotArticlesKey, 0, int64(n-1)).Result()
	if err != nil {
		log.Printf("read hot zset: %v", err)
		return s.TopFromMySQL(ctx, n)
	}

	// ZSET 的成员是字符串形式的文章 id，这里转回数字，
	// 顺便决定后面查文章标题时 IN 列表里放什么。
	rows := make([]hotRow, 0, len(pairs))
	for _, p := range pairs {
		id, err := strconv.ParseUint(p.Member.(string), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse hot member %v: %w", p.Member, err)
		}
		rows = append(rows, hotRow{ArticleID: uint(id), Score: int64(p.Score)})
	}
	return s.loadByIDs(ctx, rows)
}

// ensureHotZSet 把同一时刻发现「投影丢失」的请求合并成一次重建。
// 没有它，FLUSHDB 之后 100 个并发请求会各查一次 MySQL。
func (s *HotService) ensureHotZSet(ctx context.Context) error {
	_, err, _ := s.group.Do("hot:rebuild", func() (interface{}, error) {
		// 闭包用的是第一个请求的 ctx。它被取消会连累所有等待者，
		// 所以这里去掉取消信号，另给 3 秒超时防止无限期跑下去。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()

		// 双重检查：等待者醒来时，第一个请求可能已经把投影建好了。
		ready, err := s.rdb.Exists(loadCtx, hotReadyKey).Result()
		if err != nil {
			return nil, err
		}
		if ready > 0 {
			return nil, nil
		}
		return nil, s.rebuild(loadCtx)
	})
	return err
}

// TopFromMySQL 是降级路径：绕开 Redis，直接 GROUP BY 点赞表算前 n 名。
// 比走缓存慢，但 Redis 挂了接口也不该挂。
func (s *HotService) TopFromMySQL(ctx context.Context, n int) ([]HotArticle, error) {
	rows, err := s.groupTop(ctx, n)
	if err != nil {
		return nil, err
	}
	return s.loadByIDs(ctx, rows)
}

// markCooldown 在一次重建失败后打上退避标记。
// 用 WithoutCancel：触发它的那个请求可能已经超时断开，
// 但「别再重试」这个结论要留下来给后面的请求看。
func (s *HotService) markCooldown(ctx context.Context) {
	if err := s.rdb.Set(context.WithoutCancel(ctx), hotCooldownKey, "1", hotCooldownTTL).Err(); err != nil {
		log.Printf("mark hot cooldown: %v", err)
	}
}

// rebuild 从唯一事实来源全量重建 ZSET，最后打上已初始化标记。
// 只由 ensureHotZSet 调用，不对外暴露，也不再有脚本调用它。
//
// 三次改写留下的两条铁律：
//  1. SQL 必须带 LIMIT。不带的话，点赞表涨到千万级，
//     这条 GROUP BY 会扫穿整张表，MySQL 的 CPU 直接被打满。
//     聚合本身仍然要过一遍索引，LIMIT 省下的是结果集和后续的 ZADD 条数。
//  2. 新榜必须落在临时 key 上，写完 RENAME 换过去。
//     直接在正式 key 上 Del 再逐条 ZAdd，中途任何一次读都会拿到残缺的榜单，
//     而重建期间的点赞也会被后面的全量覆盖冲掉。
func (s *HotService) rebuild(ctx context.Context) error {
	var rows []hotRow
	if err := s.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Select("article_id, COUNT(*) AS score").
		Group("article_id").
		Order("score DESC, article_id ASC").
		Limit(hotRebuildTop).
		Scan(&rows).Error; err != nil {
		return fmt.Errorf("load all like counts: %w", err)
	}

	// 用事务管道把「清临时 key、写数据、RENAME、写 ready」打包成一次 EXEC。
	// 单条命令不是瓶颈，但要保证读侧看不到中间状态。
	pipe := s.rdb.TxPipeline()

	// 上一轮重建失败可能在临时 key 里留了半成品，先清掉。
	pipe.Del(ctx, hotTempKey)

	if len(rows) > 0 {
		for _, r := range rows {
			member := strconv.FormatUint(uint64(r.ArticleID), 10)
			pipe.ZAdd(ctx, hotTempKey, redis.Z{Score: float64(r.Score), Member: member})
		}
		// RENAME 在 MULTI 里执行，对读侧是原子的：
		// 读到的是换之前那份，或者换之后那份，不会读到空。
		pipe.Rename(ctx, hotTempKey, keys.HotArticlesKey)
	} else {
		// 一条点赞都没有时临时 key 不存在，RENAME 会报 key 不存在。
		// 空榜本来就该是空，直接删正式 key。
		pipe.Del(ctx, keys.HotArticlesKey)
	}

	// rows 为空也要写标记。否则 ZSET 依然不存在，
	// 空榜的每个请求都会重新走到这里。
	// TTL 让榜单每小时自然失效一次，避免变成历史快照。
	pipe.Set(ctx, hotReadyKey, "1", hotReadyTTL)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("rebuild hot zset: %w", err)
	}
	return nil
}

// groupTop 直接按点赞表统计前 n 名，是 Top 的降级实现。
// 与 rebuild 的区别只有 LIMIT 取 n 而不是 hotRebuildTop。
func (s *HotService) groupTop(ctx context.Context, n int) ([]hotRow, error) {
	var rows []hotRow
	err := s.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Select("article_id, COUNT(*) AS score").
		Group("article_id").
		Order("score DESC, article_id ASC").
		Limit(n).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("group top articles: %w", err)
	}
	return rows, nil
}

// loadByIDs 按 id 批量补标题，返回保持传入顺序的榜单。
//
// ZSET 里只有 id 和分数，标题必须回 MySQL 取。
// 用一次 IN 查询取完，避免每条一次 SELECT；
// 再按 map 回填，保证输出顺序跟排名一致，而不是数据库返回的顺序。
// 文章被删后 ZSET 里可能还留着 id（对账的 Fix 会清理），
// 这里查不到就留空标题，条目不丢，避免榜单长度忽多忽少。
func (s *HotService) loadByIDs(ctx context.Context, rows []hotRow) ([]HotArticle, error) {
	if len(rows) == 0 {
		return []HotArticle{}, nil
	}

	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ArticleID)
	}

	var articles []model.Article
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&articles).Error; err != nil {
		return nil, fmt.Errorf("load hot articles: %w", err)
	}
	byID := make(map[uint]model.Article, len(articles))
	for _, a := range articles {
		byID[a.ID] = a
	}

	list := make([]HotArticle, 0, len(rows))
	for _, r := range rows {
		a := byID[r.ArticleID]
		list = append(list, HotArticle{
			ArticleID: r.ArticleID,
			Score:     float64(r.Score),
			Title:     a.Title,
			LikeCount: r.Score,
		})
	}
	return list, nil
}
