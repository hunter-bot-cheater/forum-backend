package sim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 一段真实响应（截断版），字段名与 pool-sim 的输出逐字一致。
// 用它当夹具的意义是：只要那边改了字段名，这里立刻红。
const sampleResponse = `{
  "duration": 4.3667,
  "events": [
    {"t": 0.0, "type": "stick_ball", "a": "cue_stick", "b": "cue"},
    {"t": 0.1426, "type": "ball_ball", "a": "cue", "b": "9"},
    {"t": 0.28, "type": "cushion", "a": "1", "b": "lc"},
    {"t": 0.3, "type": "ball_pocket", "a": "10", "b": "rb", "b_extra": 0}
  ],
  "final": [
    {"id": "cue", "x": 0.5692, "y": 0.9843, "z": 0.0, "on_table": true},
    {"id": "10", "x": 1.0201, "y": -0.0295, "z": 0.0, "on_table": false}
  ],
  "frames": [
    {"t": 0.0, "balls": [["cue", 0.5944, 0.4557, 0.0]]},
    {"t": 0.0333, "balls": [["cue", 0.5944, 0.4715, 0.0]]}
  ],
  "sim_ms": 688
}`

// 一个最小合法请求，多个用例共用。
func sampleRequest() Request {
	return Request{
		Balls:    []RequestBall{{ID: "cue", X: 0.594371, Y: 0.455658}},
		Cue:      CueParams{Phi: 1.6667, Power: 1},
		SampleHz: 30,
	}
}

func TestShootParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("方法应为 POST，实际 %s", r.Method)
		}
		if r.URL.Path != "/simulate" {
			t.Errorf("路径应为 /simulate，实际 %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type 应为 application/json，实际 %s", ct)
		}
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	shot, err := NewClient(server.URL).Shoot(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("不该失败：%v", err)
	}

	if shot.Duration != 4.3667 {
		t.Errorf("duration = %v，期望 4.3667", shot.Duration)
	}
	if len(shot.Events) != 4 || shot.Events[1].Type != EventBallBall || shot.Events[1].B != "9" {
		t.Fatalf("事件解析不对：%+v", shot.Events)
	}
	if len(shot.Final) != 2 || shot.Final[1].OnTable {
		t.Fatalf("终局解析不对：%+v", shot.Final)
	}
	if shot.SimMS != 688 {
		t.Errorf("sim_ms = %d，期望 688", shot.SimMS)
	}
}

// 帧里的球位是混合类型数组，客户端不解构它，必须原样保留。
func TestShootKeepsFramesVerbatim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	shot, err := NewClient(server.URL).Shoot(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if len(shot.Frames) != 2 {
		t.Fatalf("帧数 = %d，期望 2", len(shot.Frames))
	}

	var balls [][]any
	if err := json.Unmarshal(shot.Frames[1].Balls, &balls); err != nil {
		t.Fatalf("帧里的球位不是合法 JSON：%v", err)
	}
	if len(balls) != 1 || len(balls[0]) != 4 {
		t.Fatalf("帧结构不对：%v", balls)
	}
	if balls[0][0].(string) != "cue" {
		t.Errorf("球号 = %v，期望 cue", balls[0][0])
	}
}

func TestShootSendsAllCueFields(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	req := sampleRequest()
	req.Cue = CueParams{Phi: 0.5, Power: 0.7, Theta: 0.1, A: -0.2, B: 0.3}
	if _, err := NewClient(server.URL).Shoot(context.Background(), req); err != nil {
		t.Fatalf("不该失败：%v", err)
	}

	cue, ok := body["cue"].(map[string]any)
	if !ok {
		t.Fatalf("请求体里没有 cue：%v", body)
	}
	for _, key := range []string{"phi", "power", "theta", "a", "b"} {
		if _, ok := cue[key]; !ok {
			t.Errorf("cue 缺字段 %s，实际 %v", key, cue)
		}
	}
	if body["sample_hz"].(float64) != 30 {
		t.Errorf("sample_hz = %v，期望 30", body["sample_hz"])
	}
}

// 5xx 是「可能抖一下」，重试一次必须成功。
func TestShootRetriesOnceOnServerError(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	shot, err := NewClient(server.URL).Shoot(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("重试之后应当成功：%v", err)
	}
	if shot.Duration == 0 {
		t.Error("拿到的结果不完整")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("调用次数 = %d，期望 2", got)
	}
}

// 422 是请求本身不合法，重试一百次也一样，必须立刻回。
func TestShootDoesNotRetryOnReject(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"power out of range"}`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL).Shoot(context.Background(), sampleRequest())
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v，期望 ErrRejected", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("调用次数 = %d，期望 1", got)
	}
}

// 连不上时返回 ErrUnavailable，调用方据此暂停对局而不是判负。
func TestShootReportsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	_, err := NewClient(url).Shoot(context.Background(), sampleRequest())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v，期望 ErrUnavailable", err)
	}
}

// 响应不是合法 JSON 时也要归到 ErrUnavailable：这是部署不一致，不是玩家的问题。
func TestShootReportsBrokenPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"duration": `))
	}))
	defer server.Close()

	_, err := NewClient(server.URL).Shoot(context.Background(), sampleRequest())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v，期望 ErrUnavailable", err)
	}
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("错误信息应提到 decode：%v", err)
	}
}
