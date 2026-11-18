// Package session 管理 refresh token 的生命周期：签发、旋转与撤销。
//
// 为什么要有这一层而不是在 handler 里直接签 JWT：
// refresh 的价值全在「服务端能撤销、能用一次就作废」，
// 这需要一份额外的登记表（Redis），签发、刷新、登出三个入口必须共用同一套规则。
// 把规则散在 handler 里，很容易出现「登出时删了、刷新时忘了校验」这种缺口。
//
// 旋转（rotation）：每次刷新返回一个新的 refresh，旧的立刻从登记表删除。
// 同一个旧 jti 被用第二次只有两种可能——客户端重放，或者 token 被复制了。
// 两种情况都按泄露处理：撤销整条刷新链（fid 相同的一批），让攻击者手里的也失效。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"forum-backend/internal/keys"
	"forum-backend/pkg/jwt"
)

// 两个哨兵错误，handler 据此决定回 401 还是 503。
var (
	// ErrInvalidRefresh 表示 refresh 本身不可用（签名错、过期、类型不对、已登记过期）。
	// 对客户端来说等价于「请重新登录」。
	ErrInvalidRefresh = errors.New("refresh token is invalid or expired")
	// ErrRefreshReused 表示这个 refresh 已经被用过一次，按泄露处理。
	// 与 ErrInvalidRefresh 分开只是为了日志能区分「过期」和「重放」。
	ErrRefreshReused = errors.New("refresh token has been used already")
)

// Tokens 是一次签发的结果，字段名直接就是登录与刷新接口的响应形状。
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// ExpiresIn 是 access token 剩余秒数，前端据此提前刷新，
	// 而不是等请求 401 了才去刷（那样每次刷新都要多花一个往返）。
	ExpiresIn int `json:"expires_in"`
}

// Service 是 refresh token 的签发与撤销入口。由装配层创建一次，注入 user 包的 handler。
type Service struct {
	jwt *jwt.Manager
	rdb *redis.Client
}

// NewService 由 router 在装配期调用一次。两个依赖都必填：
// 没有 Redis 就无法判断 refresh 是否被重放过，旋转的安全保证整条都不成立。
func NewService(jwtManager *jwt.Manager, rdb *redis.Client) *Service {
	return &Service{jwt: jwtManager, rdb: rdb}
}

// Issue 为刚登录的用户签发一对 token，并新建一条刷新链。
//
// 顺序是「先登记再返回」：如果登记失败就直接报错，
// 绝不能返回一个服务端查不到 jti 的 refresh——那种 token 刷新时会被当成泄露，
// 把用户刚建立的那条链一起撤掉。
func (s *Service) Issue(ctx context.Context, userID uint) (Tokens, error) {
	familyID, err := randomID()
	if err != nil {
		return Tokens{}, fmt.Errorf("generate family id: %w", err)
	}
	return s.issueInFamily(ctx, userID, familyID)
}

