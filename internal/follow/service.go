// Package follow 关注关系：关注、取关、关注数/粉丝数、关注与粉丝列表。
//
// 关系本身只有一张表，展示用的用户信息统一从 user.Briefs 取，
// 不在这个包里再写一遍 users 的查询。
package follow

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"forum-backend/internal/domain"
	"forum-backend/internal/metrics"
	"forum-backend/internal/model"
	"forum-backend/internal/paging"
	"forum-backend/internal/user"
)

// countTimeout 是一次「回源数计数」的时间上限。
//
// 单飞闭包脱离了调用方的 ctx（见 statsCounts 里的说明），
// 所以必须自己带一个上限，否则数据库卡住时这个闭包会一直占着，
// 后续同 key 的请求全部跟着卡死。
const countTimeout = 3 * time.Second

// 关注表的两列，抽成常量是为了在两条列表接口里共用同一段查询代码。
// 只在包内使用，不来自外部输入，拼进 SQL 是安全的。
const (
	colFollower = "follower_id"
	colFollowee = "followee_id"
)

// Service 是关注模块的业务逻辑。
// 依赖 user.UserService 而不是直接查 users 表：
// 展示信息（昵称、头像、为空时的回退）只有一处口径，各模块都从这里拿。
//
// rdb 只用来缓存两个人的计数（关注数、粉丝数），关系本身始终以 MySQL 为准。
//
// group 是进程内的请求合并器，只服务于统计查询：
// 同一个用户的主页在被反复刷、key 又刚好失效的那一瞬间，会有多个请求同时未命中，
// 合并之后只有一个去查库，其余等它的结果。它是进程级的，
// 多实例部署时每个实例各放一个过去，实例数很少所以可以接受。
type Service struct {
	db    *gorm.DB
	rdb   *redis.Client
	users *user.UserService
	group singleflight.Group
}

// NewService 由 router 在装配期调用一次。
func NewService(db *gorm.DB, rdb *redis.Client, users *user.UserService) *Service {
	return &Service{db: db, rdb: rdb, users: users}
}

// State 是一次关注/取关之后的状态，直接回给前端更新按钮与计数。
// 带上最新粉丝数，省掉前端再发一次请求。
// Changed 表示这次请求是否真的改了关系：重复关注是 false，前端可以据此不弹提示。
type State struct {
	Following     bool
	FollowerCount int64
	Changed       bool
}

// Follow 让 followerID 关注 followeeID，重复关注视为成功（幂等）。
//
// 「重复关注算成功」是刻意的：用户连点两下、网络重试都会发第二次请求，
// 如果第二次回 409，前端还得把它当成功处理，反而增加分支。
func (s *Service) Follow(ctx context.Context, followerID, followeeID uint) (*State, error) {
	if err := validatePair(followerID, followeeID); err != nil {
		return nil, err
	}

	// 先确认目标用户存在，给出准确的 404。
	// 表上没建外键，所以这一句是这个检查的唯一来源。
	if _, err := s.users.GetByID(ctx, followeeID); err != nil {
		return nil, err
	}

	// ON DUPLICATE KEY DO NOTHING：并发重复请求由唯一索引挡住，
	// 谁先插入谁生效，后来的那条静默跳过，不会报错也不会多一行。
	// RowsAffected 为 0 说明这次是重复关注，用它填 Changed 让前端知道关系没变。
	row := model.Follow{FollowerID: followerID, FolloweeID: followeeID}
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return nil, fmt.Errorf("follow user %d: %w", followeeID, res.Error)
	}

	// 写完之后重新数一次，而不是在旧值上 +1：
	// 重复关注没有新增行，凭空 +1 会让前端显示多一个粉丝。
	// 计数走的是数据库，所以这次返回的粉丝数一定是最新的。
	count, err := s.countFollowers(ctx, followeeID)
	if err != nil {
		return nil, err
	}

	// 关系真的变了才需要清缓存：重复关注没有改任何数据，计数也没变，
	// 白删一次只会让下一次读多走一趟数据库。
	// 两个人都要删：关注者少了一个「关注数」，被关注者多了一个「粉丝数」。
	if res.RowsAffected > 0 {
		s.invalidateStats(ctx, followerID, followeeID)
	}
	return &State{Following: true, FollowerCount: count, Changed: res.RowsAffected > 0}, nil
}

