package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"

	"forum-backend/internal/model"
	"forum-backend/internal/pool/sim"
	"forum-backend/internal/testutil"
)

// 这个文件覆盖落库语义：Elo 结算、重复结算、房间过期、一杆的存取、战绩分页。
//
// 全部跑真实 MySQL（未设置 TEST_MYSQL_DSN 时 testutil.NewDB 会 skip）：
// 这些行为的正确性一半在 SQL 上——条件 UPDATE 抢行、唯一索引挡重放、
// ended_at 排序里的平手处理——换成内存假库就测不到了。

// seedSeq 给每次 seedMatch 发一个递增编号。
//
// 用户名与房号都带唯一索引，同一个包里连着造几局时不能重名重号。
// 用计数器而不是时间戳：断言里不需要它，但重复跑同一个用例时要稳定。
var seedSeq int

// seedMatch 建两个用户与一条对局，返回对局与双方。
func seedMatch(t *testing.T, db *gorm.DB, status string) (*model.PoolMatch, *model.User, *model.User) {
	t.Helper()

	seedSeq++
	tag := strconv.Itoa(seedSeq)
	p0 := testutil.SeedUser(t, db, "pool_"+tag+"_p0")
	p1 := testutil.SeedUser(t, db, "pool_"+tag+"_p1")
	match := &model.PoolMatch{
		RoomCode: fmt.Sprintf("%06d", seedSeq),
		P0ID:     p0.ID,
		P1ID:     p1.ID,
		Status:   status,
	}
	if err := db.Create(match).Error; err != nil {
		t.Fatalf("建对局失败: %v", err)
	}
	return match, p0, p1
}

// 赢一局：对局落成 finished，双方 Elo 各变 ±16（初始同分，期望胜率 0.5）。
func TestFinishMatchAppliesElo(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	ctx := context.Background()
	match, p0, p1 := seedMatch(t, db, model.PoolStatusPlaying)

	endedAt := time.Now().Truncate(time.Second)
	err := store.FinishMatch(ctx, MatchRecord{
		MatchID:   match.ID,
		Winner:    Seat0,
		WinnerID:  p0.ID,
		LoserID:   p1.ID,
		WinReason: WinEightLegal,
		Shots:     7,
		EndedAt:   endedAt,
	})
	if err != nil {
		t.Fatalf("结算失败: %v", err)
	}

	var row model.PoolMatch
	if err := db.First(&row, match.ID).Error; err != nil {
		t.Fatalf("读回对局失败: %v", err)
	}
	if row.Status != model.PoolStatusFinished {
		t.Errorf("status = %s，期望 finished", row.Status)
	}
	if row.WinnerID == nil || *row.WinnerID != p0.ID {
		t.Errorf("winner_id = %v，期望 %d", row.WinnerID, p0.ID)
	}
	if row.WinReason != WinEightLegal {
		t.Errorf("win_reason = %s，期望 %s", row.WinReason, WinEightLegal)
	}
	if row.ShotCount != 7 {
		t.Errorf("shot_count = %d，期望 7", row.ShotCount)
	}
	if row.EndedAt == nil {
		t.Error("ended_at 应当写上")
	}

	winner, err := store.Rating(ctx, p0.ID)
	if err != nil {
		t.Fatalf("读赢家战绩失败: %v", err)
	}
	if winner.Elo != 1016 || winner.Wins != 1 {
		t.Errorf("赢家 = Elo %d / %d 胜，期望 1016 / 1", winner.Elo, winner.Wins)
	}

	loser, err := store.Rating(ctx, p1.ID)
	if err != nil {
		t.Fatalf("读输家战绩失败: %v", err)
	}
	if loser.Elo != 984 || loser.Losses != 1 {
		t.Errorf("输家 = Elo %d / %d 负，期望 984 / 1", loser.Elo, loser.Losses)
	}
}

