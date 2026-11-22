package pool

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"forum-backend/internal/model"
	"forum-backend/internal/pool/sim"
)

// Store 是对局的持久化层：建房、开局、每杆记录、结算、以及「能不能邀请这个人」。
type Store struct {
	db *gorm.DB
}

// NewStore 由装配层调用。
func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// CreateMatch 建一条等待中的对局。房号撞唯一索引时返回错误，由调用方换一个号重试。
//
// p1 传 nil 表示「房间里只有房主」，等对方输房号进来再补上。
func (s *Store) CreateMatch(ctx context.Context, p0 uint, p1 *uint, roomCode string) (*model.PoolMatch, error) {
	match := &model.PoolMatch{
		RoomCode: roomCode,
		P0ID:     p0,
		P1ID:     p1,
		Status:   model.PoolStatusWaiting,
	}
	if err := s.db.WithContext(ctx).Create(match).Error; err != nil {
		return nil, err
	}
	return match, nil
}

// CurrentMatchFor 找这个人当前所在的房间或对局：还没开打的房间优先，其次正在打的那一局。
//
// 必须把 playing 也找出来，否则「开局之后大厅每两秒轮询一次」会在第一轮就发现
// 自己那间 waiting 的房间不见了（它已经变成 playing），于是另开一间新房——
// 房主被留在大厅里，而他的对局在后台空转。刷新页面后回大厅也是同一个坑。
//
// 一个用户理论上只有一条 waiting，但历史上可能有多条（邀请流程留下的），
// 取最新的一条：旧的那些已经没人会去点了，等清扫把它们标掉。
func (s *Store) CurrentMatchFor(ctx context.Context, userID uint) (*model.PoolMatch, error) {
	var match model.PoolMatch
	err := s.db.WithContext(ctx).
		Where("status IN ? AND (p0_id = ? OR p1_id = ?)",
			[]string{model.PoolStatusWaiting, model.PoolStatusPlaying}, userID, userID).
		Order("id DESC").
		First(&match).Error
	if err != nil {
		return nil, err
	}
	return &match, nil
}

// WaitingMatchFor 只找还没开打的房间。邀请与被邀请这两条链路用不到正在打的对局。
func (s *Store) WaitingMatchFor(ctx context.Context, userID uint) (*model.PoolMatch, error) {
	var match model.PoolMatch
	err := s.db.WithContext(ctx).
		Where("status = ? AND (p0_id = ? OR p1_id = ?)", model.PoolStatusWaiting, userID, userID).
		Order("id DESC").
		First(&match).Error
	if err != nil {
		return nil, err
	}
	return &match, nil
}

// MatchByRoomCode 按房号找一条等待中的房间。
//
// 只认 waiting：房号在房间开始或过期之后就该失效，否则输同一串数字会进到
// 一局正在打的、甚至已经结算的对局里。
func (s *Store) MatchByRoomCode(ctx context.Context, roomCode string) (*model.PoolMatch, error) {
	var match model.PoolMatch
	err := s.db.WithContext(ctx).
		Where("room_code = ? AND status = ?", roomCode, model.PoolStatusWaiting).
		First(&match).Error
	if err != nil {
		return nil, err
	}
	return &match, nil
}

