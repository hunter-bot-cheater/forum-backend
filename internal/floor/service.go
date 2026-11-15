// Package floor 实现抢楼活动：原子发号、中奖判定、零超发的归属落库。
//
// 这个模块刻意不碰 internal/comment：普通评论是「先有内容再排队」，
// 抢楼是「先抢到稀缺名额再写内容」，两者的并发模型完全不同。
package floor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/keys"
	"forum-backend/internal/model"
)

const (
	// 中奖楼层不能无限大，否则运营手滑填一个 999999999，活动永远开不出奖。
	maxLuckyFloor = 1000000
	// 一场活动最多设 20 个中奖楼层，避免解析与展示被超长配置拖慢。
	maxLuckyCount = 20
	// 抢楼时带的一句话比评论短，够用就行。
	maxGrabContent = 500
)

// ---------- 纯函数：不碰数据库和 Redis，可以单独单测 ----------

// parseLuckyFloors 把 "8,88,888" 解析成中奖楼层列表。
// 存的时候已经规范化过，这里仍然逐个校验，是为了防住手工改库的脏数据。
// 返回的错误带 domain.ErrInvalidInput，调用方直接往上抛即可。
func parseLuckyFloors(raw string) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: lucky floors is empty", domain.ErrInvalidInput)
	}

	// 按逗号切成若干段，再逐段 trim 后转数字，容忍 "8, 88" 这种带空格的写法。
	parts := strings.Split(raw, ",")
	floors := make([]int, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(part)
		if text == "" {
			return nil, fmt.Errorf("%w: empty lucky floor segment", domain.ErrInvalidInput)
		}
		n, err := strconv.Atoi(text)
		if err != nil {
			return nil, fmt.Errorf("%w: bad lucky floor %q", domain.ErrInvalidInput, text)
		}
		if n <= 0 || n > maxLuckyFloor {
			return nil, fmt.Errorf("%w: lucky floor %d out of range", domain.ErrInvalidInput, n)
		}
		floors = append(floors, n)
	}
	return floors, nil
}

