package pool

import (
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/model"
	"forum-backend/internal/realtime"
	"forum-backend/internal/testutil"
)

// 这个文件覆盖「我的房间」这一整套流程：
// 进大厅就有房间 → 房号报给别人 → 别人输房号进来 → 双方准备 → 开局。
//
// 全部走真 HTTP：这一层的坑多半在状态码与「谁的房间算谁的」上，
// 直接调函数会跳过路由参数与鉴权，等于只测了自己写的那几行赋值。

// fakeHub 既记录推送又按用例设定谁在线。
//
// 并发用例（两个人同时输同一个房号）会用同一个实例，所以内部加锁。
type fakeHub struct {
	mu     sync.Mutex
	online []uint
	pushed []pushRecord
}

// pushRecord 是一条推送的存档，方便用例断言「发给谁、什么类型、带什么」。
type pushRecord struct {
	userID uint
	msg    realtime.PushMessage
}

func (f *fakeHub) Push(userID uint, msg realtime.PushMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushed = append(f.pushed, pushRecord{userID: userID, msg: msg})
}

func (f *fakeHub) IsOnline(userID uint) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.online {
		if id == userID {
			return true
		}
	}
	return false
}

func (f *fakeHub) OnlineUserIDs() []uint {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]uint, len(f.online))
	copy(ids, f.online)
	return ids
}

// setOnline 换一份在线名单。
func (f *fakeHub) setOnline(ids ...uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online = ids
}

// sentTo 返回发给某个人的全部推送。
func (f *fakeHub) sentTo(userID uint) []realtime.PushMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]realtime.PushMessage, 0, len(f.pushed))
	for _, record := range f.pushed {
		if record.userID == userID {
			list = append(list, record.msg)
		}
	}
	return list
}

// newRoomManager 造一个能跑房间流程的 manager：仿真给假实现，读秒给足。
func newRoomManager(t *testing.T) *Manager {
	t.Helper()

	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)
	return manager
}

// openRoomFor 用接口开一间房，返回房间快照。
func openRoomFor(t *testing.T, engine *gin.Engine, userID uint) roomResp {
	t.Helper()

	status, body := postJSON(t, engine, "/api/pool/rooms/my", userID, nil)
	if status != http.StatusOK {
		t.Fatalf("开房失败 %d：%s", status, body)
	}
	var room roomResp
	decodeData(t, body, &room)
	return room
}

// 点进台球就该有一间自己的房，而且刷新多少次都是同一间。
//
// 每次刷新都换房号的话，刚报给别人的那串数字立刻作废，
// 对方输进来只会得到「房号不对」——这是这个功能最容易踩的一脚。
func TestMyRoomKeepsSameRoomOnRefresh(t *testing.T) {
	db := testutil.NewDB(t)
	engine := newEngine(t, db, newRoomManager(t), map[uint]string{7: "甲"}, &fakeHub{})

	first := openRoomFor(t, engine, 7)
	if first.MatchID == 0 || first.RoomCode == "" {
		t.Fatalf("房间信息不全：%+v", first)
	}
	if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(first.RoomCode) {
		t.Errorf("房号 = %q，期望六位数字", first.RoomCode)
	}
	if first.Seat != Seat0 {
		t.Errorf("房主座位 = %d，期望 0", first.Seat)
	}
	if first.Status != model.PoolStatusWaiting {
		t.Errorf("status = %q，期望 waiting", first.Status)
	}
	if first.Players[0].UserID != 7 || first.Players[0].Nickname != "甲" {
		t.Errorf("自己那半边不对：%+v", first.Players[0])
	}
	// 另半边得是空的：前端靠 user_id == 0 画那个虚线空位。
	if first.Players[1].UserID != 0 {
		t.Errorf("另半边该空着，实际 %+v", first.Players[1])
	}

	second := openRoomFor(t, engine, 7)
	if second.MatchID != first.MatchID || second.RoomCode != first.RoomCode {
		t.Fatalf("刷新换了房间：%d/%s -> %d/%s",
			first.MatchID, first.RoomCode, second.MatchID, second.RoomCode)
	}

	// 库里也只该有一行 waiting，否则下一次 WaitingMatchFor 会挑到另一行。
	var count int64
	if err := db.Model(&model.PoolMatch{}).
		Where("p0_id = ? AND status = ?", 7, model.PoolStatusWaiting).
		Count(&count).Error; err != nil {
		t.Fatalf("统计房间失败：%v", err)
	}
	if count != 1 {
		t.Errorf("库里有 %d 间 waiting 房间，期望 1 间", count)
	}
}

