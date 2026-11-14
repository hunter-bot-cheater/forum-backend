// Package keys 集中定义跨模块共用的 Redis key。
//
// 点赞用户集合、热榜 ZSET、抢楼发号器这些 Redis 结构会被多个业务模块读写
// （article 删文时要清投影、like 写入时要加分、hot/reconcile 要读榜单、
// floor 抢楼要发号），所以 key 的拼法放在一个地方，避免各模块各自拼字符串导致对不上。
package keys

import "strconv"

const LikeSetKeyPrefix = "article:like:v1:"
const HotArticlesKey = "hot:articles:v1"
const FloorNextKeyPrefix = "floor:event:v1:"

// LikeSetKey 返回某篇文章点赞用户集合的 Redis key。
func LikeSetKey(articleID uint) string {
	return LikeSetKeyPrefix + strconv.FormatUint(uint64(articleID), 10)
}

// FloorNextKey 返回某场抢楼活动的发号计数器 key。
// 对这个 key 做 INCR，Redis 单线程执行命令，保证每次返回的值都不同。
func FloorNextKey(eventID uint) string {
	return FloorNextKeyPrefix + strconv.FormatUint(uint64(eventID), 10) + ":next"
}