// Unfollow 解除关注，本来就没关注时同样返回成功。
func (s *Service) Unfollow(ctx context.Context, followerID, followeeID uint) (*State, error) {
	if err := validatePair(followerID, followeeID); err != nil {
		return nil, err
	}

	res := s.db.WithContext(ctx).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Delete(&model.Follow{})
	if res.Error != nil {
		return nil, fmt.Errorf("unfollow user %d: %w", followeeID, res.Error)
	}

	count, err := s.countFollowers(ctx, followeeID)
	if err != nil {
		return nil, err
	}

	// 同 Follow：只有真删掉了关系才清缓存，本来没关注时计数没变。
	if res.RowsAffected > 0 {
		s.invalidateStats(ctx, followerID, followeeID)
	}
	return &State{Following: false, FollowerCount: count, Changed: res.RowsAffected > 0}, nil
}

// Stats 返回某人的关注数、粉丝数，以及 viewer 是否关注了他。
//
// 返回值用裸类型而不是自定义结构体：user 包要用接口声明这个方法，
// 而 user 包不能 import follow（follow 已经用了 user.Briefs），
// 只有签名全由基本类型组成，接口才能跨包对上。
//
// viewerID 为 0 表示未登录，isFollowing 固定 false，也不发那次查询。
// 主页是公开接口，未登录访问不该因为这一项变成错误。
func (s *Service) Stats(ctx context.Context, userID, viewerID uint) (int64, int64, bool, error) {
	following, followers, err := s.statsCounts(ctx, userID)
	if err != nil {
		return 0, 0, false, err
	}

	if viewerID == 0 || viewerID == userID {
		return following, followers, false, nil
	}

	// isFollowing 不进缓存：它是 (viewer, target) 二元组，
	// 每个访问者都有一份，key 数量会随用户数平方增长，
	// 而这条查询走唯一索引，本来就是一次 O(1) 的行存在性判断。
	var links int64
	err = s.db.WithContext(ctx).Model(&model.Follow{}).
		Where("follower_id = ? AND followee_id = ?", viewerID, userID).
		Count(&links).Error
	if err != nil {
		return 0, 0, false, fmt.Errorf("check follow state: %w", err)
	}
	return following, followers, links > 0, nil
}