// 别人输房号进来：他坐到座位 1，房主立刻收到一条推送。
func TestJoinRoomSeatsGuestAndNotifiesOwner(t *testing.T) {
	db := testutil.NewDB(t)
	hub := &fakeHub{}
	engine := newEngine(t, db, newRoomManager(t), map[uint]string{7: "甲", 8: "乙"}, hub)

	room := openRoomFor(t, engine, 7)

	status, body := postJSON(t, engine, "/api/pool/rooms/join", 8, joinRoomReq{RoomCode: room.RoomCode})
	if status != http.StatusOK {
		t.Fatalf("进房失败 %d：%s", status, body)
	}
	var joined roomResp
	decodeData(t, body, &joined)
	if joined.MatchID != room.MatchID {
		t.Errorf("房间号 = %d，期望 %d", joined.MatchID, room.MatchID)
	}
	if joined.Seat != Seat1 {
		t.Errorf("进房的人座位 = %d，期望 1", joined.Seat)
	}
	if joined.Players[1].UserID != 8 || joined.Players[1].Nickname != "乙" {
		t.Errorf("进房的人没被填到座位上：%+v", joined.Players[1])
	}
	// 房间里两个人都还没准备：前端据此画两个「准备」按钮而不是直接跳对局。
	if joined.Ready != [2]bool{false, false} {
		t.Errorf("准备状态 = %v，期望都没准备", joined.Ready)
	}

	// 库里 p1_id 要落下来，否则刷新之后房主看到的又是一间空房。
	var row model.PoolMatch
	if err := db.First(&row, room.MatchID).Error; err != nil {
		t.Fatalf("读回房间失败：%v", err)
	}
	if row.P1ID == nil || *row.P1ID != 8 {
		t.Errorf("p1_id = %v，期望 8", row.P1ID)
	}

	// 房主不用等轮询就能看到「人来了」，推送里还得带上进房人的昵称。
	sent := hub.sentTo(7)
	if len(sent) == 0 {
		t.Fatal("房主没收到任何推送")
	}
	if sent[0].Type != "pool_room_joined" {
		t.Errorf("推送类型 = %q，期望 pool_room_joined", sent[0].Type)
	}
	payload, ok := sent[0].Data.(roomJoinedResp)
	if !ok {
		t.Fatalf("推送内容类型不对：%T", sent[0].Data)
	}
	if payload.Player.UserID != 8 || payload.Player.Nickname != "乙" {
		t.Errorf("推送里的进房人不对：%+v", payload.Player)
	}
}

// 房号输错、格式不对、房间已开局：三种拒绝要各说各的，状态码也不能混。
func TestJoinRoomRejectsBadCode(t *testing.T) {
	db := testutil.NewDB(t)
	manager := newRoomManager(t)
	engine := newEngine(t, db, manager, map[uint]string{7: "甲", 8: "乙"}, &fakeHub{})

	room := openRoomFor(t, engine, 7)

	// 格式不对：直接 400，不必查库。
	for _, code := range []string{"", "12345", "1234567", "abcdef", "12 45"} {
		status, body := postJSON(t, engine, "/api/pool/rooms/join", 8, joinRoomReq{RoomCode: code})
		if status != http.StatusBadRequest {
			t.Errorf("房号 %q 应当 400，实际 %d：%s", code, status, body)
		}
	}

	// 房间不存在：404。
	status, body := postJSON(t, engine, "/api/pool/rooms/join", 8, joinRoomReq{RoomCode: "999999"})
	if status != http.StatusNotFound {
		t.Errorf("不存在的房号应当 404，实际 %d：%s", status, body)
	}

	// 房间已经开打了：房号相当于作废，也是 404（对用户来说「这号没用了」）。
	playing := newMatchState(9001, "888888", [2]PlayerBrief{{UserID: 11}, {UserID: 12}})
	manager.Create(playing)
	status, body = postJSON(t, engine, "/api/pool/rooms/join", 8, joinRoomReq{RoomCode: "888888"})
	if status != http.StatusNotFound {
		t.Errorf("已开局的房号应当 404，实际 %d：%s", status, body)
	}

	// 输的是自己的房号：不报错，把人带回他自己的房间。
	status, body = postJSON(t, engine, "/api/pool/rooms/join", 7, joinRoomReq{RoomCode: room.RoomCode})
	if status != http.StatusOK {
		t.Fatalf("输自己的房号应当 200，实际 %d：%s", status, body)
	}
	var own roomResp
	decodeData(t, body, &own)
	if own.MatchID != room.MatchID || own.Seat != Seat0 {
		t.Errorf("该回到自己的房间（座位 0），实际 %+v", own)
	}
}

