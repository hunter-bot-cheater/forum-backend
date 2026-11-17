package hot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
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
// 值里存的是重建时写进 ZSET 的条数，不只是 "1"。
// 存条数是为了自愈：Redis 做内存淘汰时，ZSET 这种大 key 比这个单值 key
// 先被淘汰的概率高得多，结果就是「ready 还在、榜单空了」。
// 读到空榜时拿这里的条数一比就知道是真空榜（存的是 0）还是投影丢了（存的是 N），
// 前者直接返回空，后者重建一次。值为 0 的老版本（只写 "1"）也能工作：
// "1" 解析出 1 > 0，会走一次重建，重建后值就变成真实条数了。
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

// fallbackTop 是降级路径一次取多少条，同时也是降级缓存的容量。
// 对外接口最多要 100 条，所以固定取 100 就能覆盖所有合法请求：
// 缓存一份、所有 n 都从里面切，不必按 n 分别缓存。
const fallbackTop = 100

// fallbackTTL 是降级结果在进程内保留多久。
//
// 降级发生在 Redis 不可用或投影重建失败时，这期间每个请求都去跑一次
// GROUP BY 会把 MySQL 的 CPU 打满——本来就降级了，再被自己压垮就彻底没救。
// 5 秒足够让一次故障期间只打几次库，又短到热榜数据不会明显过期。
const fallbackTTL = 5 * time.Second

// HotArticle 是热榜里的一条，service 查好标题后返回给 handler。
//
// Score 是排名依据，等于 ScoreOf(LikeCount, ViewCount)；
// 两个原始数字单独留着，前端才能解释「它凭什么排在这里」。
// Liked 的主语是当前访问者，不是文章本身：它由 FillLiked 按需填，
// 不参与任何排序与缓存。
type HotArticle struct {
	ArticleID uint    `json:"article_id"`
	Score     float64 `json:"score"`
	Title     string  `json:"title"`
	LikeCount int64   `json:"like_count"`
	ViewCount int64   `json:"view_count"`
	Liked     bool    `json:"liked"`
}

// hotRow 是 MySQL 聚合查询的落点：一篇怎么排、得了多少分、原始计数各是多少。
// 列名用 gorm tag 指定，因为别名是 SQL 里写的，不是结构体字段名推导出来的。
//
// LikeCount 在两条路径上来源不同：重建时由 SQL 一次聚合出来，
// 读 ZSET 时只能单独查一次（分数是合成值，反推不出点赞数）。
type hotRow struct {
	ArticleID uint  `gorm:"column:article_id"`
	Score     int64 `gorm:"column:score"`
	LikeCount int64 `gorm:"column:like_count"`
	ViewCount int64 `gorm:"column:view_count"`
}

// HotService 热榜业务逻辑。
type HotService struct {
	db  *gorm.DB
	rdb *redis.Client
	// group 把同一时刻发现「投影丢失」的请求合并成一次重建。
	// singleflight.Group 零值可用，不需要初始化。
	group singleflight.Group

	// fallback 缓存降级路径的结果，只在进程内，不落 Redis。
	// 降级本来就是因为 Redis 不可用或投影不可信，这时再指望 Redis 存缓存是矛盾的。
	fallback fallbackCache
}