// Refresh 用旧 refresh 换一对新 token，旧 jti 立刻作废（旋转）。
//
// 三种结果：
//   - 正常：删掉旧 jti 成功，签发新的一对，新 jti 登记在同一 family 下；
//   - 重放：旧 jti 已经不在登记表里，撤销整个 family，返回 ErrRefreshReused；
//   - Redis 故障：直接报错（不降级放行）。
//
// 这里与浏览数的降级方向相反，是有意为之：计数错了只是数字不准，
// 而 Redis 不可用时无法判断 token 是否被重放，放行等于把旋转这道防线关掉。
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	userID, jti, familyID, err := s.jwt.ParseRefresh(refreshToken)
	if err != nil {
		return Tokens{}, ErrInvalidRefresh
	}

	// DEL 返回受影响键数：1 表示这个 jti 还在，是首次使用；
	// 0 表示登记表里没有它，说明已经用过（或早就过期被清理）。
	// 用 DEL 的返回值做判断，而不是先 GET 再 DEL：后者两步之间有窗口，
	// 同一个 refresh 并发刷新时会双双通过。
	removed, err := s.rdb.Del(ctx, keys.RefreshTokenKey(userID, jti)).Result()
	if err != nil {
		return Tokens{}, fmt.Errorf("consume refresh token: %w", err)
	}
	if removed == 0 {
		// 登记表里没有这个 jti，先分清是「这条链已经结束」还是「链还在、票却重复出现」：
		//   - family 不存在：登出过，或因泄露被撤过，属于普通失效，不该再报一次泄露；
		//   - family 还在：这条登录本该只有一个有效 jti，同一个旧 jti 又出现，
		//     只能是 token 被复制了，撤销整条链。
		//
		// 这里判的是 family 这个 key 还在不在，不能用 SIsMember 判 jti 在不在集合里。
		// 刷新时会把旧 jti 从集合里 SREM 掉，所以「集合里查不到 J1」是每条链的常态，
		// 拿它当判断依据的话，所有重放都会被当成陌生 token 放过去。
		// 只要这条登录还有效，family key 就一定存在（里面躺着当前那一个 jti），
		// 这就足以证明「链还在、旧票却出现了」。
		exists, err := s.rdb.Exists(ctx, keys.RefreshFamilyKey(userID, familyID)).Result()
		if err != nil {
			return Tokens{}, fmt.Errorf("check refresh family: %w", err)
		}
		if exists == 0 {
			return Tokens{}, ErrInvalidRefresh
		}
		// 撤销整条链。失败也只记在这里的错误里，由调用方决定怎么处理，
		// 但无论撤销是否成功，本次刷新都必须拒绝。
		if revokeErr := s.revokeFamily(ctx, userID, familyID); revokeErr != nil {
			return Tokens{}, fmt.Errorf("%w: revoke family: %w", ErrRefreshReused, revokeErr)
		}
		return Tokens{}, ErrRefreshReused
	}

	// 旧 jti 已作废，同步从链上摘掉，让 family 恒等于「当前有效 jti」。
	//
	// 不摘的话 SET 只增不减：family 的 TTL 每次刷新都重置（滑动续期），
	// 活跃用户的成员数会随在线时长一直涨，7 天约 672 个、一个月近三千，
	// 而成员数一旦超过 128 就从 listpack 转成 hashtable，内存开销翻倍。
	//
	// 顺序必须先 DEL 后 SREM。反过来的话，SREM 成功而 DEL 失败会留下
	// 「登记表里还有、family 里没有它」的 jti，撤销时 SMembers 扫不到，
	// 这张票就再也无法被撤销。当前顺序最坏只是多留一个已失效成员。
	//
	// 失败只记日志不中断：多留一个废成员，撤销时多打一次空 DEL，无害。
	if err := s.rdb.SRem(ctx, keys.RefreshFamilyKey(userID, familyID), jti).Err(); err != nil {
		slog.WarnContext(ctx, "remove consumed jti from family",
			"user_id", userID, "jti", jti, "error", err)
	}

	return s.issueInFamily(ctx, userID, familyID)
}

// Revoke 撤销一个 refresh（登出）。
//
// 撤销的是整条刷新链而不是单个 jti：一次登录只对应一条链，
// 链上同一时刻能用的只有最新那个 jti。留着旧 jti 的家族记录没有意义，
// 还会让「登出后再刷新」被误判成泄露（见 Refresh 里的 exists 判断）。
//
// 解析失败直接忽略：过期或伪造的 token 本来就没有登记，
// 登出接口不该因为客户端手里的旧 token 报错。
func (s *Service) Revoke(ctx context.Context, refreshToken string) error {
	userID, _, familyID, err := s.jwt.ParseRefresh(refreshToken)
	// 解析成功才去撤销。解析失败直接当成功返回：
	// 过期或伪造的 token 本来就没有登记，登出接口不该因为客户端手里的旧票报错，
	// 否则用户换台设备点退出会看到一条无意义的失败提示。
	if err == nil {
		if err := s.revokeFamily(ctx, userID, familyID); err != nil {
			return fmt.Errorf("revoke refresh family: %w", err)
		}
	}
	return nil
}

// RevokeAll 撤销某个用户的全部刷新链，所有设备一起下线。
// 目前没有接入口（改密码功能还没做），留着是为了让「踢掉所有设备」有唯一正确实现，
// 以后改密码、封号时直接调用，不用再写一遍 SCAN。
func (s *Service) RevokeAll(ctx context.Context, userID uint) error {
	families, err := s.scanKeys(ctx, keys.RefreshFamilyKeyPattern(userID))
	if err != nil {
		return err
	}
	for _, familyKey := range families {
		members, err := s.rdb.SMembers(ctx, familyKey).Result()
		if err != nil {
			return fmt.Errorf("list family members: %w", err)
		}
		for _, jti := range members {
			if err := s.rdb.Del(ctx, keys.RefreshTokenKey(userID, jti)).Err(); err != nil {
				return fmt.Errorf("revoke refresh %s: %w", jti, err)
			}
		}
		if err := s.rdb.Del(ctx, familyKey).Err(); err != nil {
			return fmt.Errorf("delete family %s: %w", familyKey, err)
		}
	}
	return nil
}