// joinLuckyFloors 校验、排序、去重，再拼成存库用的字符串。
// 排序让 "88,8" 和 "8,88" 落库后完全一致，展示与测试都不会因输入顺序抖动。
func joinLuckyFloors(floors []int) (string, error) {
	if len(floors) == 0 {
		return "", fmt.Errorf("%w: lucky floors is required", domain.ErrInvalidInput)
	}
	if len(floors) > maxLuckyCount {
		return "", fmt.Errorf("%w: too many lucky floors", domain.ErrInvalidInput)
	}

	seen := make(map[int]bool, len(floors))
	unique := make([]int, 0, len(floors))
	for _, n := range floors {
		if n <= 0 || n > maxLuckyFloor {
			return "", fmt.Errorf("%w: lucky floor %d out of range", domain.ErrInvalidInput, n)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		unique = append(unique, n)
	}
	sort.Ints(unique)

	texts := make([]string, 0, len(unique))
	for _, n := range unique {
		texts = append(texts, strconv.Itoa(n))
	}
	return strings.Join(texts, ","), nil
}

// containsFloor 判断这次发到的号是不是中奖楼层。
// 用显式循环而不是 map，是因为中奖楼层最多 20 个，
// 线性扫描比每次抢楼都建一次 map 更省，也更好读。
func containsFloor(floors []int, target int) bool {
	for _, n := range floors {
		if n == target {
			return true
		}
	}
	return false
}

// checkWindow 判断活动是否正在进行。
// 这是应用层快速失败，不是强一致保证：检查通过之后活动仍可能被改掉。
// 真正的归属判定在数据库的条件 UPDATE 那里，见 Grab 的第 5 步。
func checkWindow(now, startsAt, endsAt time.Time) error {
	if now.Before(startsAt) {
		return fmt.Errorf("%w: event has not started", domain.ErrFloorNotActive)
	}
	if !now.Before(endsAt) {
		return fmt.Errorf("%w: event has ended", domain.ErrFloorNotActive)
	}
	return nil
}

// judgePrizeUpdate 把条件 UPDATE 的 RowsAffected 翻译成业务结果。
// 1 表示这一楼的名额被当前请求领到；
// 0 表示 WHERE 没匹配上，名额已被别人领走，或者预置行缺失；
// 大于 1 属于异常，(event_id, floor) 唯一索引保证一个楼层只有一行。
func judgePrizeUpdate(rows int64) error {
	if rows == 1 {
		return nil
	}
	if rows == 0 {
		return fmt.Errorf("%w: prize row is not claimable", domain.ErrFloorTaken)
	}
	return fmt.Errorf("claim floor prize: unexpected rows affected %d", rows)
}

// toWinnerResp 把已领取的奖品行转成对外形状。
// 对外字段仍叫 created_at，为了不让前端改解析，这里填 WonAt（真正中奖的时间），
// 奖品行自己的 CreatedAt 是活动创建时间，不该当中奖时间用。
func toWinnerResp(p *model.FloorPrize) winnerResp {
	resp := winnerResp{
		Floor:   p.Floor,
		Content: p.Content,
	}
	if p.WinnerID != nil {
		resp.UserID = *p.WinnerID
	}
	if p.WonAt != nil {
		resp.CreatedAt = *p.WonAt
	}
	return resp
}

// ---------- Service ----------

// Service 抢楼业务逻辑。db 存活动与奖品行，rdb 存发号计数器。
type Service struct {
	db  *gorm.DB
	rdb *redis.Client
}

// NewService 由装配层在启动时调用一次。
func NewService(db *gorm.DB, rdb *redis.Client) *Service {
	return &Service{db: db, rdb: rdb}
}

// Create 创建一场抢楼活动，同时把每个中奖楼层预置成 floor_prizes 的一行名额。
//
// 中奖楼层在创建时就固定下来，之后不许改，否则先抢的人和后抢的人面对的
// 不是同一份奖品。活动和奖品行必须在同一个事务里写，否则会出现「活动已存在
// 但奖品行缺失」的活动，抢到中奖号也领不到奖。
func (s *Service) Create(ctx context.Context, req createEventReq) (*model.FloorEvent, error) {
	if req.ArticleID == 0 {
		return nil, fmt.Errorf("%w: article_id is required", domain.ErrInvalidInput)
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || len(title) > 128 {
		return nil, fmt.Errorf("%w: title length invalid", domain.ErrInvalidInput)
	}
	if req.StartsAt.IsZero() || req.EndsAt.IsZero() {
		return nil, fmt.Errorf("%w: starts_at and ends_at are required", domain.ErrInvalidInput)
	}
	if !req.StartsAt.Before(req.EndsAt) {
		return nil, fmt.Errorf("%w: starts_at must be before ends_at", domain.ErrInvalidInput)
	}
	luckyText, err := joinLuckyFloors(req.LuckyFloors)
	if err != nil {
		return nil, err
	}
	// 规范化之后再解析回来，拿到排序去重后的楼层列表，用来预置奖品行。
	lucky, err := parseLuckyFloors(luckyText)
	if err != nil {
		return nil, err
	}

	ev := &model.FloorEvent{
		ArticleID:   req.ArticleID,
		Title:       title,
		LuckyFloors: luckyText,
		StartsAt:    req.StartsAt,
		EndsAt:      req.EndsAt,
	}

	// 活动行和奖品行必须一起成功：要么都有，要么都没有。
	// 少了奖品行，用户抢到中奖号也没行可改，等于这个名额永远发不出去。
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ev).Error; err != nil {
			return err
		}
		// 每个中奖楼层预置一行名额，winner_id 初始为 NULL。
		for i := 0; i < len(lucky); i++ {
			prize := &model.FloorPrize{
				EventID: ev.ID,
				Floor:   lucky[i],
			}
			if err := tx.Create(prize).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create floor event with prizes: %w", err)
	}
	return ev, nil
}

// Detail 返回活动详情、当前发到几楼、以及中奖名单。
// 发号器是 Redis 里的一个普通计数器，活动创建时并不存在，
// 所以 Get 报 redis.Nil 是正常情况，按 0 处理。
func (s *Service) Detail(ctx context.Context, eventID uint) (*eventResp, error) {
	var ev model.FloorEvent
	if err := s.db.WithContext(ctx).First(&ev, eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("load floor event %d: %w", eventID, domain.ErrNotFound)
		}
		return nil, fmt.Errorf("load floor event %d: %w", eventID, err)
	}

	lucky, err := parseLuckyFloors(ev.LuckyFloors)
	if err != nil {
		return nil, err
	}

	next, err := s.rdb.Get(ctx, keys.FloorNextKey(eventID)).Int64()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("read floor counter: %w", err)
		}
		next = 0
	}

	// 只返回已经领取的名额，未领取的行不对外暴露。
	var rows []model.FloorPrize
	if err := s.db.WithContext(ctx).
		Where("event_id = ? AND winner_id IS NOT NULL", eventID).
		Order("floor ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list claimed floor prizes: %w", err)
	}
	winners := make([]winnerResp, 0, len(rows))
	for i := 0; i < len(rows); i++ {
		winners = append(winners, toWinnerResp(&rows[i]))
	}

	resp := &eventResp{
		ID:          ev.ID,
		ArticleID:   ev.ArticleID,
		Title:       ev.Title,
		LuckyFloors: lucky,
		StartsAt:    ev.StartsAt,
		EndsAt:      ev.EndsAt,
		NextFloor:   next,
		Winners:     winners,
	}
	return resp, nil
}