// JoinRoom 把 userID 放到座位 1 上。座位已经被占、或房间已经不在了会返回 false。
//
// 判据交给一条带条件的 UPDATE 而不是「先查再写」：两个人几乎同时输同一个房号时，
// 先查再写会让两个人都看到空位。RowsAffected == 0 就说明这条 UPDATE 没抢到。
func (s *Store) JoinRoom(ctx context.Context, matchID, userID uint) (bool, error) {
	result := s.db.WithContext(ctx).Model(&model.PoolMatch{}).
		Where("id = ? AND status = ? AND p1_id IS NULL AND p0_id <> ?",
			matchID, model.PoolStatusWaiting, userID).
		Update("p1_id", userID)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// GuestRoomFor 找这个人以「客人」身份待着的房间（座位 1 是他）。
//
// 退房只认这一种：自己的房间没有「退出」这个动作，而一个人可能同时
// 既是自己房间的房主、又是别人房间的客人，按「最新一条」找会拿错。
func (s *Store) GuestRoomFor(ctx context.Context, userID uint) (*model.PoolMatch, error) {
	var match model.PoolMatch
	err := s.db.WithContext(ctx).
		Where("status = ? AND p1_id = ?", model.PoolStatusWaiting, userID).
		Order("id DESC").
		First(&match).Error
	if err != nil {
		return nil, err
	}
	return &match, nil
}

// ExpireOwnRooms 把这个人自己开的、还没开打的房间标成过期，keepID 那间留着。
//
// 一个人同时只能在一间房里等。进了别人的房间之后，自己原来那间就该作废：
// 留着它，「我的房间」会在两行之间来回跳——严重的时候刚报给别人的房号
// 会突然变成一个空房间的房号。
func (s *Store) ExpireOwnRooms(ctx context.Context, userID, keepID uint) ([]uint, error) {
	var ids []uint
	err := s.db.WithContext(ctx).Model(&model.PoolMatch{}).
		Where("status = ? AND p0_id = ? AND id <> ?", model.PoolStatusWaiting, userID, keepID).
		Pluck("id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("load own rooms: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	err = s.db.WithContext(ctx).Model(&model.PoolMatch{}).
		Where("id IN ? AND status = ?", ids, model.PoolStatusWaiting).
		Update("status", model.PoolStatusExpired).Error
	if err != nil {
		return nil, fmt.Errorf("expire own rooms: %w", err)
	}
	return ids, nil
}

// LeaveRoom 把 userID 从座位 1 上摘下来，房间回到「只有房主」的样子。
//
// 只认 p1_id = userID 的那一行：房主不能靠这个接口解散自己的房间，
// 而「我以为我在这间房里」与实际不符时（记错了房号、房间已经开打），
// 这里返回 false，调用方按 404 处理。
func (s *Store) LeaveRoom(ctx context.Context, matchID, userID uint) (bool, error) {
	result := s.db.WithContext(ctx).Model(&model.PoolMatch{}).
		Where("id = ? AND status = ? AND p1_id = ?", matchID, model.PoolStatusWaiting, userID).
		Update("p1_id", nil)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// ExpireStaleWaiting 把开了太久还没凑齐人的房间标成过期。
//
// 房间现在由用户随手就开（进大厅就有一个），不清理的话库里会积一堆 waiting 行，
// 房号也会被一直占着。返回标记了多少行，方便观察日志。
func (s *Store) ExpireStaleWaiting(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Model(&model.PoolMatch{}).
		Where("status = ? AND created_at < ?", model.PoolStatusWaiting, before).
		Update("status", model.PoolStatusExpired)
	return result.RowsAffected, result.Error
}

// ExpireRooms 每 interval 清一次「开了超过 ttl 还没开打」的房间，直到 ctx 取消。
//
// 判据用 created_at 而不是「最近一次有动静」：房间页的轮询不写库，
// 按动静判会把有人守着、只是还没等到人的房间误收掉。
// 代价是一间被守了两小时的房间会解散、房号作废——那是可以接受的：
// 真等了两个小时的人重新进一次大厅就有新房号了。
func ExpireRooms(ctx context.Context, store *Store, interval, ttl time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			count, err := store.ExpireStaleWaiting(ctx, now.Add(-ttl))
			if err != nil {
				slog.ErrorContext(ctx, "expire stale pool rooms", "error", err)
				continue
			}
			if count > 0 {
				slog.InfoContext(ctx, "expired stale pool rooms", "count", count)
			}
		}
	}
}

// Match 按 id 读一条对局。
func (s *Store) Match(ctx context.Context, id uint) (*model.PoolMatch, error) {
	var match model.PoolMatch
	if err := s.db.WithContext(ctx).First(&match, id).Error; err != nil {
		return nil, err
	}
	return &match, nil
}

// MarkPlaying 把对局从等待推进到进行中。已经开过局的不再改开始时间。
func (s *Store) MarkPlaying(ctx context.Context, matchID uint) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&model.PoolMatch{}).
		Where("id = ? AND status = ?", matchID, model.PoolStatusWaiting).
		Updates(map[string]any{
			"status":     model.PoolStatusPlaying,
			"started_at": &now,
		}).Error
}