// statsCounts 取关注数与粉丝数，优先走缓存，未命中时按 userID 合并回源。
//
// 完整路径：
//  1. 先在锁外读一次缓存，命中就返回（绝大多数请求走这里）
//  2. 未命中才进 singleflight，同一个 userID 只放一个请求去查库
//  3. 进门后二次读缓存，防止排队期间别人已经回填好了
//  4. 查库并回填
//
// 第 2 步是防击穿的关键。主页是被人反复刷的页面，热点用户的 key 一过期，
// 几十个并发会同时未命中；没有合并的话每个请求都要跑两个 COUNT，
// 而这两个 COUNT 要扫完这个人的全部关系行，正是缓存本来要挡掉的那笔开销。
// 缓存没命中会让数据库比不加缓存时更难受，这一层必须补上。
//
// 计数正确性始终以 MySQL 为准：缓存只是它的一个可能稍旧的副本。
func (s *Service) statsCounts(ctx context.Context, userID uint) (int64, int64, error) {
	// 第一遍读缓存放锁外：命中的请求不该为了排队多等一轮。
	snap, hit, _ := s.loadStats(ctx, userID)
	if hit {
		return snap.Following, snap.Followers, nil
	}

	// 未命中的请求在这里合并。key 只用 userID：
	// 计数只跟「这是谁的主页」有关，跟谁在看无关（is_following 不进缓存）。
	v, err, shared := s.group.Do(strconv.FormatUint(uint64(userID), 10), func() (any, error) {
		// 脱离调用方的 ctx：闭包捕获的是第一个请求者的 ctx，
		// 他一取消（关页面、客户端超时），所有等待者都会一起收到 context.Canceled。
		// WithoutCancel 去掉取消信号但保留 ctx 里的值，再套 countTimeout 兜住回源时间。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), countTimeout)
		defer cancel()

		// 二次读缓存：等排队期间，第一个请求可能已经把结果写进去了。
		// 少了这一遍，合并就白做，每个等待者都要自己回源一次。
		//
		// 这次读会再记一次 miss，因此 miss 事件数大于「真正未命中的请求数」。
		// 这是刻意接受的：文章详情那边也是同一套写法，两处口径一致比局部精确更重要，
		// 而看板只关心趋势，两个事件同比例放大不影响判断。
		cached, found, broken := s.loadStats(loadCtx, userID)
		if found {
			return cached, nil
		}

		following, followers, err := s.countFromDB(loadCtx, userID)
		if err != nil {
			return nil, err
		}
		fresh := statsSnapshot{Following: following, Followers: followers}

		// Redis 已经读不通时不再尝试回填，省掉一次注定失败的往返和一条重复日志。
		// 值损坏不算故障（broken=false），必须回填把坏值覆盖掉。
		if !broken {
			s.storeStats(loadCtx, userID, fresh)
		}
		return fresh, nil
	})
	if shared {
		// shared 只表示结果被多个请求领取，不代表成功，所以只计数，不影响返回。
		metrics.FollowStatsCacheEvents.WithLabelValues(cacheEventShare).Inc()
	}
	if err != nil {
		return 0, 0, err
	}

	// 闭包返回的是 any。用带检查的类型断言，
	// 以后改了闭包的返回类型会得到明确错误，而不是 panic。
	fetched, ok := v.(statsSnapshot)
	if !ok {
		return 0, 0, fmt.Errorf("follow stats loader returned unexpected type %T", v)
	}
	return fetched.Following, fetched.Followers, nil
}

