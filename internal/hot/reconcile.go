package hot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/event"
	"forum-backend/internal/keys"
	"forum-backend/internal/model"
)

// reconcileTopN 是单次对账默认检查的榜单深度。
// 不做全量对账：全量要 GROUP BY 整张点赞表，成本问题已经在 service.go 的 rebuild 注释里论证过。
// 榜单之外的文章即使漂移也影响不到接口输出，等它们真上榜时 rebuild 会给一次正确值。
const reconcileTopN = 200

// HotDrift 是一处热榜分数漂移。
// InMySQL 为 0 表示数据库里已经没有点赞，Redis 还留着分数。
type HotDrift struct {
	ArticleID uint  `json:"article_id"`
	InRedis   int64 `json:"in_redis"`
	InMySQL   int64 `json:"in_mysql"`
}

// ReconcileReport 是一次对账的完整结果。
// 它是一份只读快照，Check 之后不会再变，Fix 依据它决定改哪些成员。
type ReconcileReport struct {
	CheckedAt time.Time `json:"checked_at"`
	TopN      int       `json:"top_n"`

	// 热榜投影的状态。
	ZSetSize     int64 `json:"zset_size"`
	Checked      int   `json:"checked"`
	ReadyMissing bool  `json:"ready_missing"`
	// ProjectionLost 表示 ready 标记还在，ZSET 却空了，而 MySQL 里确实有点赞。
	// 这是最危险的一种不一致：Top 看到 ready 就跳过重建，直接读一个空 ZSET，
	// 接口会一直返回空榜，直到 ready 的 TTL（1 小时）到期才恢复。
	ProjectionLost bool       `json:"projection_lost"`
	Drifts         []HotDrift `json:"drifts"`
	Zombies        []uint     `json:"zombies"`

	// 消息链路的状态。这几项不看类型，只看积压量。
	OutboxPending int64 `json:"outbox_pending"`
	OutboxFailed  int64 `json:"outbox_failed"`
	StreamLen     int64 `json:"stream_len"`
	StreamPending int64 `json:"stream_pending"`
}

// Reconciler 对账热榜投影与消息链路。
// 它不重建榜单，只做点对点纠正：投影在但值错了，或者成员指向了不存在的文章。
//
// Check 只读、Fix 才写，两步分开是为了能先看报告再决定改不改：
// 线上先跑一次 Check 确认漂移规模，再决定是让 Fix 逐条改，还是等 rebuild 整体刷新。
type Reconciler struct {
	db  *gorm.DB
	rdb *redis.Client
}

// NewReconciler 由 cmd/reconcile 创建，也用于测试。
func NewReconciler(db *gorm.DB, rdb *redis.Client) *Reconciler {
	return &Reconciler{db: db, rdb: rdb}
}

// Check 取榜单前 topN 名，逐个核对 Redis 分数与 MySQL 真实点赞数。
// 返回的报告是 Fix 的输入。整个过程只读，不写 Redis，也不写 MySQL。
func (r *Reconciler) Check(ctx context.Context, topN int) (*ReconcileReport, error) {
	if topN <= 0 {
		topN = reconcileTopN
	}
	rep := &ReconcileReport{
		CheckedAt: time.Now(),
		TopN:      topN,
		Drifts:    make([]HotDrift, 0),
		Zombies:   make([]uint, 0),
	}

	size, err := r.rdb.ZCard(ctx, keys.HotArticlesKey).Result()
	if err != nil {
		return nil, fmt.Errorf("zcard %s: %w", keys.HotArticlesKey, err)
	}
	rep.ZSetSize = size

	// ready 标记缺失时，榜单本身就可能是「还没建过」而不是「值错了」。
	// 这种情况该走 rebuild，对账只能报告，不能代替它。
	ready, err := r.rdb.Exists(ctx, hotReadyKey).Result()
	if err != nil {
		return nil, fmt.Errorf("exists %s: %w", hotReadyKey, err)
	}
	rep.ReadyMissing = ready == 0

	pairs, err := r.rdb.ZRevRangeWithScores(ctx, keys.HotArticlesKey, 0, int64(topN-1)).Result()
	if err != nil {
		return nil, fmt.Errorf("zrevrange %s: %w", keys.HotArticlesKey, err)
	}

	// 保持 Redis 返回的顺序，后面 diffHot 才能输出稳定的报告，
	// 测试与人工比对都不用再排序。
	ids := make([]uint, 0, len(pairs))
	redisCounts := make(map[uint]int64, len(pairs))
	for _, p := range pairs {
		member, ok := p.Member.(string)
		if !ok {
			log.Printf("reconcile: unexpected member type %T", p.Member)
			continue
		}
		id64, err := strconv.ParseUint(member, 10, 64)
		if err != nil {
			// 非数字成员是脏数据，Fix 会把它当僵尸删掉。
			log.Printf("reconcile: non-numeric member %q", member)
			continue
		}
		id := uint(id64)
		ids = append(ids, id)
		redisCounts[id] = int64(p.Score)
	}
	rep.Checked = len(ids)

	mysqlCounts, err := r.countLikes(ctx, ids)
	if err != nil {
		return nil, err
	}
	alive, err := r.aliveArticles(ctx, ids)
	if err != nil {
		return nil, err
	}

	rep.Drifts, rep.Zombies = diffHot(ids, redisCounts, mysqlCounts, alive)

	// ZSET 为空时多查一次总点赞数，用来区分「真空榜」与「投影被删」。
	// 只在罕见分支上付出这次 COUNT 的成本，正常压测与线上稳态都不会走到。
	if rep.ZSetSize == 0 && !rep.ReadyMissing {
		var totalLikes int64
		err := r.db.WithContext(ctx).Model(&model.ArticleLike{}).Count(&totalLikes).Error
		if err != nil {
			return nil, fmt.Errorf("count total likes: %w", err)
		}
		rep.ProjectionLost = projectionLost(rep.ZSetSize, rep.ReadyMissing, totalLikes)
	}

	if err := r.checkMessageChain(ctx, rep); err != nil {
		return nil, err
	}
	return rep, nil
}

