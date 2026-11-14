package service

import (
	"strings"
	"testing"
)

// 完全一致时不该报任何问题。这是对账任务的基线：
// 如果它连正常状态都报漂移，跑起来就会变成噪音源，人很快就不看了。
func TestDiffHotNoDrift(t *testing.T) {
	ids := []uint{7, 9}
	redisCounts := map[uint]int64{7: 3, 9: 1}
	mysqlCounts := map[uint]int64{7: 3, 9: 1}
	alive := map[uint]bool{7: true, 9: true}

	drifts, zombies := diffHot(ids, redisCounts, mysqlCounts, alive)
	if len(drifts) != 0 {
		t.Fatalf("drifts = %v, want empty", drifts)
	}
	if len(zombies) != 0 {
		t.Fatalf("zombies = %v, want empty", zombies)
	}
}

// Redis 少算一分，要报一条漂移，并且两个值都要带出来。
// 只报「不一致」而不带具体数值，修的时候还得再查一遍库。
func TestDiffHotScoreMismatch(t *testing.T) {
	ids := []uint{7}
	redisCounts := map[uint]int64{7: 2}
	mysqlCounts := map[uint]int64{7: 3}
	alive := map[uint]bool{7: true}

	drifts, zombies := diffHot(ids, redisCounts, mysqlCounts, alive)
	if len(zombies) != 0 {
		t.Fatalf("zombies = %v, want empty", zombies)
	}
	if len(drifts) != 1 {
		t.Fatalf("drifts len = %d, want 1", len(drifts))
	}
	if drifts[0].ArticleID != 7 || drifts[0].InRedis != 2 || drifts[0].InMySQL != 3 {
		t.Fatalf("drift = %+v, want {7 2 3}", drifts[0])
	}
}

// 文章已删但 ZSET 里还留着，判僵尸。
// 这类成员没有人会触发自我修正，是对账存在的核心理由。
func TestDiffHotZombie(t *testing.T) {
	ids := []uint{7, 9}
	redisCounts := map[uint]int64{7: 3, 9: 4}
	mysqlCounts := map[uint]int64{7: 3, 9: 4}
	alive := map[uint]bool{7: true}

	drifts, zombies := diffHot(ids, redisCounts, mysqlCounts, alive)
	if len(drifts) != 0 {
		t.Fatalf("drifts = %v, want empty", drifts)
	}
	if len(zombies) != 1 || zombies[0] != 9 {
		t.Fatalf("zombies = %v, want [9]", zombies)
	}
}

// 真实点赞归零，Redis 还留着分数。Fix 走 ZREM，不是写 0。
// 写成 0 会让榜单里出现一个分数为 0 的空壳，占着榜单位置。
func TestDiffHotZeroCountIsDrift(t *testing.T) {
	ids := []uint{7}
	redisCounts := map[uint]int64{7: 1}
	mysqlCounts := map[uint]int64{}
	alive := map[uint]bool{7: true}

	drifts, zombies := diffHot(ids, redisCounts, mysqlCounts, alive)
	if len(zombies) != 0 {
		t.Fatalf("zombies = %v, want empty", zombies)
	}
	if len(drifts) != 1 {
		t.Fatalf("drifts len = %d, want 1", len(drifts))
	}
	if drifts[0].InMySQL != 0 {
		t.Fatalf("InMySQL = %d, want 0", drifts[0].InMySQL)
	}
}

// 输出顺序必须跟 Redis 排名一致，不然人工比对报告时要自己再排一遍。
func TestDiffHotKeepsRankOrder(t *testing.T) {
	ids := []uint{9, 7, 3}
	redisCounts := map[uint]int64{9: 1, 7: 2, 3: 5}
	mysqlCounts := map[uint]int64{9: 9, 7: 9, 3: 9}
	alive := map[uint]bool{9: true, 7: true, 3: true}

	drifts, _ := diffHot(ids, redisCounts, mysqlCounts, alive)
	if len(drifts) != 3 {
		t.Fatalf("drifts len = %d, want 3", len(drifts))
	}
	want := []uint{9, 7, 3}
	for i := 0; i < len(want); i++ {
		if drifts[i].ArticleID != want[i] {
			t.Fatalf("drifts order = %v, want %v", drifts, want)
		}
	}
}

// 空榜单不该报问题。服务刚起来、一篇文章都没有的时候就是这种状态。
func TestDiffHotEmpty(t *testing.T) {
	drifts, zombies := diffHot(nil, nil, nil, nil)
	if len(drifts) != 0 || len(zombies) != 0 {
		t.Fatalf("drifts = %v zombies = %v, want empty", drifts, zombies)
	}
}

// pending 与 stream_pending 短时间不为 0 属于正常在途，
// 不能当成故障。否则每 200ms 一轮的 relay 会让告警一直响。
func TestHasProblemIgnoresBacklog(t *testing.T) {
	rep := &ReconcileReport{OutboxPending: 40, StreamPending: 12, StreamLen: 40}
	if rep.HasProblem() {
		t.Fatal("inline backlog should not be treated as problem")
	}

	rep.OutboxFailed = 1
	if !rep.HasProblem() {
		t.Fatal("failed outbox must be treated as problem")
	}
}

func TestHasProblemOnDriftAndZombie(t *testing.T) {
	drift := &ReconcileReport{Drifts: []HotDrift{{ArticleID: 7, InRedis: 2, InMySQL: 3}}}
	if !drift.HasProblem() {
		t.Fatal("drift must be treated as problem")
	}

	zombie := &ReconcileReport{Zombies: []uint{9}}
	if !zombie.HasProblem() {
		t.Fatal("zombie must be treated as problem")
	}

	lost := &ReconcileReport{ProjectionLost: true}
	if !lost.HasProblem() {
		t.Fatal("lost projection must be treated as problem")
	}
}

// 空榜有两种可能，区分依据只能是 MySQL。
// 判错的代价是两个方向都很难受：
// 把正常空榜当成丢失会每小时多一次无谓重建；
// 把丢失当成空榜会让接口一直返回空榜单，直到 ready 过期。
func TestProjectionLost(t *testing.T) {
	cases := []struct {
		name         string
		zsetSize     int64
		readyMissing bool
		totalLikes   int64
		want         bool
	}{
		{name: "ready 在 ZSET 空 库里有点赞", zsetSize: 0, readyMissing: false, totalLikes: 42, want: true},
		{name: "真空榜 ready 在 ZSET 空 库里没点赞", zsetSize: 0, readyMissing: false, totalLikes: 0, want: false},
		{name: "ready 缺失走的是重建分支", zsetSize: 0, readyMissing: true, totalLikes: 42, want: false},
		{name: "ZSET 有成员不属于丢失", zsetSize: 30, readyMissing: false, totalLikes: 42, want: false},
	}
	for _, c := range cases {
		got := projectionLost(c.zsetSize, c.readyMissing, c.totalLikes)
		if got != c.want {
			t.Fatalf("%s: projectionLost = %t, want %t", c.name, got, c.want)
		}
	}
}

// Summary 是给日志和 CLI 直接打印的，四个关键数必须都在里面。
func TestReportSummary(t *testing.T) {
	rep := &ReconcileReport{
		Checked:       200,
		Drifts:        []HotDrift{{ArticleID: 7}},
		Zombies:       []uint{9},
		ReadyMissing:  false,
		OutboxPending: 3,
		OutboxFailed:  1,
		StreamLen:     30,
		StreamPending: 2,
	}
	got := rep.Summary()
	for _, want := range []string{"checked=200", "drift=1", "zombie=1", "failed=1", "stream_pending=2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
}