// fallbackCache 是降级结果的内存缓存。
//
// 用互斥锁而不是 singleflight：这里要的不只是「合并并发查询」，
// 还要「拿到锁的那个查完，后面排队的人直接用结果」。
// 查库期间持锁会让并发请求排队，这正好起到给 MySQL 限流的作用。
type fallbackCache struct {
	mu        sync.Mutex
	rows      []hotRow
	expiresAt time.Time
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

// readyState 是 hotReadyKey 里记的内容。
// initialized 为 false 表示 key 不存在（从没建过，或 TTL 到期）。
// rows 是上次重建写进 ZSET 的条数，用来区分「真空榜」和「投影被淘汰」。
type readyState struct {
	initialized bool
	rows        int64
}

// Top 返回热榜前 n 名，优先读 ZSET。
//
// 三条路径，按成本从低到高：
//  1. ready 在：一次 GET + 一次 ZREVRANGE 就返回，线上绝大多数请求走这里
//  2. ready 不在：进 ensureHotZSet 重建一次，重建好再读
//  3. Redis 出错或重建失败：降级到 GROUP BY 直接查 MySQL，接口仍可用，只是慢
//
// 另有一条自愈路径：ready 在、ZSET 却空了，说明投影被内存淘汰掉了
// （大 key 比单值 key 更容易先被淘汰）。这时按 ready 里记的条数判断：
// 记着 0 就是真没人点赞，直接返回空；记着 N 说明丢过数据，重建一次。
func (s *HotService) Top(ctx context.Context, n int) ([]HotArticle, error) {
	if err := checkTopN(n); err != nil {
		return nil, err
	}

	// ready 存在说明榜单已经按 MySQL 建过一次（哪怕建出来是 0 条）。
	ready, err := s.readyState(ctx)
	if err != nil {
		// Redis 报错（断连、超时）直接降级，不让缓存故障变成接口故障。
		slog.WarnContext(ctx, "check hot ready key", "error", err)
		return s.TopFromMySQL(ctx, n)
	}

	if !ready.initialized {
		// 刚启动、Redis 被清空、或者 ready 的 TTL 到期，都需要重建。
		// 冷却期内不再试，直接降级，避免一次失败的重建被反复触发。
		if s.inCooldown(ctx) {
			return s.TopFromMySQL(ctx, n)
		}
		if err := s.ensureHotZSet(ctx, false); err != nil {
			slog.WarnContext(ctx, "rebuild hot zset", "error", err)
			s.markCooldown(ctx)
			return s.TopFromMySQL(ctx, n)
		}
	}

	// ZREVRANGE 按分数从大到小取前 n 个，WITHSCORES 把分数一起带回来，
	// 省掉为每条再查一次 ZSCORE。
	rows, err := s.topFromZSet(ctx, n)
	if err != nil {
		slog.WarnContext(ctx, "load hot ranking", "error", err)
		return s.TopFromMySQL(ctx, n)
	}

	// 读到空榜且 ready 记着非 0 条，说明 ZSET 丢了而不是没人点赞。
	// 这一步放在读到空之后才做，正常命中路径不会为它多付一次判断。
	if len(rows) == 0 && ready.rows > 0 {
		slog.WarnContext(ctx, "hot zset is empty but ready says otherwise", "expected", ready.rows)
		if err := s.ensureHotZSet(ctx, true); err != nil {
			slog.WarnContext(ctx, "self-heal hot zset", "error", err)
			return s.TopFromMySQL(ctx, n)
		}
		rows, err = s.topFromZSet(ctx, n)
		if err != nil {
			slog.WarnContext(ctx, "load hot ranking after self-heal", "error", err)
			return s.TopFromMySQL(ctx, n)
		}
	}
	return s.loadByIDs(ctx, rows)
}

// FillLiked 给榜单补上「当前访问者赞过哪些」，viewerID 为 0（游客）时直接返回。
//
// 单独一步而不是塞进 Top：Top 的结果与访问者无关，对账和自愈都复用它；
// 「谁在看」每个请求都不同，混进 Top 会让同一份榜单没法共用。
//
// 一次 IN 查询取回这一屏的标记，走 uk_article_user 的最左前缀，
// 每条只扫自己那段索引。游客不查：热榜是公开页，大多数访问没有登录态，
// 为它们多打一次库换不到任何信息。
func (s *HotService) FillLiked(ctx context.Context, list []HotArticle, viewerID uint) error {
	if viewerID == 0 || len(list) == 0 {
		return nil
	}

	ids := make([]uint, 0, len(list))
	for _, item := range list {
		ids = append(ids, item.ArticleID)
	}

	var likedIDs []uint
	err := s.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Where("user_id = ? AND article_id IN ?", viewerID, ids).
		Pluck("article_id", &likedIDs).Error
	if err != nil {
		return fmt.Errorf("load liked marks for %d articles: %w", len(ids), err)
	}

	// 用集合回填而不是逐条判断：IN 查回来的顺序由数据库决定，
	// 和榜单顺序无关，按下标对齐是错的。
	liked := make(map[uint]struct{}, len(likedIDs))
	for _, id := range likedIDs {
		liked[id] = struct{}{}
	}
	for i := range list {
		if _, ok := liked[list[i].ArticleID]; ok {
			list[i].Liked = true
		}
	}
	return nil
}

// readyState 读 hotReadyKey 的值并解析成 readyState。
// key 不存在返回零值（initialized=false），不算错误。
func (s *HotService) readyState(ctx context.Context) (readyState, error) {
	raw, err := s.rdb.Get(ctx, hotReadyKey).Result()
	if errors.Is(err, redis.Nil) {
		return readyState{}, nil
	}
	if err != nil {
		return readyState{}, err
	}

	rows, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		// 值不是数字（手工写进去的、或更早的版本只写 "1"）：
		// 当成「建过且可能有数据」，让空榜检查走一次重建把值修正成真实条数。
		//nolint:nilerr // 坏值按「建过且可能有数据」处理是刻意的降级，为的是触发一次重建把值修正回来
		return readyState{initialized: true, rows: 1}, nil
	}
	return readyState{initialized: true, rows: rows}, nil
}