// projectionLost 判断「投影丢了」还是「榜单本来就是空的」。
// ready 在、ZSET 空，有两种可能：
// 一篇文章都没被点赞过，rebuild 的空结果分支就是这么写的（删正式 key，照样写 ready）；
// 或者 ZSET 被单独删掉了（手工 DEL、只清了这一个 key 的脚本、内存淘汰）。
// 唯一可靠的区分依据是 MySQL 里还有没有点赞。
func projectionLost(zsetSize int64, readyMissing bool, totalLikes int64) bool {
	if readyMissing || zsetSize > 0 {
		return false
	}
	return totalLikes > 0
}

// countLikes 用一次带 IN 的 GROUP BY 拿回这批文章的真实点赞数。
// IN 走了 uk_article_user 的最左前缀，所以每条只扫自己那段索引。
func (r *Reconciler) countLikes(ctx context.Context, ids []uint) (map[uint]int64, error) {
	counts := make(map[uint]int64, len(ids))
	if len(ids) == 0 {
		return counts, nil
	}
	var rows []hotRow
	err := r.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Select("article_id, COUNT(*) AS score").
		Where("article_id IN ?", ids).
		Group("article_id").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("count likes for %d articles: %w", len(ids), err)
	}
	for _, row := range rows {
		counts[row.ArticleID] = row.Score
	}
	return counts, nil
}

// aliveArticles 查出这批 id 里哪些文章还在。
// 不在集合里的就是僵尸成员：文章被删了，ZSET 里还留着分数。
// 这种漂移没有人会触发自我修正（文章没了就不会再有点赞），只能靠对账清掉。
func (r *Reconciler) aliveArticles(ctx context.Context, ids []uint) (map[uint]bool, error) {
	alive := make(map[uint]bool, len(ids))
	if len(ids) == 0 {
		return alive, nil
	}
	var found []uint
	err := r.db.WithContext(ctx).Model(&model.Article{}).
		Where("id IN ?", ids).
		Pluck("id", &found).Error
	if err != nil {
		return nil, fmt.Errorf("load alive articles: %w", err)
	}
	for _, id := range found {
		alive[id] = true
	}
	return alive, nil
}

// diffHot 是纯函数，把「该修什么」的判断从 IO 里剥出来，方便单测。
// ids 的顺序决定输出顺序，调用方传的是 Redis 排名顺序。
func diffHot(ids []uint, redisCounts, mysqlCounts map[uint]int64, alive map[uint]bool) ([]HotDrift, []uint) {
	drifts := make([]HotDrift, 0)
	zombies := make([]uint, 0)
	for _, id := range ids {
		if !alive[id] {
			zombies = append(zombies, id)
			continue
		}
		if redisCounts[id] != mysqlCounts[id] {
			drifts = append(drifts, HotDrift{
				ArticleID: id,
				InRedis:   redisCounts[id],
				InMySQL:   mysqlCounts[id],
			})
		}
	}
	return drifts, zombies
}

