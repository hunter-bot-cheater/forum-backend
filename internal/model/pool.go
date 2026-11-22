// Package model 定义 GORM 数据模型。
package model

import "time"

// 对局状态。房间与对局共用一张表：开房时就有 room_code 与 status=waiting，
// 双方准备齐转 playing，结算转 finished，长时间没人来转 expired。
//
// 不把「房间」独立成一张表，是因为房间除了状态与两个人之外没有别的字段，
// 分表后每次读房间都要 join 一次，而它本来就是这条对局的前半生。
const (
	PoolStatusWaiting  = "waiting"  // 房间已建，还没双方就绪
	PoolStatusPlaying  = "playing"  // 对局进行中
	PoolStatusFinished = "finished" // 已结算
	PoolStatusExpired  = "expired"  // 长时间没人来，房间自动解散
)

// PoolMatch 是一局八球对战。
//
// RoomCode 是 6 位数字，房间页把它显示出来，告诉别人就能进同一个房间；
// 唯一索引兜住并发重号。
//
// P0 / P1 分别对应座位 0 与 1：开球方是座位 0，由开房的人占据，
// 这样「谁先开球」不需要额外字段，从房间归属就能推出来。
type PoolMatch struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	RoomCode string `gorm:"size:6;not null;uniqueIndex:uk_pool_room" json:"room_code"`
	P0ID     uint   `gorm:"not null;index:idx_pool_p0,priority:1" json:"p0_id"`
	// P1ID 可空：房间建好时可以只有房主，等对方输房号进来（或接受邀请）再补上。
	// 用 NULL 而不是 0 表示空位——0 不是合法用户，用它会让每个读这行的地方
	// 都得多一条「0 是不是真的没人」的特例。
	P1ID *uint `gorm:"index:idx_pool_p1,priority:1" json:"p1_id"`

	Status string `gorm:"size:16;not null;default:waiting" json:"status"`

	// WinnerID 用指针：0 是合法的「还没人赢」以外的值，但 NULL 才是数据库层面的「没有」。
	// 结算前这一列必须是 NULL，否则「谁赢了」的查询会把所有平局算成 0 号赢。
	WinnerID  *uint      `json:"winner_id"`
	WinReason string     `gorm:"size:32;not null;default:''" json:"win_reason"`
	ShotCount int        `gorm:"not null;default:0" json:"shot_count"`
	StartedAt *time.Time `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// HasPlayer 判断某人是不是这一局的两个座位上的人。
//
// 座位 1 可能还空着（房间里只有房主），所以不能直接比 id——nil 与任何数都不相等，
// 每个调用点自己写一遍很容易漏。
func (m *PoolMatch) HasPlayer(userID uint) bool {
	if userID == 0 {
		return false
	}
	if m.P0ID == userID {
		return true
	}
	return m.P1ID != nil && *m.P1ID == userID
}

// OpponentOf 返回 userID 的对手。座位 1 空着、或 userID 不在这局里时返回 0。
//
// 战绩与回放都用它取「另一个人是谁」：刚建好的房间里没有对手，
// 那些页面本来也不该看到这种行（它们只读 finished）。
func (m *PoolMatch) OpponentOf(userID uint) uint {
	if m.P0ID == userID {
		if m.P1ID != nil {
			return *m.P1ID
		}
		return 0
	}
	if m.P1ID != nil && *m.P1ID == userID {
		return m.P0ID
	}
	return 0
}

// PoolShot 是一杆的完整记录，回放靠它按 seq 顺序重播。
//
// Frames 存 gzip 之后的 JSON，不是设计稿里的 msgpack：
// 一杆 130 帧的 JSON 约 60KB，gzip 之后 8-10KB，比 msgpack 还小一截，
// 而且不用引第三方编解码库。回放是低频操作，解压那几毫秒不值一提。
//
// 唯一索引 (match_id, seq) 是幂等护栏：客户端重发 shoot 时 seq 不变，
// 第二次插入会撞唯一键，而不是在同一局里留下两条一模一样的记录。
type PoolShot struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	MatchID    uint      `gorm:"not null;uniqueIndex:uk_pool_shot_seq,priority:1" json:"match_id"`
	Seq        int       `gorm:"not null;uniqueIndex:uk_pool_shot_seq,priority:2" json:"seq"`
	ShooterID  uint      `gorm:"not null" json:"shooter_id"`
	Seat       int       `gorm:"not null;default:0" json:"seat"`
	Params     []byte    `gorm:"type:json;not null" json:"-"`
	Events     []byte    `gorm:"type:json;not null" json:"-"`
	Frames     []byte    `gorm:"type:mediumblob;not null" json:"-"`
	DurationMS int       `gorm:"not null;default:0" json:"duration_ms"`
	SimMS      int       `gorm:"not null;default:0" json:"sim_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

// PoolRating 是八球战绩与积分。
//
// 单独一张表而不是塞进 users：绝大多数用户一辈子没打过球，
// 往用户表加三列等于每次查用户都带上用不到的数据。
// Elo 初值 1000，赢一局涨、输一局跌，具体算法在 pool/rating.go。
type PoolRating struct {
	UserID    uint      `gorm:"primaryKey;autoIncrement:false" json:"user_id"`
	Elo       int       `gorm:"not null;default:1000" json:"elo"`
	Wins      int       `gorm:"not null;default:0" json:"wins"`
	Losses    int       `gorm:"not null;default:0" json:"losses"`
	UpdatedAt time.Time `json:"updated_at"`
}
