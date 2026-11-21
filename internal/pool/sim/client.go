// Package sim 是 pool-sim 服务的 HTTP 客户端。
//
// 它只做一件事：把一次出杆的球位与杆法送过去，把那边的轨迹、事件、终局球位拿回来。
// 不含任何规则判断，也不缓存对局——pool-sim 是无状态的纯函数，这里也不持有状态。
package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// 球号常量。母球不叫 "0" 也不叫 "white"，跟 pooltool 与前端保持同一个字符串。
const (
	CueBallID   = "cue"
	EightBallID = "8"
)

// 事件类型。这四个是 pool-sim 过滤之后剩下的全部取值，
// 规则判定只认它们；库里存的也是这四个字符串。
const (
	EventStickBall  = "stick_ball"
	EventBallBall   = "ball_ball"
	EventCushion    = "cushion"
	EventBallPocket = "ball_pocket"
)

// ErrUnavailable 表示仿真服务连不上或超时。
// 与「请求被拒」分开，是因为两者的处置不同：前者对局暂停等重试，后者是代码问题。
var ErrUnavailable = errors.New("pool-sim unavailable")

// ErrRejected 表示 pool-sim 认为请求非法（HTTP 422）。
// 正常路径下不该出现：参数区间在进 actor 之前就校验过了。
var ErrRejected = errors.New("pool-sim rejected the request")

// RequestBall 是一颗球的起始位置，单位米。
//
// Z 是球底离台面的高度，静止球为 0。它和 pool-sim、前端是同一个语义，
// 三方任一处理解成球心高度都会让球浮在半空。
type RequestBall struct {
	ID string  `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
	Z  float64 `json:"z"`
}

// CueParams 是一次击球的六个参数。
//
// Phi 是方位角（弧度，0 指向 +x），Theta 是杆头仰角（弧度），
// A/B 是击球点相对球心的偏移（-1~1）。Power 是 0~1 的力度。
type CueParams struct {
	Phi   float64 `json:"phi"`
	Power float64 `json:"power"`
	Theta float64 `json:"theta"`
	A     float64 `json:"a"`
	B     float64 `json:"b"`
}

// Request 是 POST /simulate 的请求体。
type Request struct {
	Table    string        `json:"table,omitempty"`
	Balls    []RequestBall `json:"balls"`
	Cue      CueParams     `json:"cue"`
	SampleHz int           `json:"sample_hz,omitempty"`
}

// Event 是一次碰撞。A / B 的含义随 Type 变化：
// 出杆是 (球杆, 母球)，球撞球是两个球号，撞库是 (球号, 库边名)，落袋是 (球号, 袋口名)。
type Event struct {
	T    float64 `json:"t"`
	Type string  `json:"type"`
	A    string  `json:"a"`
	B    string  `json:"b"`
}

// Frame 是一帧关键帧。
//
// Balls 原样保留成 json.RawMessage：它的形状是 [球号, x, y, z] 的混合类型数组，
// Go 侧从不解构它——规则不看轨迹，广播与落库都是整段透传。
// 解成 []any 只会多两次无意义的分配，还容易在浮点转字符串时丢掉尾数。
type Frame struct {
	T     float64         `json:"t"`
	Balls json.RawMessage `json:"balls"`
}

// ResultBall 是终局里的一颗球。OnTable 为 false 表示已经落袋。
type ResultBall struct {
	ID      string  `json:"id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Z       float64 `json:"z"`
	OnTable bool    `json:"on_table"`
}

// Shot 是一次仿真的完整结果。
type Shot struct {
	Duration float64      `json:"duration"`
	Events   []Event      `json:"events"`
	Final    []ResultBall `json:"final"`
	Frames   []Frame      `json:"frames"`
	SimMS    int          `json:"sim_ms"`
}

// Client 是 pool-sim 的客户端，进程内共用一个实例。
type Client struct {
	baseURL string
	http    *http.Client
}

// 开球是最重的一杆（十六颗球、几百次接触），实测一秒半。
// 十秒是给机器抖动留的余量，再长说明 pool-sim 出了问题，不如早点失败让玩家重试。
const shootTimeout = 10 * time.Second

// 失败重试一次的间隔。不重试的坏处是偶发的一次抖动被判成服务故障；
// 重试超过一次则会把一个已经超载的 pool-sim 压得更死。
const retryDelay = 200 * time.Millisecond

// NewClient 由装配层创建。baseURL 形如 http://pool-sim:8000。
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: shootTimeout,
			// 不复用连接是浪费：一局每二十秒一次请求，keep-alive 让这二十秒
			// 里那条空闲连接一直占着 pool-sim 的连接数。默认 Transport 就够。
		},
	}
}

// Shoot 算一杆。网络错误与 5xx 会重试一次，4xx 直接返回 ErrRejected。
func (c *Client) Shoot(ctx context.Context, req Request) (*Shot, error) {
	shot, err := c.shootOnce(ctx, req)
	if err == nil {
		return shot, nil
	}
	// 只有「可能是一次抖动」的失败才重试。请求被拒重试一百次也是被拒。
	if !errors.Is(err, ErrUnavailable) {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, err
	case <-time.After(retryDelay):
	}
	return c.shootOnce(ctx, req)
}

// shootOnce 发一次请求并解析响应。
func (c *Client) shootOnce(ctx context.Context, req Request) (*Shot, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal simulate request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/simulate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build simulate request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	// 响应体要读完再关：直接 Close 会让这条连接没法复用，
	// 每次出杆都新建一条 TCP 连接对 pool-sim 是额外负担。
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusBadRequest {
		return nil, fmt.Errorf("%w: status %d", ErrRejected, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}

	var shot Shot
	if err := json.NewDecoder(resp.Body).Decode(&shot); err != nil {
		// 解不动响应说明两边协议不一致，属于部署问题：把它算成不可用，
		// 让对局暂停并提示重试，比带着半个结果继续算规则安全。
		return nil, fmt.Errorf("%w: decode response: %v", ErrUnavailable, err)
	}
	return &shot, nil
}
