package pool

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"forum-backend/internal/pool/sim"
	"forum-backend/pkg/jwt"
)

// newTestServer 起一个只挂了对局 WS 的 gin 服务，返回地址与 jwt manager。
func newTestServer(t *testing.T, manager *Manager) (string, *jwt.Manager) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	jwtManager, err := jwt.NewManager(strings.Repeat("k", 32), 15*time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("构造 jwt manager 失败：%v", err)
	}

	engine := gin.New()
	handler := NewWSHandler(manager, jwtManager, []string{"example.com"})
	engine.GET("/api/ws/game", handler.Serve)

	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return server.URL, jwtManager
}

// dialGame 连上对局 WebSocket。token 为空时不带 token，用于验鉴权分支。
func dialGame(t *testing.T, baseURL, token string, matchID uint) (*websocket.Conn, *http.Response, error) {
	t.Helper()

	url := "ws" + strings.TrimPrefix(baseURL, "http") + "/api/ws/game?match_id=" + strconv.FormatUint(uint64(matchID), 10)
	if token != "" {
		url += "&token=" + token
	}
	return websocket.DefaultDialer.Dial(url, nil)
}

// readMessage 读一条消息并解成信封结构。
func readMessage(t *testing.T, conn *websocket.Conn) Message {
	t.Helper()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读消息失败：%v", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("消息不是 JSON：%v", err)
	}
	return msg
}