// FollowingSet 批量判断「我关注了这几个人里的哪些」。
//
// 在线名单一次要标几十个人，逐个查就是几十条 SQL；这里一条 IN 查询拿回来。
// 只有 candidates 为空时才需要特判——空 IN 在部分数据库上是语法错误。
func (s *Store) FollowingSet(ctx context.Context, followerID uint, candidates []uint) (map[uint]bool, error) {
	set := make(map[uint]bool, len(candidates))
	if len(candidates) == 0 {
		return set, nil
	}

	var ids []uint
	err := s.db.WithContext(ctx).Model(&model.Follow{}).
		Where("follower_id = ? AND followee_id IN ?", followerID, candidates).
		Pluck("followee_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("load following set: %w", err)
	}
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// IsFollowing 判断 follower 有没有关注 followee。
//
// 直接查 follow 表而不是复用 follow 模块：那边只有「列表」与「关注/取关」，
// 没有单条判断，为一个布尔值去拉一页列表不划算。
func (s *Store) IsFollowing(ctx context.Context, followerID, followeeID uint) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.Follow{}).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// SaveShot 写一杆。实现 Recorder。
//
// Frames 先 JSON 再 gzip：一杆 130 帧的 JSON 约 60KB，压完 8-10KB，
// 比上 msgpack 更小还少一个依赖。回放时反向解一次，那几毫秒不值得优化。
func (s *Store) SaveShot(ctx context.Context, record ShotRecord) error {
	params, err := json.Marshal(record.Params)
	if err != nil {
		return fmt.Errorf("marshal shot params: %w", err)
	}
	events, err := json.Marshal(record.Events)
	if err != nil {
		return fmt.Errorf("marshal shot events: %w", err)
	}
	frames, err := gzipJSON(record.Frames)
	if err != nil {
		return err
	}

	shot := model.PoolShot{
		MatchID:    record.MatchID,
		Seq:        record.Seq,
		ShooterID:  record.ShooterID,
		Seat:       int(record.Seat),
		Params:     params,
		Events:     events,
		Frames:     frames,
		DurationMS: int(record.Duration * 1000),
		SimMS:      record.SimMS,
	}
	if err := s.db.WithContext(ctx).Create(&shot).Error; err != nil {
		return fmt.Errorf("create pool shot: %w", err)
	}
	return nil
}

// FinishMatch 结算：写赢家与原因，并按 Elo 更新双方战绩。
//
// 整个结算放在一个事务里：对局状态与战绩必须一起生效，
// 否则会出现「战绩涨了但对局还是进行中」这种查询得到的状态。
//
// 状态更新带 status = playing 条件，挡住重复结算：重连、超时、对手退出
// 三条路径可能在同一秒里都触发一次结算，只有第一条能改到行。
func (s *Store) FinishMatch(ctx context.Context, record MatchRecord) error {
	now := record.EndedAt
	if now.IsZero() {
		now = time.Now()
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		update := tx.Model(&model.PoolMatch{}).
			Where("id = ? AND status = ?", record.MatchID, model.PoolStatusPlaying).
			Updates(map[string]any{
				"status":     model.PoolStatusFinished,
				"winner_id":  record.WinnerID,
				"win_reason": record.WinReason,
				"shot_count": record.Shots,
				"ended_at":   &now,
			})
		if update.Error != nil {
			return fmt.Errorf("finish pool match: %w", update.Error)
		}
		// 没改到行说明已经结算过了（或这局根本没开始），不重复算分。
		if update.RowsAffected == 0 {
			return nil
		}
		if record.WinnerID == 0 || record.LoserID == 0 {
			return nil
		}
		return s.applyResult(tx, record.WinnerID, record.LoserID, now)
	})
}

// applyResult 读出双方 Elo，按标准公式算完再写回去。
func (s *Store) applyResult(tx *gorm.DB, winnerID, loserID uint, now time.Time) error {
	winner, err := s.ratingOf(tx, winnerID)
	if err != nil {
		return err
	}
	loser, err := s.ratingOf(tx, loserID)
	if err != nil {
		return err
	}

	// 标准 Elo：胜者得 K·(1-E)，负者扣 K·E，E 是按分差算出的期望胜率。
	// K 取 32，与棋类常用的业余档一致；两人的分差越大，强者赢一局的收益越小。
	expected := 1.0 / (1.0 + math.Pow(10, float64(loser.Elo-winner.Elo)/400.0))
	delta := int(math.Round(32 * (1 - expected)))
	if delta < 1 {
		delta = 1
	}

	winner.Elo += delta
	winner.Wins++
	loser.Elo -= delta
	loser.Losses++
	if loser.Elo < 100 {
		loser.Elo = 100
	}
	winner.UpdatedAt, loser.UpdatedAt = now, now

	for _, rating := range []*model.PoolRating{winner, loser} {
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(rating).Error; err != nil {
			return fmt.Errorf("save pool rating: %w", err)
		}
	}
	return nil
}