// inCooldown 判断是否处于「重建失败后的退避期」。
// 读失败时不进冷却判断，直接返回 false 让调用方去试——反正试也会失败，
// 但至少不会因为一个读不到的状态把一个本该可用的重建挡掉。
func (s *HotService) inCooldown(ctx context.Context) bool {
	cool, err := s.rdb.Exists(ctx, hotCooldownKey).Result()
	return err == nil && cool > 0
}

// topFromZSet 读 ZSET 前 n 名，转成只带 id 与分数的行。
func (s *HotService) topFromZSet(ctx context.Context, n int) ([]hotRow, error) {
	// ZSET 的成员是字符串形式的文章 id，这里转回数字，
	// 顺便决定后面查文章标题时 IN 列表里放什么。
	pairs, err := s.rdb.ZRevRangeWithScores(ctx, keys.HotArticlesKey, 0, int64(n-1)).Result()
	if err != nil {
		return nil, err
	}

	rows := make([]hotRow, 0, len(pairs))
	for _, p := range pairs {
		// 用带 ok 的断言：成员总是字符串（写的时候就是 FormatUint 的产物），
		// 但万一有人手工往榜单里写了个非字符串成员，裸断言会 panic，
		// 把一次「榜单脏了」升级成「热榜接口挂了」。
		member, ok := p.Member.(string)
		if !ok {
			return nil, fmt.Errorf("hot member %v is not a string", p.Member)
		}
		id, err := strconv.ParseUint(member, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse hot member %v: %w", p.Member, err)
		}
		rows = append(rows, hotRow{ArticleID: uint(id), Score: int64(p.Score)})
	}

	// ZSET 里只有合成分数，反推不出点赞数，这里补一次。
	// 放在解析之后统一做，两条读取路径（正常读、自愈后重读）都能用到。
	if err := s.fillLikeCounts(ctx, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// fillLikeCounts 给来自 ZSET 的行补上点赞数。
//
// 榜单展示的「♥ N」是原始点赞数，不是分数。
// 这个查询是读榜多出来的一次成本，换来的是数字不会被合成分数污染。
func (s *HotService) fillLikeCounts(ctx context.Context, rows []hotRow) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ArticleID)
	}
	counts, err := countLikesByIDs(ctx, s.db, ids)
	if err != nil {
		return err
	}
	for i := range rows {
		rows[i].LikeCount = counts[rows[i].ArticleID]
	}
	return nil
}