// 同一局结算两次，第二次必须什么都不改：
// 重连、超时、对手退出三条路径可能在同一秒里都触发一次结算。
func TestFinishMatchIsIdempotent(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	ctx := context.Background()
	match, p0, p1 := seedMatch(t, db, model.PoolStatusPlaying)

	record := MatchRecord{
		MatchID:   match.ID,
		Winner:    Seat0,
		WinnerID:  p0.ID,
		LoserID:   p1.ID,
		WinReason: WinEightLegal,
		Shots:     7,
		EndedAt:   time.Now(),
	}
	if err := store.FinishMatch(ctx, record); err != nil {
		t.Fatalf("首次结算失败: %v", err)
	}

	// 第二次故意换成另一方赢：真正生效的话胜负会整体反过来，断言一定抓得到。
	second := record
	second.Winner = Seat1
	second.WinnerID = p1.ID
	second.LoserID = p0.ID
	second.WinReason = WinLeave
	if err := store.FinishMatch(ctx, second); err != nil {
		t.Fatalf("重复结算不该报错: %v", err)
	}

	var row model.PoolMatch
	if err := db.First(&row, match.ID).Error; err != nil {
		t.Fatalf("读回对局失败: %v", err)
	}
	if row.WinnerID == nil || *row.WinnerID != p0.ID {
		t.Errorf("winner_id = %v，期望仍是 %d", row.WinnerID, p0.ID)
	}
	if row.WinReason != WinEightLegal {
		t.Errorf("win_reason = %s，期望仍是 %s", row.WinReason, WinEightLegal)
	}

	winner, _ := store.Rating(ctx, p0.ID)
	if winner.Wins != 1 || winner.Elo != 1016 {
		t.Errorf("赢家战绩被算了两次：Elo %d / %d 胜", winner.Elo, winner.Wins)
	}
	loser, _ := store.Rating(ctx, p1.ID)
	if loser.Losses != 1 || loser.Elo != 984 {
		t.Errorf("输家战绩被算了两次：Elo %d / %d 负", loser.Elo, loser.Losses)
	}
}

