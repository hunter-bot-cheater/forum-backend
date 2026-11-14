package floor

import "time"

// 本文件存放抢楼模块的请求体与对外响应结构体。
// 请求体只收必要的字段，用户 id 一律从 JWT 取，不接受客户端传。
// 时间用 time.Time：Gin 的 ShouldBindJSON 会按 RFC3339 解析，
// 格式不对直接 400，不用自己写时间戳转换。

// createEventReq 是创建抢楼活动的请求体。
type createEventReq struct {
	ArticleID   uint      `json:"article_id"`
	Title       string    `json:"title"`
	LuckyFloors []int     `json:"lucky_floors"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
}

// grabReq 是抢一次楼的请求体。楼层号由服务端发，客户端不能指定。
type grabReq struct {
	Content string `json:"content"`
}

// winnerResp 是一条中奖记录对外的形状。
type winnerResp struct {
	Floor     int       `json:"floor"`
	UserID    uint      `json:"user_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// eventResp 是活动详情对外的形状。
// NextFloor 是发号器当前发出的最后一个号码，等于「已经有多少人抢过」。
type eventResp struct {
	ID          uint          `json:"id"`
	ArticleID   uint          `json:"article_id"`
	Title       string        `json:"title"`
	LuckyFloors []int         `json:"lucky_floors"`
	StartsAt    time.Time     `json:"starts_at"`
	EndsAt      time.Time     `json:"ends_at"`
	NextFloor   int64         `json:"next_floor"`
	Winners     []winnerResp  `json:"winners"`
}

// grabResp 是一次抢楼的结果。
// Won 只有两个来源：楼层号命中中奖集合，且 INSERT 成功。
type grabResp struct {
	Floor   int    `json:"floor"`
	Won     bool   `json:"won"`
	Message string `json:"message"`
}