// ensureHotZSet 把同一时刻发现「投影丢失」的请求合并成一次重建。
// 没有它，FLUSHDB 之后 100 个并发请求会各查一次 MySQL。
//
// force 为 true 时跳过「ready 已经存在」的双重检查。
// 自愈场景必须传 true：那时 ready 恰恰是存在的（丢的是 ZSET），
// 双重检查会把等待者直接放行，谁都不会去重建，空榜单就一直空着。
func (s *HotService) ensureHotZSet(ctx context.Context, force bool) error {
	_, err, _ := s.group.Do("hot:rebuild", func() (interface{}, error) {
		// 闭包用的是第一个请求的 ctx。它被取消会连累所有等待者，
		// 所以这里去掉取消信号，另给 3 秒超时防止无限期跑下去。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()

		// 双重检查：等待者醒来时，第一个请求可能已经把投影建好了。
		if !force {
			ready, err := s.rdb.Exists(loadCtx, hotReadyKey).Result()
			if err != nil {
				return nil, err
			}
			if ready > 0 {
				return nil, nil
			}
		}
		return nil, s.rebuild(loadCtx)
	})
	return err
}

// TopFromMySQL 是降级路径：绕开 Redis，直接 GROUP BY 点赞表算前 n 名。
// 比走缓存慢，但 Redis 挂了接口也不该挂。
//
// 结果在进程内缓存 fallbackTTL：降级期间每个请求都跑一次聚合，
// 等于用故障把数据库压垮，而这时候最需要的是让数据库活着。
func (s *HotService) TopFromMySQL(ctx context.Context, n int) ([]HotArticle, error) {
	rows, err := s.fallbackRows(ctx)
	if err != nil {
		return nil, err
	}

	// 缓存的是前 fallbackTop 名，这里按请求条数截取。
	// n 的上限由 checkTopN 保证不超过 100，所以不需要再判越界。
	if n < len(rows) {
		rows = rows[:n]
	}
	return s.loadByIDs(ctx, rows)
}

// fallbackRows 返回降级用的聚合结果，优先用未过期的进程内缓存。
//
// 持锁期间查库：并发请求会在锁上排队，而不是一起涌向 MySQL。
// 这是有意的——降级场景下「慢一点但活着」远好过「全都一起查库」。
func (s *HotService) fallbackRows(ctx context.Context) ([]hotRow, error) {
	s.fallback.mu.Lock()
	defer s.fallback.mu.Unlock()

	now := time.Now()
	if now.Before(s.fallback.expiresAt) {
		return s.fallback.rows, nil
	}

	rows, err := s.groupTop(ctx, fallbackTop)
	if err != nil {
		return nil, err
	}
	// 存空切片而不是 nil：调用方 len() 结果一样，但序列化时 nil 会变成 null。
	if rows == nil {
		rows = []hotRow{}
	}
	s.fallback.rows = rows
	s.fallback.expiresAt = now.Add(fallbackTTL)
	return rows, nil
}

// markCooldown 在一次重建失败后打上退避标记。
// 用 WithoutCancel：触发它的那个请求可能已经超时断开，
// 但「别再重试」这个结论要留下来给后面的请求看。
func (s *HotService) markCooldown(ctx context.Context) {
	if err := s.rdb.Set(context.WithoutCancel(ctx), hotCooldownKey, "1", hotCooldownTTL).Err(); err != nil {
		slog.WarnContext(ctx, "mark hot cooldown", "error", err)
	}
}

// rebuild 从唯一事实来源全量重建 ZSET，最后打上已初始化标记。
// 只由 ensureHotZSet 调用，不对外暴露，也不再有脚本调用它。
//
// 三次改写留下的两条铁律：
//  1. SQL 必须带 LIMIT。不带的话，articles 涨到百万级，
//     这条带 JOIN 的 GROUP BY 会扫穿两张表，MySQL 的 CPU 直接被打满。
//     聚合本身仍然要过一遍索引，LIMIT 省下的是结果集和后续的 ZADD 条数。
//  2. 新榜必须落在临时 key 上，写完 RENAME 换过去。
//     直接在正式 key 上 Del 再逐条 ZAdd，中途任何一次读都会拿到残缺的榜单，
//     而重建期间的点赞也会被后面的全量覆盖冲掉。
func (s *HotService) rebuild(ctx context.Context) error {
	rows, err := s.loadScoreRows(ctx, hotRebuildTop)
	if err != nil {
		return err
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
	// 值写真实条数而不是 "1"：读侧靠它区分「真空榜」和「ZSET 被内存淘汰」，
	// 写死 "1" 的话这两种情况看起来一模一样，投影丢了也自愈不了。
	// TTL 让榜单每小时自然失效一次，避免变成历史快照。
	pipe.Set(ctx, hotReadyKey, strconv.Itoa(len(rows)), hotReadyTTL)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("rebuild hot zset: %w", err)
	}
	return nil
}

