package pool

import (
	"testing"
	"time"
)

// 账本按「一对玩家」记，与谁排前面无关：同一对人换边打，读出来还是各自的胜局。
func TestSeriesKeepsScoreByPair(t *testing.T) {
	series := NewSeries()
	if got := series.SeatScores(7, 9); got != [2]int{} {
		t.Fatalf("没打过时应当是 0:0，实际 %v", got)
	}

	series.AddWin(7, 9)
	series.AddWin(9, 7)
	series.AddWin(9, 7)

	// 7 赢一局、9 赢两局。查询按传入顺序给出，与 id 大小无关。
	if got := series.SeatScores(7, 9); got != [2]int{1, 2} {
		t.Errorf("7:9 的比分 = %v，期望 [1 2]", got)
	}
	if got := series.SeatScores(9, 7); got != [2]int{2, 1} {
		t.Errorf("9:7 的比分 = %v，期望 [2 1]", got)
	}
}

// 超过 TTL 的一轮就当没打过：隔了大半天再碰上，从 0:0 重新开始。
func TestSeriesExpires(t *testing.T) {
	series := NewSeries()
	series.ttl = 10 * time.Millisecond

	series.AddWin(1, 2)
	time.Sleep(20 * time.Millisecond)

	if got := series.SeatScores(1, 2); got != [2]int{} {
		t.Errorf("过期之后应当归零，实际 %v", got)
	}
	// 过期之后再赢一局，从 0 起算。
	series.AddWin(2, 1)
	if got := series.SeatScores(2, 1); got != [2]int{1, 0} {
		t.Errorf("新一轮的比分 = %v，期望 [1 0]", got)
	}
}

// 一局打完，赢家那一位的比分 +1，而且立刻出现在快照里。
//
// 这里用「连续两次超时判负」结束一局：不用造一杆八球胜利的复杂局面，
// 走的还是同一条 finish() 收口，结算逻辑完全一样。
func TestSeriesScoreGrowsWhenMatchEnds(t *testing.T) {
	series := NewSeries()
	state := newMatchState(51, "111111", [2]PlayerBrief{{UserID: 1}, {UserID: 2}})
	state.Series = series.SeatScores(1, 2)

	match := NewMatch(state, Options{
		Sim:         &fakeSim{shot: quietShot()},
		TurnTimeout: 15 * time.Millisecond,
		Series:      series,
	})
	t.Cleanup(match.Close)

	waitFor(t, 5*time.Second, func() bool {
		snapshot, err := match.Snapshot(Seat0)
		return err == nil && snapshot.Status == "finished"
	})

	snapshot, err := match.Snapshot(Seat0)
	if err != nil {
		t.Fatalf("取快照失败：%v", err)
	}
	// 座位 0 一直不出杆，两次超时判负，所以座位 1 拿走这一局。
	if snapshot.Series != [2]int{0, 1} {
		t.Errorf("快照里的比分 = %v，期望 [0 1]", snapshot.Series)
	}
	if got := series.SeatScores(1, 2); got != [2]int{0, 1} {
		t.Errorf("账本里的比分 = %v，期望 [0 1]", got)
	}

	// 再约开出来的下一局读同一份账本，开局就带着这个比分。
	next := newMatchState(52, "111112", [2]PlayerBrief{{UserID: 1}, {UserID: 2}})
	next.Series = series.SeatScores(1, 2)
	if next.Series != [2]int{0, 1} {
		t.Errorf("第二局开局的比分 = %v，期望 [0 1]", next.Series)
	}
}
