package pool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"forum-backend/internal/model"
	"forum-backend/internal/pool/sim"
	"forum-backend/internal/realtime"
	"forum-backend/internal/testutil"
	"forum-backend/internal/user"
)

// 这个文件覆盖 Handler 的读接口：观战列表、战绩、回放。
//
// 走真 HTTP（httptest）而不是直接调 handler 函数：
// 这几条接口的坑一半在路由参数与鉴权上（别人的对局要 403、未结算要 409），
// 直接调函数会把这一层跳过，等于只测了自己写的那几行赋值。

// fakeUsers 按 id 返回昵称，模拟 user 模块的批量读接口。
type fakeUsers struct {
	names map[uint]string
}

func (f fakeUsers) Briefs(_ context.Context, ids []uint) (map[uint]user.Brief, error) {
	briefs := make(map[uint]user.Brief, len(ids))
	for _, id := range ids {
		briefs[id] = user.Brief{ID: id, Username: "u" + strconv.FormatUint(uint64(id), 10), Nickname: f.names[id]}
	}
	return briefs, nil
}

// fakePusher 什么也不推：读接口不该产生推送，用例也不需要它。
type fakePusher struct{}

func (fakePusher) Push(uint, realtime.PushMessage) {}

// newRestEngine 起一个只挂了台球读接口的 gin 引擎。
//
// userID 直接从请求头里取，绕开 JWT：这里要验的是 handler 的行为，
// 而 token 的解析在 middleware 里已经由它自己的用例覆盖。
func newRestEngine(t *testing.T, db *gorm.DB, manager *Manager, names map[uint]string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	handler := NewHandler(manager, NewInvites(), NewStore(db), fakeUsers{names: names}, fakePusher{})

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		if raw := c.GetHeader("X-Test-User"); raw != "" {
			id, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				t.Fatalf("用例给的 user id 不合法：%v", err)
			}
			c.Set("userID", uint(id))
		}
		c.Next()
	})
	engine.GET("/api/pool/live", handler.Live)
	engine.GET("/api/pool/records", handler.Records)
	engine.GET("/api/pool/matches/:id/replay", handler.Replay)
	return engine
}

// do 发一次带 user 的请求，返回状态码与响应体。
func do(t *testing.T, engine *gin.Engine, path string, userID uint) (int, []byte) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	if userID != 0 {
		req.Header.Set("X-Test-User", strconv.FormatUint(uint64(userID), 10))
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.Bytes()
}

// decodeData 把 {"code":0,"data":...} 里的 data 解出来。
func decodeData(t *testing.T, body []byte, target any) {
	t.Helper()

	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("响应不是 JSON：%s", body)
	}
	if envelope.Code != 0 {
		t.Fatalf("响应 code = %d：%s", envelope.Code, body)
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("解 data 失败：%v（%s）", err, envelope.Data)
	}
}