// loadScoreRows 从唯一事实来源算前 limit 名：点赞数在 article_likes 里，
// 浏览数在 articles.view_count 上，两者合起来才是热榜分数。
//
// 为什么排序交给数据库：在 Go 里算完再排，就得先把整张 articles 捞回来，
// LIMIT 也就白写了。代价是 SQL 里要重写一遍 ScoreOf 的公式——
// 权重常量取自本包，改公式时两处一起改，score.go 那里有说明。
//
// 为什么是 LEFT JOIN：没有点赞的文章也要能上榜。
// 这正是不再只按点赞排序之后最主要的收益——只被大量阅读、
// 没人点赞的文章以前根本进不了候选集。
//
// HAVING score > 0 把「一次没被打开、也没被赞过」的文章挡在外面：
// 它们分数为 0，排到榜里只会是占位的空条目。
func (s *HotService) loadScoreRows(ctx context.Context, limit int) ([]hotRow, error) {
	scoreExpr := fmt.Sprintf("articles.view_count + COUNT(article_likes.id) * %d", scoreLikeWeight)

	var rows []hotRow
	err := s.db.WithContext(ctx).Model(&model.Article{}).
		Select("articles.id AS article_id, articles.view_count AS view_count, " +
			"COUNT(article_likes.id) AS like_count, " + scoreExpr + " AS score").
		Joins("LEFT JOIN article_likes ON article_likes.article_id = articles.id").
		Group("articles.id, articles.view_count").
		Having(scoreExpr + " > 0").
		Order(scoreExpr + " DESC, articles.id ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("group hot scores: %w", err)
	}
	return rows, nil
}

// groupTop 直接按库里的计数算前 n 名，是 Top 的降级实现。
// 与 rebuild 的区别只有 LIMIT 取调用方指定的条数（降级缓存固定取 fallbackTop），
// 以及不写任何 Redis key。
func (s *HotService) groupTop(ctx context.Context, n int) ([]hotRow, error) {
	return s.loadScoreRows(ctx, n)
}

// countLikesByIDs 一次取回这批文章的点赞数，给榜单展示用。
//
// ZSET 里存的是点赞与浏览的合成分数，反推不出点赞数，只能单独查一次。
// IN 走 uk_article_user 的最左前缀，每条只扫自己那段索引。
// 这是每次读榜多出来的一次查询，换来的是「♥ 12」这个数字不会显示成合成分数。
func countLikesByIDs(ctx context.Context, db *gorm.DB, ids []uint) (map[uint]int64, error) {
	counts := make(map[uint]int64, len(ids))
	if len(ids) == 0 {
		return counts, nil
	}
	var rows []hotRow
	err := db.WithContext(ctx).Model(&model.ArticleLike{}).
		Select("article_id, COUNT(*) AS like_count").
		Where("article_id IN ?", ids).
		Group("article_id").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("count likes for %d articles: %w", len(ids), err)
	}
	for _, row := range rows {
		counts[row.ArticleID] = row.LikeCount
	}
	return counts, nil
}

// loadByIDs 按 id 批量补标题与浏览数，返回保持传入顺序的榜单。
//
// 传进来的 rows 必须已经把 LikeCount 填好：重建与降级走 SQL 聚合时自带，
// 读 ZSET 时由 fillLikeCounts 补。这里不再查点赞表——
// 降级路径的结果是带缓存的，每读一次都回库数点赞等于把缓存废掉。
//
// 标题与浏览数回 articles 取，用一次 IN 查询取完，避免每条一次 SELECT；
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
			LikeCount: r.LikeCount,
			ViewCount: a.ViewCount,
		})
	}
	return list, nil
}