// 房间满了之后第三个人进不来，而且不能把他放进座位 1 顶掉先来的人。
func TestJoinRoomRejectsTakenSeat(t *testing.T) {
	db := testutil.NewDB(t)
	engine := newEngine(t, db, newRoomManager(t), map[uint]string{7: "甲", 8: "乙", 9: "丙"}, &fakeHub{})

	room := openRoomFor(t, engine, 7)

	if status, body := postJSON(t, engine, "/api/pool/rooms/join", 8, joinRoomReq{RoomCode: room.RoomCode}); status != http.StatusOK {
		t.Fatalf("第一个进房的应当 200，实际 %d：%s", status, body)
	}

	status, body := postJSON(t, engine, "/api/pool/rooms/join", 9, joinRoomReq{RoomCode: room.RoomCode})
	if status != http.StatusConflict {
		t.Fatalf("座位被占应当 409，实际 %d：%s", status, body)
	}

	// 座位 1 上还是乙，不能被后来的人顶掉。
	var row model.PoolMatch
	if err := db.First(&row, room.MatchID).Error; err != nil {
		t.Fatalf("读回房间失败：%v", err)
	}
	if row.P1ID == nil || *row.P1ID != 8 {
		t.Errorf("p1_id = %v，期望还是 8", row.P1ID)
	}
}

// 两个人几乎同时输同一个房号，只能有一个坐进去。
//
// 「先查空位再写」在这里会让两个人都以为自己抢到了，
// 判据必须是那条带条件的 UPDATE 的 RowsAffected。
func TestJoinRoomConcurrentOnlyOneSeats(t *testing.T) {
	db := testutil.NewDB(t)
	engine := newEngine(t, db, newRoomManager(t), map[uint]string{7: "甲", 8: "乙", 9: "丙"}, &fakeHub{})

	room := openRoomFor(t, engine, 7)

	const guests = 4
	statuses := make([]int, guests)
	var wg sync.WaitGroup
	for index := 0; index < guests; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			code, _ := postJSON(t, engine, "/api/pool/rooms/join", uint(8+slot), joinRoomReq{RoomCode: room.RoomCode})
			statuses[slot] = code
		}(index)
	}
	wg.Wait()

	ok, conflict := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != guests-1 {
		t.Fatalf("抢座结果 = %v，期望恰好一个 200、其余 409", statuses)
	}

	// 库里也只能有一个人。
	var row model.PoolMatch
	if err := db.First(&row, room.MatchID).Error; err != nil {
		t.Fatalf("读回房间失败：%v", err)
	}
	if row.P1ID == nil {
		t.Fatal("座位 1 空了：没人抢到")
	}
	if *row.P1ID < 8 || *row.P1ID > 11 {
		t.Errorf("p1_id = %d，不在候选里", *row.P1ID)
	}
}