// issueInFamily 签发一对 token 并登记，是新登录与刷新共用的收尾逻辑。
// 刷新时沿用原来的 familyID，所以一条链上的所有 jti 都能被一次撤销。
func (s *Service) issueInFamily(ctx context.Context, userID uint, familyID string) (Tokens, error) {
	jti, err := randomID()
	if err != nil {
		return Tokens{}, fmt.Errorf("generate jti: %w", err)
	}

	access, err := s.jwt.SignAccess(userID)
	if err != nil {
		return Tokens{}, fmt.Errorf("sign access token: %w", err)
	}
	refresh, err := s.jwt.SignRefresh(userID, jti, familyID)
	if err != nil {
		return Tokens{}, fmt.Errorf("sign refresh token: %w", err)
	}

	// 登记 jti。TTL 与 refresh 自身一致，到期两边同时失效，不会留下指向空号的登记。
	//
	// 下面三步不是原子操作，顺序是有意的：先登记 jti，再挂到家族上。
	// 万一 SAdd 失败，代价是这条链少了重放检测（撤销时扫不到它），登录与刷新照常可用；
	// 反过来先 SAdd 再登记，失败时会留下一个「家族里有、登记表里没有」的 jti，
	// 下次刷新必被判成泄露，用户当场被登出。宁可弱一点，也不要无故踢人。
	if err := s.rdb.Set(ctx, keys.RefreshTokenKey(userID, jti), "1", s.jwt.RefreshTTL()).Err(); err != nil {
		return Tokens{}, fmt.Errorf("register refresh token: %w", err)
	}
	// family 是 SET，每条链只保留当前有效的那一个 jti：新 jti 在这里加入，
	// 被消耗的旧 jti 由 Refresh 用 SREM 摘掉，成员数因此稳定在 1 个。
	// 每次 SADD 后重设过期时间，让活跃用户这条链不会因为第一次登录的 7 天到期而被删掉。
	familyKey := keys.RefreshFamilyKey(userID, familyID)
	if err := s.rdb.SAdd(ctx, familyKey, jti).Err(); err != nil {
		return Tokens{}, fmt.Errorf("register refresh family: %w", err)
	}
	if err := s.rdb.Expire(ctx, familyKey, s.jwt.RefreshTTL()).Err(); err != nil {
		return Tokens{}, fmt.Errorf("expire refresh family: %w", err)
	}

	return Tokens{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int(s.jwt.AccessTTL().Seconds()),
	}, nil
}

// revokeFamily 撤销一条刷新链上的全部 jti。
// 调用点是「检测到重放」，属于异常路径，用 SMEMBERS 一次取出再逐个 DEL 就够，
// 不值得为它设计更省往返的 Lua 脚本。
func (s *Service) revokeFamily(ctx context.Context, userID uint, familyID string) error {
	familyKey := keys.RefreshFamilyKey(userID, familyID)
	members, err := s.rdb.SMembers(ctx, familyKey).Result()
	if err != nil {
		return fmt.Errorf("list family members: %w", err)
	}
	for _, jti := range members {
		if err := s.rdb.Del(ctx, keys.RefreshTokenKey(userID, jti)).Err(); err != nil {
			return fmt.Errorf("revoke refresh %s: %w", jti, err)
		}
	}
	if err := s.rdb.Del(ctx, familyKey).Err(); err != nil {
		return fmt.Errorf("delete family: %w", err)
	}
	return nil
}

// scanKeys 用 SCAN 分批遍历匹配的 key。
// 不用 KEYS：KEYS 在大库上会阻塞整个 Redis，而这里可能发生在线上请求路径里。
// 每轮重建游标，直到游标回到 0。
func (s *Service) scanKeys(ctx context.Context, pattern string) ([]string, error) {
	var (
		cursor uint64
		result []string
	)
	for {
		batch, next, err := s.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", pattern, err)
		}
		result = append(result, batch...)
		cursor = next
		if cursor == 0 {
			return result, nil
		}
	}
}

// randomID 生成 16 字节随机数的十六进制串，用作 jti 与 familyID。
// 必须是密码学随机：用时间戳或自增会让攻击者猜到别人的 jti 并主动触发撤销。
func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