// checkMessageChain 顺手看一眼消息链路的三处积压。
// 它们不参与热榜修正，但和对账放在同一个报告里，
// 因为「数据不一致」和「事件没投出去」经常同时出现。
func (r *Reconciler) checkMessageChain(ctx context.Context, rep *ReconcileReport) error {
	if err := r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("status = ?", model.OutboxPending).Count(&rep.OutboxPending).Error; err != nil {
		return fmt.Errorf("count pending outbox: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("status = ?", model.OutboxFailed).Count(&rep.OutboxFailed).Error; err != nil {
		return fmt.Errorf("count failed outbox: %w", err)
	}

	length, err := r.rdb.XLen(ctx, event.StreamKey).Result()
	if err != nil {
		return fmt.Errorf("xlen %s: %w", event.StreamKey, err)
	}
	rep.StreamLen = length

	// Stream 或消费组可能还不存在（服务没起来过、Redis 刚清空）。
	// 这时 XPENDING 会报 NOGROUP，直接按 0 处理，不要让整个对账失败。
	// 用 XInfoGroups 探一下，比匹配错误字符串可靠：错误文案会随客户端版本变。
	groups, err := r.rdb.XInfoGroups(ctx, event.StreamKey).Result()
	if err != nil || len(groups) == 0 {
		rep.StreamPending = 0
		return nil
	}
	pending, err := r.rdb.XPending(ctx, event.StreamKey, event.NotifyGroup).Result()
	if err != nil {
		return fmt.Errorf("xpending %s: %w", event.StreamKey, err)
	}
	rep.StreamPending = pending.Count
	return nil
}

// Fix 按报告修正热榜。返回修正的成员数。
// 所有写入都是绝对值或删除，没有增量：增量会把已有的误差继续放大。
// 幂等，连跑两次第二次的 Drifts 应该为空。
func (r *Reconciler) Fix(ctx context.Context, rep *ReconcileReport) (int, error) {
	fixed := 0

	// 投影整体丢失时，删掉 ready 标记就够了：下一次 Top 发现 ready 不在，
	// 会自己走一次 rebuild 把榜单补回来。
	// 不在这里调 rebuild，因为那是 HotService 的私有逻辑，
	// 对账只负责指出问题、清掉挡路的标记，不越界替别人做重建。
	if rep.ProjectionLost {
		if err := r.rdb.Del(ctx, hotReadyKey).Err(); err != nil {
			return fixed, fmt.Errorf("clear stale ready marker: %w", err)
		}
		fixed++
	}

	// 先清僵尸，再改分数。僵尸的分数没有意义，改完还得再删一次。
	for _, id := range rep.Zombies {
		member := strconv.FormatUint(uint64(id), 10)
		if err := r.rdb.ZRem(ctx, keys.HotArticlesKey, member).Err(); err != nil {
			return fixed, fmt.Errorf("remove zombie %s: %w", member, err)
		}
		fixed++
	}

	for _, d := range rep.Drifts {
		member := strconv.FormatUint(uint64(d.ArticleID), 10)
		if d.InMySQL <= 0 {
			// 真实点赞已经归零，成员留着会让榜单出现一个 0 分的空壳。
			if err := r.rdb.ZRem(ctx, keys.HotArticlesKey, member).Err(); err != nil {
				return fixed, fmt.Errorf("remove empty member %s: %w", member, err)
			}
		} else {
			err := r.rdb.ZAdd(ctx, keys.HotArticlesKey, redis.Z{
				Score:  float64(d.InMySQL),
				Member: member,
			}).Err()
			if err != nil {
				return fixed, fmt.Errorf("rewrite score %s: %w", member, err)
			}
		}
		fixed++
	}
	return fixed, nil
}

// Summary 生成一行摘要，给日志和 CLI 直接打印。
// 报告里最该先看的是四个数：检查了多少、漂移几处、僵尸几个、死信几条。
func (r *ReconcileReport) Summary() string {
	return fmt.Sprintf(
		"checked=%d drift=%d zombie=%d ready_missing=%t projection_lost=%t pending=%d failed=%d stream_len=%d stream_pending=%d",
		r.Checked, len(r.Drifts), len(r.Zombies), r.ReadyMissing, r.ProjectionLost,
		r.OutboxPending, r.OutboxFailed, r.StreamLen, r.StreamPending,
	)
}

// HasProblem 判断这次对账是否需要人工或自动处理。
// 只看会真正影响接口输出的项：投影丢失、分数漂移、僵尸成员、死信。
// pending 与 stream_pending 是滞后指标，短时间不为 0 属于正常在途。
func (r *ReconcileReport) HasProblem() bool {
	if r.ProjectionLost || len(r.Drifts) > 0 || len(r.Zombies) > 0 || r.OutboxFailed > 0 {
		return true
	}
	return false
}