// countFromDB 是真正的回源：两次 COUNT。
// 抽成独立方法，因为单飞闭包要用它，而它是「计数口径」的唯一实现。
func (s *Service) countFromDB(ctx context.Context, userID uint) (int64, int64, error) {
	var following int64
	err := s.db.WithContext(ctx).Model(&model.Follow{}).
		Where("follower_id = ?", userID).Count(&following).Error
	if err != nil {
		return 0, 0, fmt.Errorf("count following of %d: %w", userID, err)
	}

	followers, err := s.countFollowers(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	return following, followers, nil
}

// Item 是列表里的一行：展示信息、关注时间、viewer 是否也关注了这个人。
// FollowedByMe 供粉丝列表画「回关」按钮，个人中心的关注列表里都是已关注，值为 true。
type Item struct {
	User         user.Brief
	FollowedAt   time.Time
	FollowedByMe bool
}

// ListFollowing 返回某人的关注列表，按关注时间倒序（新关注的在前）。
func (s *Service) ListFollowing(ctx context.Context, userID, viewerID uint, page, pageSize int) ([]Item, int64, error) {
	return s.list(ctx, userID, viewerID, colFollower, colFollowee, page, pageSize)
}

// ListFollowers 返回某人的粉丝列表，按关注时间倒序。
func (s *Service) ListFollowers(ctx context.Context, userID, viewerID uint, page, pageSize int) ([]Item, int64, error) {
	return s.list(ctx, userID, viewerID, colFollowee, colFollower, page, pageSize)
}

// list 是两条列表接口的共同实现。
// ownerCol 决定「这个列表属于谁」，otherCol 决定「列表里的人是谁」，
// 两种列表的差别只有这两列的互换，其余逻辑完全相同，所以合并成一处。
//
// 固定开销是 4 次查询：确认用户存在、总数、当前页关系、批量取用户信息；
// 若 viewer 已登录再加 1 次批量查关注状态，与页大小无关。
func (s *Service) list(ctx context.Context, ownerID, viewerID uint, ownerCol, otherCol string, page, pageSize int) ([]Item, int64, error) {
	q, err := paging.New(page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	// 用户不存在时返回 404，而不是一个空列表：
	// 前端拿到空列表会以为「这个人没有关注任何人」，其实是页面地址错了。
	if _, err := s.users.GetByID(ctx, ownerID); err != nil {
		return nil, 0, err
	}

	var total int64
	err = s.db.WithContext(ctx).Model(&model.Follow{}).
		Where(ownerCol+" = ?", ownerID).Count(&total).Error
	if err != nil {
		return nil, 0, fmt.Errorf("count follow list: %w", err)
	}

	// 关系行里只取「对方 id」和关注时间，用户信息留给下一步批量查。
	// 排序用 id 而不是 created_at：同一秒内连续关注多个人时，时间戳会重复，
	// id 自增能给出稳定的先后顺序，分页也不会漏行或重行。
	type relationRow struct {
		OtherID   uint
		CreatedAt time.Time
	}
	rows := make([]relationRow, 0, pageSize)
	err = s.db.WithContext(ctx).Model(&model.Follow{}).
		Select(otherCol+" AS other_id", "created_at").
		Where(ownerCol+" = ?", ownerID).
		Order("id DESC").
		Offset(q.Offset()).Limit(q.PageSize).
		Find(&rows).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list follows: %w", err)
	}

	// 收集本页涉及的用户 id，一次 IN 查询拿全部展示信息。
	otherIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		otherIDs = append(otherIDs, row.OtherID)
	}
	briefs, err := s.users.Briefs(ctx, otherIDs)
	if err != nil {
		return nil, 0, err
	}

	// viewer 的关注状态也一次批量取，避免每行查一次。
	followed := make(map[uint]bool, len(otherIDs))
	if viewerID != 0 && len(otherIDs) > 0 {
		var mine []model.Follow
		err = s.db.WithContext(ctx).Select("followee_id").
			Where("follower_id = ? AND followee_id IN ?", viewerID, otherIDs).
			Find(&mine).Error
		if err != nil {
			return nil, 0, fmt.Errorf("check followed state: %w", err)
		}
		for _, m := range mine {
			followed[m.FolloweeID] = true
		}
	}

	// 按关系行的顺序组装：顺序由 SQL 的 ORDER BY 决定，
	// 不能遍历 briefs map，map 的遍历顺序是随机的。
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		brief, ok := briefs[row.OtherID]
		if !ok {
			// 用户已被删但关系行还在：跳过这一行，不要让整页请求失败。
			continue
		}
		items = append(items, Item{
			User:         brief,
			FollowedAt:   row.CreatedAt,
			FollowedByMe: followed[row.OtherID],
		})
	}
	return items, total, nil
}

// countFollowers 数某个用户的粉丝数。
// 认领统计与列表都走这个函数，口径只有一处。
func (s *Service) countFollowers(ctx context.Context, userID uint) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.Follow{}).
		Where("followee_id = ?", userID).Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count followers of %d: %w", userID, err)
	}
	return count, nil
}

// validatePair 挡掉自关注与非法 id。
// 自关注会让「关注数」和「粉丝数」同时 +1，主页上看起来很怪，也会干扰推荐类查询。
// 自关注单独返回 ErrSelfAction，客户端能拿到「不能关注自己」这句准确文案。
func validatePair(followerID, followeeID uint) error {
	if followerID == 0 || followeeID == 0 {
		return fmt.Errorf("%w: user id is required", domain.ErrInvalidInput)
	}
	if followerID == followeeID {
		return fmt.Errorf("%w: follow", domain.ErrSelfAction)
	}
	return nil
}

// 编译期确认 Service 满足 user 包声明的接口。
// 签名一旦改动，编译在 follow 包就失败，不用等到路由装配时才发现类型不匹配。
var _ user.FollowStats = (*Service)(nil)
