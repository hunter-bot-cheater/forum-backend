package pool

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"gorm.io/gorm"

	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
)

// 这个文件覆盖「再约一局」：两个人各自点一下，第二下就把新的一局开起来，
// 跳过房间与准备，并且开球方与上一局轮换。
//
// 走真 HTTP 而不是直接调 handler：这条链路的坑一半在鉴权与状态判断上
// （不是参与者、上一局还没结算），跳过网关那一层等于只测了赋值。

// newFinishedMatch 造一局已经结算的对局，返回它。
//
// 库里那行必须真的走到 finished：handler 按它判断「能不能再约」。
// 内存里再放一个同 id 的 actor：它只用来回答「上一局谁开的球」，
// 真实环境里这个 actor 会一直留到清扫为止。
func newFinishedMatch(t *testing.T, db *gorm.DB, manager *Manager, p0, p1 uint, breaker Seat) *model.PoolMatch {
	t.Helper()
	ctx := context.Background()
	store := NewStore(db)

	row, err := store.CreateMatch(ctx, p0, &p1, "654321")
	if err != nil {
		t.Fatalf("建对局失败：%v", err)
	}
	if err := store.MarkPlaying(ctx, row.ID); err != nil {
		t.Fatalf("推进到进行中失败：%v", err)
	}
	if err := store.FinishMatch(ctx, MatchRecord{MatchID: row.ID, WinReason: WinEightLegal}); err != nil {
		t.Fatalf("结算失败：%v", err)
	}

	state := newMatchState(row.ID, row.RoomCode, [2]PlayerBrief{
		{UserID: p0, Nickname: "甲"},
		{UserID: p1, Nickname: "乙"},
	})
	state.Breaker = breaker
	manager.Create(state)
	return row
}

// readMatchStatus 读库里那条对局的状态。
func readMatchStatus(t *testing.T, db *gorm.DB, matchID uint) string {
	t.Helper()
	var row model.PoolMatch
	if err := db.First(&row, matchID).Error; err != nil {
		t.Fatalf("读对局失败：%v", err)
	}
	return row.Status
}

// 只有一个人点时只是记下来；第二个人一点，新局立刻开起来。
//
// 这条用例同时验三件事，它们是一件事的三个面：
//   - 第一次点不能建局（否则先点的人会自己跟自己开一局）；
//   - 第二个请求必须把新局建出来并直接进入 playing（不再经过房间准备）；
//   - 开球方要换人（上一局座位 0 开球，这一局该座位 1）。
func TestRematchStartsWhenBothClicked(t *testing.T) {
	db := testutil.NewDB(t)
	manager := newRoomManager(t)
	hub := &fakeHub{}
	engine := newEngine(t, db, manager, map[uint]string{31: "甲", 32: "乙"}, hub)

	prev := newFinishedMatch(t, db, manager, 31, 32, Seat0)
	path := "/api/pool/matches/" + strconv.FormatUint(uint64(prev.ID), 10) + "/rematch"

	// 甲先点：只记下来，并告诉乙「他点了」。
	status, body := postJSON(t, engine, path, 31, nil)
	if status != http.StatusOK {
		t.Fatalf("第一次点再约失败 %d：%s", status, body)
	}
	var first rematchResp
	decodeData(t, body, &first)
	if first.Started != nil || !first.Waiting {
		t.Fatalf("只点了一个人就不该开局：%+v", first)
	}
	if len(hub.sentTo(32)) != 1 || hub.sentTo(32)[0].Type != "pool_rematch" {
		t.Fatalf("乙应该收到一条再约请求，实际 %+v", hub.sentTo(32))
	}

	// 乙也点：新局立刻开起来。
	status, body = postJSON(t, engine, path, 32, nil)
	if status != http.StatusOK {
		t.Fatalf("第二次点再约失败 %d：%s", status, body)
	}
	var second rematchResp
	decodeData(t, body, &second)
	if second.Started == nil {
		t.Fatalf("两个人都点了应该直接开局：%+v", second)
	}
	started := second.Started
	if started.MatchID == 0 || started.MatchID == prev.ID {
		t.Fatalf("新局 id 不对：%+v", started)
	}
	if started.Breaker != Seat1 {
		t.Errorf("开球方 = %d，上一局是座位 0 开球，这一局该轮到座位 1", started.Breaker)
	}
	if got := readMatchStatus(t, db, started.MatchID); got != model.PoolStatusPlaying {
		t.Errorf("新局在库里的状态 = %q，期望 playing", got)
	}

	match, ok := manager.Get(started.MatchID)
	if !ok {
		t.Fatal("新局没有进内存，进对局页会拿到「对局不存在」")
	}
	snapshot, err := match.Snapshot(Seat0)
	if err != nil {
		t.Fatalf("取新局快照失败：%v", err)
	}
	if snapshot.Status != model.PoolStatusPlaying || snapshot.Turn != Seat1 {
		t.Errorf("新局状态 = %+v，期望 playing 且轮到座位 1", snapshot)
	}
	// 开球这一杆必须是「开球线之后的手中球」，与房间开局一致。
	if !snapshot.BallInHand || snapshot.PlaceZone != PlaceBehindHead {
		t.Errorf("新局开球没有给手中球：ball_in_hand=%v zone=%q", snapshot.BallInHand, snapshot.PlaceZone)
	}

	// 两边都要收到「已开局」：先点的那个人不在这次请求里，只能靠推送。
	for _, userID := range []uint{31, 32} {
		var found bool
		for _, msg := range hub.sentTo(userID) {
			if msg.Type == "pool_rematch_started" {
				found = true
			}
		}
		if !found {
			t.Errorf("用户 %d 没收到开局推送：%+v", userID, hub.sentTo(userID))
		}
	}
}