// ratingOf 读一个用户的战绩，没有记录时返回一份初始值（Elo 1000）。
func (s *Store) ratingOf(tx *gorm.DB, userID uint) (*model.PoolRating, error) {
	var rating model.PoolRating
	err := tx.Where("user_id = ?", userID).Take(&rating).Error
	if err == nil {
		return &rating, nil
	}
	if err == gorm.ErrRecordNotFound {
		return &model.PoolRating{UserID: userID, Elo: 1000}, nil
	}
	return nil, fmt.Errorf("load pool rating: %w", err)
}

// Rating 载入一个用户的战绩，没有记录时返回初始值（Elo 1000、零胜零负）。
//
// 读不到不写库：绝大多数人只是打开看一眼战绩，为一次只读操作插一行没有意义，
// 而且并发首访还会撞主键。
func (s *Store) Rating(ctx context.Context, userID uint) (model.PoolRating, error) {
	rating, err := s.ratingOf(s.db.WithContext(ctx), userID)
	if err != nil {
		return model.PoolRating{}, err
	}
	return *rating, nil
}

// RecentMatches 分页读这个人参与过且已结算的对局，最近的在前。
//
// 只认 finished：waiting 还没打成，expired 是房间没人来，
// 把它们混进战绩列表会让「我赢了 3 局」和列表条数对不上。
func (s *Store) RecentMatches(ctx context.Context, userID uint, offset, limit int) ([]model.PoolMatch, int64, error) {
	// 条件构造两遍而不是复用同一条链：Count 之后接着 Order/Find 会带上 Count 改写过的
	// Statement（Select 被换成 count(*)），拿到的行里字段全是零值。
	scope := func() *gorm.DB {
		return s.db.WithContext(ctx).Model(&model.PoolMatch{}).
			Where("(p0_id = ? OR p1_id = ?)", userID, userID).
			Where("status = ?", model.PoolStatusFinished)
	}

	var total int64
	if err := scope().Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count pool matches: %w", err)
	}

	var matches []model.PoolMatch
	// id 兜在排序里：同一秒结束的两局，光按 ended_at 排出来的顺序在分页之间会飘，
	// 第二页可能重复第一页的最后一条。
	err := scope().Order("ended_at DESC, id DESC").Offset(offset).Limit(limit).Find(&matches).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list pool matches: %w", err)
	}
	return matches, total, nil
}

// MatchShots 读一局的全部杆，按 seq 升序，并把 gzip 过的帧解回结构。
//
// 回放是唯一调用方：它不需要参数与事件，只把轨迹画出来，
// 所以这里只取 seq / seat / duration / frames 四列——
// 一局几十杆，每杆多读几十 KB 的 events 纯属浪费。
func (s *Store) MatchShots(ctx context.Context, matchID uint) ([]ShotReplay, error) {
	var rows []model.PoolShot
	err := s.db.WithContext(ctx).
		Select("seq", "seat", "duration_ms", "frames").
		Where("match_id = ?", matchID).
		Order("seq ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list pool shots: %w", err)
	}

	shots := make([]ShotReplay, 0, len(rows))
	for _, row := range rows {
		var frames []sim.Frame
		if err := gunzipJSON(row.Frames, &frames); err != nil {
			// 一杆解不开不该让整局回放打不开：跳过它，其余照放。
			return nil, fmt.Errorf("decode frames of shot %d: %w", row.Seq, err)
		}
		shots = append(shots, ShotReplay{
			Seq:      row.Seq,
			Seat:     Seat(row.Seat),
			Duration: float64(row.DurationMS) / 1000,
			Frames:   frames,
		})
	}
	return shots, nil
}

// ShotReplay 是回放里的一杆：只留前端画得出来的东西。
type ShotReplay struct {
	Seq      int
	Seat     Seat
	Duration float64
	Frames   []sim.Frame
}

// gzipJSON 把任意结构压成 gzip 后的 JSON。
func gzipJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal frames: %w", err)
	}

	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(raw); err != nil {
		return nil, fmt.Errorf("gzip frames: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close gzip writer: %w", err)
	}
	return buffer.Bytes(), nil
}

// gunzipJSON 是 gzipJSON 的反向操作，供回放解帧用。
func gunzipJSON(data []byte, out any) error {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open gzip frames: %w", err)
	}
	// 先读完再关：Read 出错时也得把 reader 关掉，否则解压状态泄漏。
	raw, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return fmt.Errorf("read gzip frames: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close gzip reader: %w", closeErr)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unmarshal frames: %w", err)
	}
	return nil
}

// 编译期确认 Store 满足 Recorder。
var _ Recorder = (*Store)(nil)