// Grab 抢一次楼。整个模块最关键的方法，五步各自解决一件事：
//
//  1. 读活动配置（中奖楼层集合）。
//  2. 校验时间窗，快速失败，别让已结束的活动白白消耗一个号。
//  3. Redis INCR 原子发号。Redis 单线程执行命令，N 个人并发也各拿不同的号。
//     这里绝不能用「SELECT COUNT(*) 再 +1」：两个请求会读到同一个 count，
//     然后各自 +1 写出同一个楼层号。
//  4. 判断这个号是否命中中奖楼层，没命中就直接返回，不写库。
//  5. 命中就对 floor_prizes 里那一行做条件 UPDATE，WHERE 带 winner_id IS NULL。
//     InnoDB 给这行加排他锁，并发的第二个事务在当前读下重新检查条件，
//     发现名额已被领走，RowsAffected 变成 0，奖仍然只发一份。
//     应用层不做事先查询，判定和领取在同一次写入里完成。
//
// 已知取舍：INCR 在 MySQL 之外，UPDATE 失败时号已经发出去了，不会回滚，
// 于是这个名额会空着（奖品行的 winner_id 仍是 NULL）。奖品行还在，扫
// 「楼层号已发过但 winner_id IS NULL」就能发现作废名额，这是改用奖品池之后
// 新增的可观测能力，自动补发留待后续。
// Redis 计数器被清空后会重新从 1 发号，条件 UPDATE 照样拦住重复领取，
// 用户看到 409，奖不会超发。
func (s *Service) Grab(ctx context.Context, eventID, userID uint, content string) (*grabResp, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("%w: content is required", domain.ErrInvalidInput)
	}
	if len(content) > maxGrabContent {
		return nil, fmt.Errorf("%w: content too long", domain.ErrInvalidInput)
	}

	// 第 1 步：读活动。
	var ev model.FloorEvent
	if err := s.db.WithContext(ctx).First(&ev, eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("load floor event %d: %w", eventID, domain.ErrNotFound)
		}
		return nil, fmt.Errorf("load floor event %d: %w", eventID, err)
	}

	// 第 2 步：时间窗。
	if err := checkWindow(time.Now(), ev.StartsAt, ev.EndsAt); err != nil {
		return nil, err
	}

	// 第 3 步：原子发号。
	n, err := s.rdb.Incr(ctx, keys.FloorNextKey(eventID)).Result()
	if err != nil {
		return nil, fmt.Errorf("incr floor counter for event %d: %w", eventID, err)
	}
	floorNo := int(n)

	// 第 4 步：是否中奖。
	lucky, err := parseLuckyFloors(ev.LuckyFloors)
	if err != nil {
		return nil, err
	}
	if !containsFloor(lucky, floorNo) {
		return &grabResp{Floor: floorNo, Won: false, Message: "没中奖，下一楼见"}, nil
	}

	// 第 5 步：条件 UPDATE 领取预置名额，靠 RowsAffected 判定谁抢到。
	now := time.Now()
	result := s.db.WithContext(ctx).
		Model(&model.FloorPrize{}).
		Where("event_id = ? AND floor = ? AND winner_id IS NULL", eventID, floorNo).
		Updates(map[string]interface{}{
			"winner_id": userID,
			"content":   content,
			"won_at":    now,
		})
	if result.Error != nil {
		return nil, fmt.Errorf("claim floor prize: %w", result.Error)
	}
	if err := judgePrizeUpdate(result.RowsAffected); err != nil {
		return nil, err
	}
	return &grabResp{Floor: floorNo, Won: true, Message: "恭喜抢到中奖楼层"}, nil
}

// Mine 返回当前用户在这场活动里领到的中奖楼层。
func (s *Service) Mine(ctx context.Context, eventID, userID uint) ([]winnerResp, error) {
	var rows []model.FloorPrize
	if err := s.db.WithContext(ctx).
		Where("event_id = ? AND winner_id = ?", eventID, userID).
		Order("floor ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list my floor prizes: %w", err)
	}
	list := make([]winnerResp, 0, len(rows))
	for i := 0; i < len(rows); i++ {
		list = append(list, toWinnerResp(&rows[i]))
	}
	return list, nil
}
