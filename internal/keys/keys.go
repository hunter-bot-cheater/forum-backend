// Package keys 集中定义跨模块共用的 Redis key。
//
// 点赞用户集合、热榜 ZSET、抢楼发号器这些 Redis 结构会被多个业务模块读写
// （article 删文时要清投影、like 写入时要加分、hot/reconcile 要读榜单、
// floor 抢楼要发号），所以 key 的拼法放在一个地方，避免各模块各自拼字符串导致对不上。
package keys

import "strconv"

// key 里统一带 v1 版本号：将来结构变了，改成 v2 就能让旧 key 自然过期，
// 不用写迁移脚本去清理线上残留。
const LikeSetKeyPrefix = "article:like:v1:"          // 文章点赞用户集合（SET），成员是 user_id
const ArticleViewKeyPrefix = "article:view:v1:"      // 浏览去重标记（STRING，SETNX 写入，到期自动消失）
const HotArticlesKey = "hot:articles:v1"             // 热榜（ZSET），成员是 article_id，score 是热度
const FloorNextKeyPrefix = "floor:event:v1:"         // 抢楼发号器前缀
const RefreshTokenKeyPrefix = "auth:refresh:v1:"     // refresh token 的 jti 登记表前缀
const RefreshFamilyKeyPrefix = "auth:family:v1:"     // 一条刷新链（一次登录）包含的全部 jti
const LoginFailKeyPrefix = "auth:fail:v1:"           // 登录失败计数前缀
const ArticleDupKeyPrefix = "article:dup:v1:"        // 重复文章拦截标记前缀
const TagSuggestKeyPrefix = "article:tagsuggest:v1:" // 标签建议的按用户配额前缀

// LikeSetKey 返回某篇文章点赞用户集合的 Redis key。
func LikeSetKey(articleID uint) string {
	return LikeSetKeyPrefix + strconv.FormatUint(uint64(articleID), 10)
}

// ArticleViewKey 返回「某访问者看过某篇文章」的去重标记 key，形如
// article:view:v1:42:u:7。
//
// viewer 由调用方拼，取值是 u:<user_id>（登录）或 ip:<地址>（游客）。
// 冒号在两种形态里都只出现在 level 分隔处，用户 id 与 IP 本身不含冒号，不会拼歪。
// 删除文章时不需要清理这些 key：它们只活 30 分钟，靠 TTL 自然回收。
func ArticleViewKey(articleID uint, viewer string) string {
	return ArticleViewKeyPrefix + strconv.FormatUint(uint64(articleID), 10) + ":" + viewer
}

// FloorNextKey 返回某场抢楼活动的发号计数器 key。
// 对这个 key 做 INCR，Redis 单线程执行命令，保证每次返回的值都不同。
func FloorNextKey(eventID uint) string {
	return FloorNextKeyPrefix + strconv.FormatUint(uint64(eventID), 10) + ":next"
}

// RefreshTokenKey 返回某个 refresh token 的登记 key，形如 auth:refresh:v1:7:ab12…
//
// 值为 "1"，TTL 与 refresh 本身相同。它的存在表示「这个 jti 还没被用过」：
// 刷新时先 DEL，删得掉才算首次使用，删不掉说明被重放过。
func RefreshTokenKey(userID uint, jti string) string {
	return RefreshTokenKeyPrefix + strconv.FormatUint(uint64(userID), 10) + ":" + jti
}

// RefreshTokenKeyPattern 返回某用户全部 refresh 的匹配模式，用于泄露时一次性撤销。
// 只在检测到 token 重放这种极少数场景用 SCAN 扫，不进热路径。
func RefreshTokenKeyPattern(userID uint) string {
	return RefreshTokenKeyPrefix + strconv.FormatUint(uint64(userID), 10) + ":*"
}

// RefreshFamilyKey 返回一条刷新链的 key，形如 auth:family:v1:7:ab12…
// 值是这条链上签发过的全部 jti（SET）。检测到重放时按这里逐个撤销，
// 范围只到「这一次登录」，同一个人别的设备照常在线。
func RefreshFamilyKey(userID uint, familyID string) string {
	return RefreshFamilyKeyPrefix + strconv.FormatUint(uint64(userID), 10) + ":" + familyID
}

// RefreshFamilyKeyPattern 返回某用户全部刷新链的匹配模式，改密码时可用来踢掉所有设备。
func RefreshFamilyKeyPattern(userID uint) string {
	return RefreshFamilyKeyPrefix + strconv.FormatUint(uint64(userID), 10) + ":*"
}

// LoginFailKey 返回某个用户名的登录失败计数 key。
// 用用户名而不是 IP 做键：撞库会不断换 IP，但目标账号是固定的。
func LoginFailKey(username string) string {
	return LoginFailKeyPrefix + username
}

// TagSuggestKey 返回某用户本小时的标签建议次数计数 key。
// 它护的是外部大模型额度：接口是登录就能调的，不限次会被脚本刷成账单。
func TagSuggestKey(userID uint) string {
	return TagSuggestKeyPrefix + strconv.FormatUint(uint64(userID), 10)
}

// ArticleDupKey 返回「同一用户短时间发过同样内容」的标记 key。
// 内容是标题与正文的哈希，只用来判重，不落库。
func ArticleDupKey(userID uint, digest string) string {
	return ArticleDupKeyPrefix + strconv.FormatUint(uint64(userID), 10) + ":" + digest
}