// 再来一轮时开球方要换回去：两局之后又轮到最初那个人。
//
// 这条单独验，是因为「轮流开球」最容易写成「永远座位 0 开球」——
// 单局的用例看不出这个问题。
func TestRematchAlternatesBreaker(t *testing.T) {
	db := testutil.NewDB(t)
	manager := newRoomManager(t)
	engine := newEngine(t, db, manager, nil, &fakeHub{})

	prev := newFinishedMatch(t, db, manager, 41, 42, Seat1)
	path := "/api/pool/matches/" + strconv.FormatUint(uint64(prev.ID), 10) + "/rematch"

	if status, body := postJSON(t, engine, path, 41, nil); status != http.StatusOK {
		t.Fatalf("第一次点失败 %d：%s", status, body)
	}
	status, body := postJSON(t, engine, path, 42, nil)
	if status != http.StatusOK {
		t.Fatalf("第二次点失败 %d：%s", status, body)
	}
	var resp rematchResp
	decodeData(t, body, &resp)
	if resp.Started == nil || resp.Started.Breaker != Seat0 {
		t.Fatalf("上一局座位 1 开球，这一局该轮到座位 0：%+v", resp.Started)
	}
}

// 不能约的三种情形要各自回一个准确的码。
func TestRematchRejectsBadRequests(t *testing.T) {
	db := testutil.NewDB(t)
	manager := newRoomManager(t)
	engine := newEngine(t, db, manager, nil, &fakeHub{})

	p1ID := uint(52)
	row, err := NewStore(db).CreateMatch(context.Background(), 51, &p1ID, "777777")
	if err != nil {
		t.Fatalf("建对局失败：%v", err)
	}
	if err := NewStore(db).MarkPlaying(context.Background(), row.ID); err != nil {
		t.Fatalf("推进失败：%v", err)
	}
	// 还没结算。
	if status, body := postJSON(t, engine, "/api/pool/matches/"+strconv.FormatUint(uint64(row.ID), 10)+"/rematch", 51, nil); status != http.StatusConflict {
		t.Errorf("未结算的对局状态码 = %d，期望 409：%s", status, body)
	}

	finished := newFinishedMatch(t, db, manager, 61, 62, Seat0)
	// 不是这一局的人。
	if status, body := postJSON(t, engine, "/api/pool/matches/"+strconv.FormatUint(uint64(finished.ID), 10)+"/rematch", 63, nil); status != http.StatusForbidden {
		t.Errorf("旁观者状态码 = %d，期望 403：%s", status, body)
	}
	// 对局不存在。
	if status, body := postJSON(t, engine, "/api/pool/matches/999999/rematch", 61, nil); status != http.StatusNotFound {
		t.Errorf("不存在的对局状态码 = %d，期望 404：%s", status, body)
	}
}
