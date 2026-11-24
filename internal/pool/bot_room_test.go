package pool

import (
	"net/http"
	"testing"
	"time"

	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
)

// TestStartBotMatch 检查人机开局一步到位：不用房号、不用准备、立刻能打。
func TestStartBotMatch(t *testing.T) {
	db := testutil.NewDB(t)
	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	engine := newRestEngine(t, db, manager, map[uint]string{21: "甲"})

	status, body := postJSON(t, engine, "/api/pool/rooms/bot", 21, nil)
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", status, body)
	}
	var room roomResp
	decodeData(t, body, &room)

	if room.Status != model.PoolStatusPlaying {
		t.Errorf("状态 = %q，期望 playing（人机局没有等待这一步）", room.Status)
	}
	if room.Seat != Seat0 {
		t.Errorf("座位 = %d，玩家应当坐 0 号位并且先开球", room.Seat)
	}
	if room.Players[1].Nickname != BotPlayer.Nickname {
		t.Errorf("座位 1 = %q，期望机器人 %q", room.Players[1].Nickname, BotPlayer.Nickname)
	}
	if room.Players[1].UserID != 0 {
		t.Errorf("机器人不该有 user_id，拿到 %d（结算时会被算成一局真实败绩）", room.Players[1].UserID)
	}

	// 连点两下不该开出两局：第二下要把同一局原样还回来。
	status2, body2 := postJSON(t, engine, "/api/pool/rooms/bot", 21, nil)
	if status2 != http.StatusOK {
		t.Fatalf("第二次状态码 = %d：%s", status2, body2)
	}
	var again roomResp
	decodeData(t, body2, &again)
	if again.MatchID != room.MatchID {
		t.Errorf("第二次开出的是第 %d 局，期望还是第 %d 局", again.MatchID, room.MatchID)
	}
	if manager.Count() != 1 {
		t.Errorf("内存里有 %d 局，应当只有 1 局", manager.Count())
	}
}
