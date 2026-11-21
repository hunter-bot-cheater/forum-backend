package pool

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"forum-backend/internal/model"
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
func (s *Store) CreateMatch(ctx context.Context, p0, p1 uint, roomCode string) (*model.PoolMatch, error) {
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

// ExpireMatch 把一条没人进的房间标成过期。房号随之作废，可以被重新生成。
func (s *Store) ExpireMatch(ctx context.Context, matchID uint) error {
	return s.db.WithContext(ctx).Model(&model.PoolMatch{}).
		Where("id = ? AND status = ?", matchID, model.PoolStatusWaiting).
		Update("status", model.PoolStatusExpired).Error
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

// 编译期确认 Store 满足 Recorder。
var _ Recorder = (*Store)(nil)