// 观战列表只列正在打的对局，房间与已结束的都不出现。
//
// 房间列进去没有意义（还没开打，进去只能看着两个头像）；
// 已结束的列进去会让人点开一个已经结算的页面。
func TestLiveOnlyListsPlayingMatches(t *testing.T) {
	db := testutil.NewDB(t)
	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	playing := newMatchState(301, "300001", [2]PlayerBrief{{UserID: 11, Nickname: "甲"}, {UserID: 12, Nickname: "乙"}})
	match := manager.Create(playing)
	if err := match.Join(Seat0, &fakeConn{}); err != nil {
		t.Fatalf("进房失败：%v", err)
	}
	if err := match.Watch(&fakeConn{}); err != nil {
		t.Fatalf("观战失败：%v", err)
	}

	waiting := newRoomState(302, "300002", [2]PlayerBrief{{UserID: 13}, {UserID: 14}})
	manager.Create(waiting)

	finished := newMatchState(303, "300003", [2]PlayerBrief{{UserID: 15}, {UserID: 16}})
	finishedMatch := manager.Create(finished)
	if err := finishedMatch.Join(Seat0, &fakeConn{}); err != nil {
		t.Fatalf("进房失败：%v", err)
	}
	if err := finishedMatch.Leave(Seat0); err != nil {
		t.Fatalf("退出失败：%v", err)
	}

	engine := newRestEngine(t, db, manager, map[uint]string{11: "甲", 12: "乙"})
	status, body := do(t, engine, "/api/pool/live", 11)
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", status, body)
	}

	var list []liveMatchResp
	decodeData(t, body, &list)
	if len(list) != 1 {
		t.Fatalf("列表有 %d 条，只该有正在打的那一局：%+v", len(list), list)
	}
	item := list[0]
	if item.MatchID != 301 {
		t.Errorf("对局号 = %d，期望 301", item.MatchID)
	}
	if item.Players[0].Nickname != "甲" || item.Players[1].Nickname != "乙" {
		t.Errorf("玩家资料不对：%+v", item.Players)
	}
	if item.Spectators != 1 || item.SpectatorsMax != maxSpectators {
		t.Errorf("观战人数 = %d/%d，期望 1/%d", item.Spectators, item.SpectatorsMax, maxSpectators)
	}
}

// 战绩接口给出汇总与列表，对手资料要带上昵称。
func TestRecordsReturnsRatingAndOpponent(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	ctx := context.Background()

	match, p0, p1 := seedMatch(t, db, model.PoolStatusPlaying)
	if err := store.FinishMatch(ctx, MatchRecord{
		MatchID:   match.ID,
		Winner:    Seat0,
		WinnerID:  p0.ID,
		LoserID:   p1.ID,
		WinReason: WinEightLegal,
		Shots:     7,
		EndedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("结算失败：%v", err)
	}

	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	engine := newRestEngine(t, db, manager, map[uint]string{p0.ID: "甲", p1.ID: "乙"})
	status, body := do(t, engine, "/api/pool/records", p0.ID)
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", status, body)
	}

	var resp recordsResp
	decodeData(t, body, &resp)
	if resp.Rating.Wins != 1 || resp.Rating.Losses != 0 {
		t.Errorf("战绩 = %+v，期望 1 胜 0 负", resp.Rating)
	}
	if resp.Rating.Elo <= 1000 {
		t.Errorf("赢了之后积分应当高于 1000，实际 %d", resp.Rating.Elo)
	}
	if len(resp.List) != 1 {
		t.Fatalf("列表有 %d 行，期望 1 行", len(resp.List))
	}
	if resp.List[0].Opponent.Nickname != "乙" {
		t.Errorf("对手昵称 = %q，期望「乙」", resp.List[0].Opponent.Nickname)
	}
	if !resp.List[0].Won || resp.List[0].ShotCount != 7 {
		t.Errorf("这一行不对：%+v", resp.List[0])
	}
}