// 在线名单：排除自己与正在打的人，关注的排前面，但没关注的人也照样能邀请。
//
// 「只有关注才能打」是这一轮要去掉的限制：名单里有陌生人本身就是需求。
func TestOnlineListsOthersWithFollowingFirst(t *testing.T) {
	db := testutil.NewDB(t)

	me := testutil.SeedUser(t, db, "pool_me")
	followed := testutil.SeedUser(t, db, "pool_followed")
	stranger := testutil.SeedUser(t, db, "pool_stranger")
	playing := testutil.SeedUser(t, db, "pool_playing")

	if err := db.Create(&model.Follow{FollowerID: me.ID, FolloweeID: followed.ID}).Error; err != nil {
		t.Fatalf("造关注关系失败：%v", err)
	}

	manager := newRoomManager(t)
	// 正在打的那一局：名单里不该出现他，邀请他只会得到一个「我在打」。
	busy := manager.Create(newMatchState(9101, "777777", [2]PlayerBrief{
		{UserID: playing.ID}, {UserID: me.ID},
	}))
	if err := busy.Join(Seat0, &fakeConn{}); err != nil {
		t.Fatalf("进房失败：%v", err)
	}

	hub := &fakeHub{}
	hub.setOnline(followed.ID, stranger.ID, playing.ID, me.ID)

	engine := newEngine(t, db, manager, map[uint]string{
		me.ID: "我", followed.ID: "乙", stranger.ID: "丙", playing.ID: "丁",
	}, hub)

	status, body := do(t, engine, "/api/pool/online", me.ID)
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", status, body)
	}
	var list []onlineUserResp
	decodeData(t, body, &list)

	if len(list) != 2 {
		t.Fatalf("名单 = %+v，期望只有关注的那位与陌生人（自己在打的和正在打的都要排除）", list)
	}
	if list[0].UserID != followed.ID || !list[0].Following {
		t.Errorf("关注的人该排第一且带标记：%+v", list[0])
	}
	if list[1].UserID != stranger.ID || list[1].Following {
		t.Errorf("陌生人该在第二且不带关注标记：%+v", list[1])
	}
	for _, item := range list {
		if item.UserID == me.ID {
			t.Errorf("自己出现在了名单里：%+v", item)
		}
		if item.UserID == playing.ID {
			t.Errorf("正在对局的人出现在了名单里：%+v", item)
		}
	}
}

// 邀请不再要求关注，但要求在线；房间指向邀请人自己那间。
func TestCreateInviteNeedsOnlineNotFollowing(t *testing.T) {
	db := testutil.NewDB(t)
	hub := &fakeHub{}
	engine := newEngine(t, db, newRoomManager(t), map[uint]string{7: "甲", 8: "乙"}, hub)

	room := openRoomFor(t, engine, 7)

	// 对方不在线：400，并且提示把房号发过去更快。
	status, body := postJSON(t, engine, "/api/pool/invites", 7, inviteCreateReq{ToUserID: 8})
	if status != http.StatusBadRequest {
		t.Fatalf("离线的人应当 400，实际 %d：%s", status, body)
	}

	hub.setOnline(8)
	status, body = postJSON(t, engine, "/api/pool/invites", 7, inviteCreateReq{ToUserID: 8})
	if status != http.StatusOK {
		t.Fatalf("在线但没关注的人应当可以邀请，实际 %d：%s", status, body)
	}
	var resp inviteResp
	decodeData(t, body, &resp)

	// 邀请指向我自己的房间：对方接受后进的就是这一间，房号一致。
	if resp.MatchID != room.MatchID || resp.RoomCode != room.RoomCode {
		t.Errorf("邀请指向 %d/%s，期望我的房间 %d/%s",
			resp.MatchID, resp.RoomCode, room.MatchID, room.RoomCode)
	}

	sent := hub.sentTo(8)
	if len(sent) != 1 {
		t.Fatalf("被邀请人收到 %d 条推送，期望 1 条", len(sent))
	}
	if sent[0].Type != "pool_invite" {
		t.Errorf("推送类型 = %q，期望 pool_invite", sent[0].Type)
	}
	payload, ok := sent[0].Data.(pendingInviteResp)
	if !ok {
		t.Fatalf("推送内容类型不对：%T", sent[0].Data)
	}
	if payload.RoomCode != room.RoomCode || payload.From.Nickname != "甲" {
		t.Errorf("推送内容不对：%+v", payload)
	}
}

// 接受邀请就会坐到邀请人房间的座位 1 上，邀请人收到回执。
func TestAcceptInviteJoinsInviterRoom(t *testing.T) {
	db := testutil.NewDB(t)
	hub := &fakeHub{}
	engine := newEngine(t, db, newRoomManager(t), map[uint]string{7: "甲", 8: "乙"}, hub)
	hub.setOnline(8)

	room := openRoomFor(t, engine, 7)
	status, body := postJSON(t, engine, "/api/pool/invites", 7, inviteCreateReq{ToUserID: 8})
	if status != http.StatusOK {
		t.Fatalf("发邀请失败 %d：%s", status, body)
	}
	var invite inviteResp
	decodeData(t, body, &invite)

	status, body = postJSON(t, engine, "/api/pool/invites/"+invite.InviteID+"/accept", 8, nil)
	if status != http.StatusOK {
		t.Fatalf("接受邀请失败 %d：%s", status, body)
	}
	var accepted acceptInviteResp
	decodeData(t, body, &accepted)
	if accepted.MatchID != room.MatchID {
		t.Errorf("接受后进的房间 = %d，期望 %d", accepted.MatchID, room.MatchID)
	}

	var row model.PoolMatch
	if err := db.First(&row, room.MatchID).Error; err != nil {
		t.Fatalf("读回房间失败：%v", err)
	}
	if row.P1ID == nil || *row.P1ID != 8 {
		t.Errorf("p1_id = %v，期望 8", row.P1ID)
	}

	// 邀请人那边要立刻知道「他来了」。
	var acceptedPush bool
	for _, msg := range hub.sentTo(7) {
		if msg.Type == "pool_invite_accepted" {
			acceptedPush = true
		}
	}
	if !acceptedPush {
		t.Error("邀请人没收到 pool_invite_accepted")
	}
}

