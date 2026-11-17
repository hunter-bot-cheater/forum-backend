package hot

import (
	"context"
	"errors"
	"strconv"

	"github.com/redis/go-redis/v9"

	"forum-backend/internal/keys"
)

// 热榜分数的两个权重。改这里就够了：rebuild 的 SQL、点赞写入、对账判断、
// 浏览增量四处的口径全部取自这两个常量。
//
// 3:1 反映两种信号的成本差别。点赞要先打开文章、读完、再点一下，
// 一次点赞背后至少有三次访问；浏览可以是一眼扫过、也可以是误点。
// 只按点赞排时，「很多人读过但没人点赞」的文章永远沉底，
// 而这类文章恰恰是论坛里最典型的好内容——它有用，但不至于让人激动到点赞。
const (
	scoreLikeWeight = 3
	scoreViewWeight = 1
)

// ScoreOf 把点赞数与浏览数合成热榜分数。
//
// 纯函数，没有 IO：rebuild 按库里的值算名次、点赞后写绝对值、
// 浏览时加增量、对账时判断 Redis 该是多少，四处共用同一套口径。
// 分开写四遍的话，改权重时漏掉一处，榜单与对账就会互相指认对方是错的。
//
// 没有时间衰减。当前榜单是「累计热度」，一篇半年前的文章只要浏览量足够大
// 就会一直排在前面。要做近期热度得把 rebuild 的 SQL 一起改成带时间窗口的，
// 那时这个函数要加一个「发布至今多久」的参数，调用点会一起变，所以不在这里预留。
func ScoreOf(likeCount, viewCount int64) int64 {
	return likeCount*scoreLikeWeight + viewCount*scoreViewWeight
}

// AddViewScore 给已经上榜的文章加一次浏览的权重。
//
// 为什么用 ZADD XX INCR 而不是像点赞那样写绝对值：
// 浏览路径手上只有「刚刚多了 1 次」，没有点赞数，算不出绝对分数。
// 为什么带 XX：不带的话，每篇被打开过的文章都会进 ZSET，
// 这个集合几年下来就成了全站文章的镜像，而榜单只需要最热的那几百篇。
// 浏览让新文章进榜这种事交给每小时的重建，或者等它的第一个赞——
// 点赞路径能拿到点赞数，会写一个完整分数进去。
//
// 成员不在榜上时 Redis 返回空，go-redis 转成 redis.Nil，这是正常结果不是错误。
func AddViewScore(ctx context.Context, rdb *redis.Client, articleID uint) error {
	member := strconv.FormatUint(uint64(articleID), 10)
	// ZAddArgsIncr 才是 ZADD ... INCR 的入口：ZAddArgs 结构体上只有 NX/XX/GT/LT/Ch，
	// 没有 Incr 字段，增量这个行为由调用的方法决定。
	_, err := rdb.ZAddArgsIncr(ctx, keys.HotArticlesKey, redis.ZAddArgs{
		XX:      true,
		Members: []redis.Z{{Score: scoreViewWeight, Member: member}},
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	return err
}