// 回放只给这一局的两位玩家。
func TestReplayForbidsOutsider(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	match, p0, p1 := seedMatch(t, db, model.PoolStatusPlaying)
	if err := store.FinishMatch(context.Background(), MatchRecord{
		MatchID:   match.ID,
		Winner:    Seat0,
		WinnerID:  p0.ID,
		LoserID:   p1.ID,
		WinReason: WinEightLegal,
		Shots:     1,
		EndedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("结算失败：%v", err)
	}

	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	engine := newRestEngine(t, db, manager, nil)
	path := "/api/pool/matches/" + strconv.FormatUint(uint64(match.ID), 10) + "/replay"

	// 局外人：看不到别人的对局。
	status, body := do(t, engine, path, p1.ID+1000)
	if status != http.StatusForbidden {
		t.Fatalf("局外人应当 403，实际 %d：%s", status, body)
	}

	// 局中人：能取到。
	status, body = do(t, engine, path, p0.ID)
	if status != http.StatusOK {
		t.Fatalf("局中人应当 200，实际 %d：%s", status, body)
	}
}

// 还没结算的对局不给回放：这时 pool_shots 还没写完，给了也是半份。
func TestReplayRejectsUnfinishedMatch(t *testing.T) {
	db := testutil.NewDB(t)
	match, p0, _ := seedMatch(t, db, model.PoolStatusPlaying)

	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	engine := newRestEngine(t, db, manager, nil)
	path := "/api/pool/matches/" + strconv.FormatUint(uint64(match.ID), 10) + "/replay"

	status, body := do(t, engine, path, p0.ID)
	if status != http.StatusConflict {
		t.Fatalf("未结算应当 409，实际 %d：%s", status, body)
	}
}

// 回放里的杆按 seq 升序，帧原样带回来。
//
// frames 是 msgpack 压过存的，读出来要能还原成一模一样的结构——
// 这里顺手把「一杆 60KB 的帧」这条链路验一遍。
func TestReplayReturnsShotsInOrder(t *testing.T) {
	db := testutil.NewDB(t)
	store := NewStore(db)
	ctx := context.Background()

	match, p0, p1 := seedMatch(t, db, model.PoolStatusPlaying)
	first := &sim.Frame{T: 0, Balls: []byte(`[["cue",0.5,0.5,0]]`)}
	second := &sim.Frame{T: 1, Balls: []byte(`[["cue",0.6,0.6,0]]`)}

	// 故意倒着写：读出来必须按 seq 升序，不能按写入顺序。
	if err := store.SaveShot(ctx, ShotRecord{
		MatchID: match.ID, Seq: 2, ShooterID: p1.ID, Seat: Seat1,
		Params: sim.CueParams{Phi: 1.5, Power: 0.6}, Frames: []sim.Frame{*second},
		Duration: 1.2, SimMS: 20,
	}); err != nil {
		t.Fatalf("写第 2 杆失败：%v", err)
	}
	if err := store.SaveShot(ctx, ShotRecord{
		MatchID: match.ID, Seq: 1, ShooterID: p0.ID, Seat: Seat0,
		Params: sim.CueParams{Phi: 1.4, Power: 0.5}, Frames: []sim.Frame{*first},
		Duration: 0.8, SimMS: 18,
	}); err != nil {
		t.Fatalf("写第 1 杆失败：%v", err)
	}
	if err := store.FinishMatch(ctx, MatchRecord{
		MatchID: match.ID, Winner: Seat0, WinnerID: p0.ID, LoserID: p1.ID,
		WinReason: WinEightLegal, Shots: 2, EndedAt: time.Now(),
	}); err != nil {
		t.Fatalf("结算失败：%v", err)
	}

	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	engine := newRestEngine(t, db, manager, nil)
	path := "/api/pool/matches/" + strconv.FormatUint(uint64(match.ID), 10) + "/replay"
	status, body := do(t, engine, path, p0.ID)
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", status, body)
	}

	var resp replayResp
	decodeData(t, body, &resp)
	if len(resp.Shots) != 2 {
		t.Fatalf("回放有 %d 杆，期望 2 杆", len(resp.Shots))
	}
	if resp.Shots[0].Seq != 1 || resp.Shots[1].Seq != 2 {
		t.Errorf("杆序不对：%d, %d", resp.Shots[0].Seq, resp.Shots[1].Seq)
	}
	if len(resp.Shots[0].Frames) != 1 {
		t.Fatalf("第 1 杆应当有 1 帧，实际 %d", len(resp.Shots[0].Frames))
	}
	if string(resp.Shots[0].Frames[0].Balls) != string(first.Balls) {
		t.Errorf("帧内容被改动：%s", resp.Shots[0].Frames[0].Balls)
	}
	if resp.Winner != Seat0 || resp.Reason != WinEightLegal {
		t.Errorf("结算信息不对：%+v", resp)
	}
}