// startMatch 造一局并登记到 Manager 里，两个座位分别是用户 1 与 2。
func startMatch(t *testing.T, shot *sim.Shot) (*Manager, uint) {
	t.Helper()

	manager, err := NewManager(Options{Sim: &fakeSim{shot: shot}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	state := newMatchState(9, "000123", [2]PlayerBrief{
		{UserID: 1, Nickname: "甲"},
		{UserID: 2, Nickname: "乙"},
	})
	manager.Create(state)
	return manager, state.MatchID
}

// 没带 token：握手前就 401，不能等升级之后再说。
func TestGameWSRequiresToken(t *testing.T) {
	manager, matchID := startMatch(t, quietShot())
	baseURL, _ := newTestServer(t, manager)

	conn, resp, err := dialGame(t, baseURL, "", matchID)
	if err == nil {
		conn.Close()
		t.Fatal("没带 token 不该连上")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("状态码应当 401，实际 %+v", resp)
	}
}

// 对局不在内存里：404，前端据此回大厅而不是反复重连。
func TestGameWSRejectsUnknownMatch(t *testing.T) {
	manager, _ := startMatch(t, quietShot())
	baseURL, jwtManager := newTestServer(t, manager)

	token, _ := jwtManager.SignAccess(1)
	conn, resp, err := dialGame(t, baseURL, token, 999999)
	if err == nil {
		conn.Close()
		t.Fatal("不存在的对局不该连上")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("状态码应当 404，实际 %+v", resp)
	}
}

// 不是这一局里的人：403，别把对局内容泄露给路人。
func TestGameWSRejectsOutsider(t *testing.T) {
	manager, matchID := startMatch(t, quietShot())
	baseURL, jwtManager := newTestServer(t, manager)

	token, _ := jwtManager.SignAccess(3)
	conn, resp, err := dialGame(t, baseURL, token, matchID)
	if err == nil {
		conn.Close()
		t.Fatal("局外人不该连上")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("状态码应当 403，实际 %+v", resp)
	}
}

// 正常连接：先收到 joined（带座位），再收到完整快照。
func TestGameWSJoinsAndReceivesState(t *testing.T) {
	manager, matchID := startMatch(t, quietShot())
	baseURL, jwtManager := newTestServer(t, manager)

	token, _ := jwtManager.SignAccess(2)
	conn, _, err := dialGame(t, baseURL, token, matchID)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer conn.Close()

	joined := readMessage(t, conn)
	if joined.Type != MsgJoined {
		t.Fatalf("第一条应当是 joined，实际 %s", joined.Type)
	}
	data, _ := json.Marshal(joined.Data)
	var joinedData JoinedData
	_ = json.Unmarshal(data, &joinedData)
	if joinedData.Seat != Seat1 {
		t.Errorf("用户 2 应当坐 1 号位，实际 %d", joinedData.Seat)
	}
	if joinedData.MatchID != matchID {
		t.Errorf("match_id = %d，期望 %d", joinedData.MatchID, matchID)
	}

	state := readMessage(t, conn)
	if state.Type != MsgState {
		t.Fatalf("第二条应当是 state，实际 %s", state.Type)
	}
}

// 出杆：上行 shoot，两边都该收到 shot。
func TestGameWSBroadcastsShot(t *testing.T) {
	manager, matchID := startMatch(t, quietShot())
	baseURL, jwtManager := newTestServer(t, manager)

	token0, _ := jwtManager.SignAccess(1)
	token1, _ := jwtManager.SignAccess(2)

	conn0, _, err := dialGame(t, baseURL, token0, matchID)
	if err != nil {
		t.Fatalf("座位 0 连接失败：%v", err)
	}
	defer conn0.Close()
	conn1, _, err := dialGame(t, baseURL, token1, matchID)
	if err != nil {
		t.Fatalf("座位 1 连接失败：%v", err)
	}
	defer conn1.Close()

	// 两条连接各自先收掉 joined 与 state。
	readMessage(t, conn0)
	readMessage(t, conn0)
	readMessage(t, conn1)
	readMessage(t, conn1)

	shoot := `{"type":"shoot","seq":7,"data":{"phi":1.5,"power":0.6,"theta":0,"a":0,"b":0}}`
	if err := conn0.WriteMessage(websocket.TextMessage, []byte(shoot)); err != nil {
		t.Fatalf("发送 shoot 失败：%v", err)
	}

	// 射手会先收到自己那条 peer joined 之类的消息，这里循环找到 shot 为止。
	found := false
	for index := 0; index < 6; index++ {
		msg := readMessage(t, conn1)
		if msg.Type == MsgShot {
			found = true
			data, _ := json.Marshal(msg.Data)
			var shotData ShotData
			_ = json.Unmarshal(data, &shotData)
			if shotData.Seat != Seat0 || shotData.Seq != 1 {
				t.Errorf("shot 内容不对：%+v", shotData)
			}
			break
		}
	}
	if !found {
		t.Fatal("对手没收到 shot")
	}
}

// 不是自己的回合出杆：回一条 error，seq 与请求配对。
func TestGameWSReportsOutOfTurn(t *testing.T) {
	manager, matchID := startMatch(t, quietShot())
	baseURL, jwtManager := newTestServer(t, manager)

	token1, _ := jwtManager.SignAccess(2)
	conn1, _, err := dialGame(t, baseURL, token1, matchID)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer conn1.Close()

	readMessage(t, conn1)
	readMessage(t, conn1)

	shoot := `{"type":"shoot","seq":31,"data":{"phi":1.5,"power":0.6}}`
	if err := conn1.WriteMessage(websocket.TextMessage, []byte(shoot)); err != nil {
		t.Fatalf("发送 shoot 失败：%v", err)
	}

	msg := readMessage(t, conn1)
	if msg.Type != MsgError {
		t.Fatalf("应当收到 error，实际 %s", msg.Type)
	}
	if msg.Seq != 31 {
		t.Errorf("seq = %d，期望 31（要不前端没法把提示挂到对应操作上）", msg.Seq)
	}
	data, _ := json.Marshal(msg.Data)
	var errData ErrorData
	_ = json.Unmarshal(data, &errData)
	if errData.Code != ErrCodeNotYourTurn {
		t.Errorf("错误码 = %s，期望 %s", errData.Code, ErrCodeNotYourTurn)
	}
}

// 脏数据不踢人：回一条 error 让它自己收敛，连接继续可用。
func TestGameWSKeepsConnectionOnBadMessage(t *testing.T) {
	manager, matchID := startMatch(t, quietShot())
	baseURL, jwtManager := newTestServer(t, manager)

	token, _ := jwtManager.SignAccess(1)
	conn, _, err := dialGame(t, baseURL, token, matchID)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer conn.Close()

	readMessage(t, conn)
	readMessage(t, conn)

	if err := conn.WriteMessage(websocket.TextMessage, []byte("{not json")); err != nil {
		t.Fatalf("发送脏数据失败：%v", err)
	}

	msg := readMessage(t, conn)
	if msg.Type != MsgError {
		t.Fatalf("应当收到 error，实际 %s", msg.Type)
	}

	// 连接还能用：再发一次合法消息仍然有回应。
	shoot := `{"type":"shoot","seq":5,"data":{"phi":1.5,"power":0.6}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(shoot)); err != nil {
		t.Fatalf("脏数据之后连接不能用了：%v", err)
	}
}
