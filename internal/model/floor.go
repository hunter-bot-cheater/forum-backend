package model

import "time"

// FloorEvent 是一场抢楼活动。
//
// 活动挂在某篇文章下，但不建 article 外键：活动是一份独立配置，
// 文章被删之后这份配置仍要留着给人复盘，所以 ArticleID 只是个普通列。
//
// LuckyFloors 用逗号分隔的字符串存中奖楼层，比如 "8,88,888"。
// 没单独建一张中奖楼层表，是因为这个集合创建后就不再变，
// 每次抢楼都要读，拆成关联表反而多一次 JOIN。解析规则见 floor 包。
type FloorEvent struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ArticleID   uint      `gorm:"index;not null" json:"article_id"`
	Title       string    `gorm:"size:128;not null" json:"title"`
	LuckyFloors string    `gorm:"type:varchar(512);not null" json:"lucky_floors"`
	StartsAt    time.Time `gorm:"not null" json:"starts_at"`
	EndsAt      time.Time `gorm:"not null" json:"ends_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// FloorPrize 是一个中奖名额，活动创建时就把每个中奖楼层写成一行。
//
// WinnerID 为 NULL 表示名额还没人领取。抢的动作是一条
// UPDATE ... SET winner_id=? WHERE event_id=? AND floor=? AND winner_id IS NULL，
// 判定和领取在同一次数据库写入里完成，RowsAffected 为 1 才算抢到。
// 并发时 InnoDB 对这行加排他锁，第二个事务在当前读下重新检查条件，
// 发现 winner_id 已经不是 NULL，RowsAffected 变成 0，奖仍然只发一份。
//
// uk_floor_prize_event_floor 保证一个楼层只有一行名额。
// 有了预置行，扫「winner_id IS NULL 且楼层号已经被发过」就能找出作废名额，
// 这是奖品池模型比 INSERT 方案更容易做对账的地方。
type FloorPrize struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	EventID   uint       `gorm:"uniqueIndex:uk_floor_prize_event_floor,priority:1;not null" json:"event_id"`
	Floor     int        `gorm:"uniqueIndex:uk_floor_prize_event_floor,priority:2;not null" json:"floor"`
	WinnerID  *uint      `gorm:"index" json:"winner_id"`
	Content   string     `gorm:"type:text" json:"content"`
	WonAt     *time.Time `json:"won_at"`
	CreatedAt time.Time  `json:"created_at"`
}