// 还没开打的房间被结算：状态不动，也不该冒出战绩行。
func TestFinishMatchIgnoresWaitingRoom(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	ctx := context.Background()
	match, p0, p1 := seedMatch(t, db, model.PoolStatusWaiting)

	err := store.FinishMatch(ctx, MatchRecord{
		MatchID:   match.ID,
		Winner:    Seat0,
		WinnerID:  p0.ID,
		LoserID:   p1.ID,
		WinReason: WinLeave,
		EndedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("结算不该报错: %v", err)
	}

	var row model.PoolMatch
	if err := db.First(&row, match.ID).Error; err != nil {
		t.Fatalf("读回对局失败: %v", err)
	}
	if row.Status != model.PoolStatusWaiting {
		t.Errorf("status = %s，期望仍是 waiting", row.Status)
	}

	var count int64
	if err := db.Model(&model.PoolRating{}).Count(&count).Error; err != nil {
		t.Fatalf("统计战绩失败: %v", err)
	}
	if count != 0 {
		t.Errorf("战绩行数 = %d，期望 0", count)
	}
}

// 过期只作用于还在等人的房间，进行中与已结算的不能被改成 expired。
func TestExpireMatchOnlyTouchesWaiting(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	ctx := context.Background()

	waiting, _, _ := seedMatch(t, db, model.PoolStatusWaiting)
	playing, _, _ := seedMatch(t, db, model.PoolStatusPlaying)
	finished, _, _ := seedMatch(t, db, model.PoolStatusFinished)

	for _, id := range []uint{waiting.ID, playing.ID, finished.ID} {
		if err := store.ExpireMatch(ctx, id); err != nil {
			t.Fatalf("标记过期失败: %v", err)
		}
	}

	read := func(id uint) string {
		var row model.PoolMatch
		if err := db.First(&row, id).Error; err != nil {
			t.Fatalf("读回对局 %d 失败: %v", id, err)
		}
		return row.Status
	}
	if got := read(waiting.ID); got != model.PoolStatusExpired {
		t.Errorf("等待中的房间 = %s，期望 expired", got)
	}
	if got := read(playing.ID); got != model.PoolStatusPlaying {
		t.Errorf("进行中的对局 = %s，期望保持 playing", got)
	}
	if got := read(finished.ID); got != model.PoolStatusFinished {
		t.Errorf("已结算的对局 = %s，期望保持 finished", got)
	}
}

// 一杆写进去再读出来，帧要能原样解回来；同一个 seq 重放要被唯一索引挡住。
func TestSaveShotAndMatchShots(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	ctx := context.Background()
	match, p0, _ := seedMatch(t, db, model.PoolStatusPlaying)

	frames := []sim.Frame{
		{T: 0, Balls: json.RawMessage(`[["cue",0.49,0.99,0]]`)},
		{T: 0.6, Balls: json.RawMessage(`[["cue",0.52,1.2,0],["9",0.6,1.3,0]]`)},
		{T: 1.2, Balls: json.RawMessage(`[["cue",0.55,1.4,0],["9",0.63,1.36,0]]`)},
	}
	record := ShotRecord{
		MatchID:   match.ID,
		Seq:       1,
		ShooterID: p0.ID,
		Seat:      Seat0,
		Params:    sim.CueParams{Phi: 1.57, Power: 0.8},
		Events:    []sim.Event{{T: 0.6, Type: sim.EventBallBall, A: sim.CueBallID, B: "9"}},
		Frames:    frames,
		Duration:  1.2,
		SimMS:     830,
	}
	if err := store.SaveShot(ctx, record); err != nil {
		t.Fatalf("写一杆失败: %v", err)
	}

	shots, err := store.MatchShots(ctx, match.ID)
	if err != nil {
		t.Fatalf("读回放失败: %v", err)
	}
	if len(shots) != 1 {
		t.Fatalf("回放里有 %d 杆，期望 1 杆", len(shots))
	}
	if shots[0].Seq != 1 || shots[0].Seat != Seat0 {
		t.Errorf("回放第一杆 = seq %d / seat %d，期望 1 / 0", shots[0].Seq, shots[0].Seat)
	}
	if shots[0].Duration < 1.19 || shots[0].Duration > 1.21 {
		t.Errorf("时长 = %v，期望约 1.2 秒", shots[0].Duration)
	}
	if len(shots[0].Frames) != len(frames) {
		t.Fatalf("帧数 = %d，期望 %d", len(shots[0].Frames), len(frames))
	}
	// 只比内容不比字节：解压后的 JSON 会重新序列化，空白与浮点写法都可能不同。
	for index, frame := range shots[0].Frames {
		if frame.T != frames[index].T {
			t.Errorf("第 %d 帧 t = %v，期望 %v", index, frame.T, frames[index].T)
		}
		var got, want []any
		if err := json.Unmarshal(frame.Balls, &got); err != nil {
			t.Fatalf("第 %d 帧解不出球位: %v", index, err)
		}
		if err := json.Unmarshal(frames[index].Balls, &want); err != nil {
			t.Fatalf("期望值本身解不出: %v", err)
		}
		if len(got) != len(want) {
			t.Errorf("第 %d 帧有 %d 颗球，期望 %d 颗", index, len(got), len(want))
		}
	}

	// 同一个 seq 再写一次：撞唯一索引。客户端重发 shoot 时 seq 不变，
	// 靠这条索引挡住「同一杆记两条」。
	if err := store.SaveShot(ctx, record); err == nil {
		t.Error("同一个 seq 重写应当被唯一索引挡住")
	}
}

// 没有战绩记录时读出来是初始值，并且不往库里插行。
func TestRatingDefaultsWithoutRow(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	p0 := testutil.SeedUser(t, db, "pool_fresh")

	rating, err := store.Rating(context.Background(), p0.ID)
	if err != nil {
		t.Fatalf("读战绩失败: %v", err)
	}
	if rating.Elo != 1000 || rating.Wins != 0 || rating.Losses != 0 {
		t.Errorf("初始战绩 = %+v，期望 Elo 1000 且零胜零负", rating)
	}

	var count int64
	if err := db.Model(&model.PoolRating{}).Count(&count).Error; err != nil {
		t.Fatalf("统计战绩失败: %v", err)
	}
	if count != 0 {
		t.Errorf("读一次就插了 %d 行，期望 0 行", count)
	}
}

// 战绩列表只认已结算的对局，最近的在前，分页之间不重不漏。
func TestRecentMatchesPaging(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	ctx := context.Background()

	p0 := testutil.SeedUser(t, db, "pool_owner")
	opponents := []*model.User{
		testutil.SeedUser(t, db, "pool_opp1"),
		testutil.SeedUser(t, db, "pool_opp2"),
		testutil.SeedUser(t, db, "pool_opp3"),
		// 第五个只用来造一局「不是我打的」，它不该出现在我的列表里。
		testutil.SeedUser(t, db, "pool_stranger"),
	}

	base := time.Now().Truncate(time.Second)
	create := func(roomCode string, p0ID, p1ID uint, status string, endedAt *time.Time) *model.PoolMatch {
		match := &model.PoolMatch{
			RoomCode: roomCode,
			P0ID:     p0ID,
			P1ID:     p1ID,
			Status:   status,
			EndedAt:  endedAt,
		}
		if err := db.Create(match).Error; err != nil {
			t.Fatalf("建对局失败: %v", err)
		}
		return match
	}

	// 三局已结算（时间递增），外加一局还在打、一局过期，以及一局与我无关。
	oldest := create("111111", p0.ID, opponents[0].ID, model.PoolStatusFinished, timePtr(base.Add(-3*time.Hour)))
	middle := create("222222", opponents[1].ID, p0.ID, model.PoolStatusFinished, timePtr(base.Add(-2*time.Hour)))
	newest := create("333333", p0.ID, opponents[2].ID, model.PoolStatusFinished, timePtr(base.Add(-1*time.Hour)))
	create("444444", p0.ID, opponents[0].ID, model.PoolStatusPlaying, nil)
	create("555555", p0.ID, opponents[0].ID, model.PoolStatusExpired, nil)
	create("666666", opponents[0].ID, opponents[1].ID, model.PoolStatusFinished, timePtr(base))

	firstPage, total, err := store.RecentMatches(ctx, p0.ID, 0, 2)
	if err != nil {
		t.Fatalf("读第一页失败: %v", err)
	}
	if total != 3 {
		t.Errorf("总数 = %d，期望 3（只算已结算的）", total)
	}
	if len(firstPage) != 2 {
		t.Fatalf("第一页 %d 行，期望 2 行", len(firstPage))
	}
	if firstPage[0].ID != newest.ID || firstPage[1].ID != middle.ID {
		t.Errorf("第一页顺序 = %d / %d，期望 %d / %d",
			firstPage[0].ID, firstPage[1].ID, newest.ID, middle.ID)
	}

	secondPage, _, err := store.RecentMatches(ctx, p0.ID, 2, 2)
	if err != nil {
		t.Fatalf("读第二页失败: %v", err)
	}
	if len(secondPage) != 1 {
		t.Fatalf("第二页 %d 行，期望 1 行", len(secondPage))
	}
	if secondPage[0].ID != oldest.ID {
		t.Errorf("第二页是 %d，期望最早那局 %d", secondPage[0].ID, oldest.ID)
	}
}

// timePtr 让上面的用例能一行写一个结束时间。
func timePtr(value time.Time) *time.Time {
	return &value
}