// 座位被别人用房号占了之后再接受邀请：说「已经有人了」，不把人塞进满员的房间。
func TestAcceptInviteRejectedWhenSeatTaken(t *testing.T) {
	db := testutil.NewDB(t)
	hub := &fakeHub{}
	engine := newEngine(t, db, newRoomManager(t), map[uint]string{7: "甲", 8: "乙", 9: "丙"}, hub)
	hub.setOnline(8)

	room := openRoomFor(t, engine, 7)
	status, body := postJSON(t, engine, "/api/pool/invites", 7, inviteCreateReq{ToUserID: 8})
	if status != http.StatusOK {
		t.Fatalf("发邀请失败 %d：%s", status, body)
	}
	var invite inviteResp
	decodeData(t, body, &invite)

	// 房主把房号给了另一个人，他先坐了进来。
	if status, body := postJSON(t, engine, "/api/pool/rooms/join", 9,
		joinRoomReq{RoomCode: room.RoomCode}); status != http.StatusOK {
		t.Fatalf("房号进房失败 %d：%s", status, body)
	}

	status, body = postJSON(t, engine, "/api/pool/invites/"+invite.InviteID+"/accept", 8, nil)
	if status != http.StatusConflict {
		t.Fatalf("座位被占时接受邀请应当 409，实际 %d：%s", status, body)
	}

	var row model.PoolMatch
	if err := db.First(&row, room.MatchID).Error; err != nil {
		t.Fatalf("读回房间失败：%v", err)
	}
	if row.P1ID == nil || *row.P1ID != 9 {
		t.Errorf("p1_id = %v，期望先坐进来的 9", row.P1ID)
	}
}

// 两个人都在自己的房间里点准备，双方齐了就把房间推进成对局。
func TestBothReadyStartsMatch(t *testing.T) {
	db := testutil.NewDB(t)
	engine := newEngine(t, db, newRoomManager(t), map[uint]string{7: "甲", 8: "乙"}, &fakeHub{})

	room := openRoomFor(t, engine, 7)
	if status, body := postJSON(t, engine, "/api/pool/rooms/join", 8,
		joinRoomReq{RoomCode: room.RoomCode}); status != http.StatusOK {
		t.Fatalf("进房失败 %d：%s", status, body)
	}

	path := fmt.Sprintf("/api/pool/rooms/%d/ready", room.MatchID)
	status, body := postJSON(t, engine, path, 7, readyReq{Ready: true})
	if status != http.StatusOK {
		t.Fatalf("房主准备失败 %d：%s", status, body)
	}
	var half roomResp
	decodeData(t, body, &half)
	// 只有一个人准备好：还在房间里等，不能提前开局。
	if half.Status != model.PoolStatusWaiting {
		t.Fatalf("一个人准备好就开局了：%+v", half)
	}
	if half.Ready != [2]bool{true, false} {
		t.Errorf("准备状态 = %v，期望 [true false]", half.Ready)
	}

	status, body = postJSON(t, engine, path, 8, readyReq{Ready: true})
	if status != http.StatusOK {
		t.Fatalf("对手准备失败 %d：%s", status, body)
	}
	var full roomResp
	decodeData(t, body, &full)
	if full.Status != model.PoolStatusPlaying {
		t.Fatalf("双方都准备好了还是 %q，期望 playing", full.Status)
	}

	// 库里那行也得跟着推进，否则战绩与清扫都看不到这一局。
	var row model.PoolMatch
	if err := db.First(&row, room.MatchID).Error; err != nil {
		t.Fatalf("读回对局失败：%v", err)
	}
	if row.Status != model.PoolStatusPlaying {
		t.Errorf("库里 status = %q，期望 playing", row.Status)
	}
}
