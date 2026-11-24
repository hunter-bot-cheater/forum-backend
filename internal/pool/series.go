package pool

import (
	"fmt"
	"sync"
	"time"
)

// SeriesTTL 是一对玩家的连续对局比分保留多久。
//
// 连续约战之间只隔几分钟，但「打完一局回大厅喝了杯水再约上」也该接着算，
// 所以给到三小时；再久的就不算同一轮了，从 0:0 重新开始比接着 7:5 打更清楚。
const SeriesTTL = 3 * time.Hour

// Series 记同一对玩家「这一轮各赢了几局」，进程内唯一。
//
// 为什么要有它：每次再约都是新的一行 pool_matches，单看当前这一局，
// 界面只知道「本局 0:0」——两个人连着打了五局，顶上还是 0:0，
// 看不出这是 3:2 的第五局还是刚开打。
//
// 按「一对玩家」记账，与座位无关：同一对人换边打，账还是这一本。
//
// 只放内存：这是一轮连续对战的临时账本，进程重启后两边从 0:0 重新开始，
// 不存在「谁的比分丢了」——正式战绩在 pool_matches 里，那是另一套。
type Series struct {
	mu     sync.Mutex
	byPair map[string]*seriesEntry
	ttl    time.Duration
}

// seriesEntry 是某一对人的账本。
type seriesEntry struct {
	// wins 的顺序与 pairKey 里两个 id 的先后一致，与座位、与谁先开球都无关。
	wins [2]int
	// at 是最后一次记账的时刻，超过 TTL 整条作废。
	at time.Time
}

// NewSeries 由装配层创建一次，交给 Manager 与 Handler 共用。
func NewSeries() *Series {
	return &Series{
		byPair: make(map[string]*seriesEntry),
		ttl:    SeriesTTL,
	}
}

// pairKey 把两个人编成一个与先后无关的键，并报告 a 是不是排在前面。
//
// 返回值第二个参数决定读出来的两个比分怎么摆：调用方关心的是
// 「a 赢了几局、b 赢了几局」，而账本里永远按 id 从小到大排。
func pairKey(a, b uint) (string, bool) {
	if a <= b {
		return fmt.Sprintf("%d:%d", a, b), true
	}
	return fmt.Sprintf("%d:%d", b, a), false
}

// SeatScores 返回 a、b 两人这一轮的比分，按传入顺序给出。
//
// 从没打过、或者已经超过 TTL，返回 0:0（零值），调用方不用先判断有没有账本。
func (s *Series) SeatScores(a, b uint) [2]int {
	key, aFirst := pairKey(a, b)
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.byPair[key]
	if !ok || time.Since(entry.at) > s.ttl {
		return [2]int{}
	}
	if aFirst {
		return [2]int{entry.wins[0], entry.wins[1]}
	}
	return [2]int{entry.wins[1], entry.wins[0]}
}

// AddWin 给赢的人记一局。
//
// 账本过期时先清空再加：这样「隔了大半天又碰上」不会接着上一轮的比分算。
func (s *Series) AddWin(winner, loser uint) {
	key, winnerFirst := pairKey(winner, loser)
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.byPair[key]
	if !ok || time.Since(entry.at) > s.ttl {
		entry = &seriesEntry{}
		s.byPair[key] = entry
	}
	if winnerFirst {
		entry.wins[0] += 1
	} else {
		entry.wins[1] += 1
	}
	entry.at = time.Now()
}
