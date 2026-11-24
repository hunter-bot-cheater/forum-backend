package pool

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"forum-backend/internal/model"
	"forum-backend/internal/pool/sim"
)

// 对局层对外返回的错误。handler 按这些哨兵值映射成错误码与 HTTP 状态。
var (
	ErrNotPlaying    = errors.New("match is not playing")
	ErrNotYourTurn   = errors.New("not your turn")
	ErrBadParams     = errors.New("shot params out of range")
	ErrSimFailed     = errors.New("simulation failed")
	ErrNotBallInHand = errors.New("ball in hand is not available")
	ErrMustPlaceCue  = errors.New("cue ball must be placed first")
	ErrSpectatorFull = errors.New("too many spectators")
	ErrMatchClosed   = errors.New("match is closed")
)

// maxSpectators 是一局最多同时挂着的观战连接数。
//
// 限的是连接不是人：同一个人开两个标签页就是两条。
// 上限的意义是别让一场热门对局把整个进程的连接与发件箱预算吃光——
// 每条观战连接都要收完整的 shot 帧（一杆约 60KB），十条就是 600KB 一次广播。
const maxSpectators = 10

// Conn 是对局连接的最小接口。
//
// 只要求「投递一条消息」和「关掉」，不暴露 WebSocket：
// actor 不该知道网络的细节，测试里也就能塞一个记录消息的假连接进去。
type Conn interface {
	// Send 把消息放进连接的发送队列，返回 false 表示队列已满（对方读不动）。
	Send(msg Message) bool
	Close()
}

// Recorder 负责把每一杆与结算写进库。传 NopRecorder 表示纯内存（联调与测试用）。
type Recorder interface {
	SaveShot(ctx context.Context, record ShotRecord) error
	FinishMatch(ctx context.Context, record MatchRecord) error
}

// ShotRecord 是一杆的落库内容。
type ShotRecord struct {
	MatchID   uint
	Seq       int
	ShooterID uint
	Seat      Seat
	Params    sim.CueParams
	Events    []sim.Event
	Frames    []sim.Frame
	Duration  float64
	SimMS     int
}

// MatchRecord 是结算的落库内容。
//
// 带的是赢家与输家的用户 id 而不是座位号：落库与算分都只认用户，
// 座位号出了这一局就没有意义了。
type MatchRecord struct {
	MatchID   uint
	Winner    Seat
	WinnerID  uint
	LoserID   uint
	WinReason string
	Shots     int
	EndedAt   time.Time
}

// NopRecorder 什么都不写。用于测试与尚未接库的阶段。
type NopRecorder struct{}

// SaveShot 什么也不做。
func (NopRecorder) SaveShot(context.Context, ShotRecord) error { return nil }

// FinishMatch 什么也不做。
func (NopRecorder) FinishMatch(context.Context, MatchRecord) error { return nil }

// Simulator 是 actor 需要的仿真能力。
//
// 用接口而不是直接依赖 *sim.Client：测试里塞一个返回固定轨迹的假实现，
// actor 的用例就不用起 Python 服务，跑一遍几百毫秒。
type Simulator interface {
	Shoot(ctx context.Context, req sim.Request) (*sim.Shot, error)
}

// Options 是创建一局需要的依赖与参数。
type Options struct {
	Sim         Simulator
	Recorder    Recorder
	TurnTimeout time.Duration
	// Series 是「同一对玩家连续对局」的比分账本，由装配层创建后共用一份。
	// 同一局里它只被读两次（开局读初始比分、结算时记一局胜负），
	// 所以不需要进 actor 的串行通道——Series 自己带锁。
	Series *Series
}

// 一杆的读秒时长。职业比赛常见 30 秒，这里留 40 秒：
// 手机端拖动瞄准本来就慢，加上开球时服务端要算一秒半。
const DefaultTurnTimeout = 40 * time.Second

type cmdKind int

const (
	cmdJoin cmdKind = iota
	cmdShoot
	cmdPlaceCue
	cmdAim
	cmdChat
	cmdSnapshot
	cmdReady
	cmdLeave
	cmdDisconnect
	cmdWatch
	cmdFillSeat
	// cmdBotShoot 由机器人的定时器自己投进来，不经过任何连接。
	cmdBotShoot
	cmdClose
)

// command 是投给 actor 的一条命令。
//
// reply 与 snapshot 只会用到其中一个：带返回值的命令才需要它们，
// 做成两个通道而不是一个 any，是为了让调用方不必做类型断言。
type command struct {
	kind   cmdKind
	seat   Seat
	conn   Conn
	brief  PlayerBrief
	params sim.CueParams
	// shot 是客户端随出杆一起上来的整杆结果，nil 表示这份请求没带
	// （老客户端），由 handleShoot 退回 pool-sim 现算。
	shot *sim.Shot
	// text 是聊天正文，只有 cmdChat 用。
	text  string
	seq   int
	x, y  float64
	reply chan error
	state chan Snapshot
}

// LiveInfo 是观战列表要看的那一小份数据。
//
// 单独拎出来是因为它必须能在**不打扰 actor** 的前提下读到：
// 一局正在算杆时 actor 被占着一两秒，而大厅那个列表每十秒就要拉一次、
// 还要一次问几十局。走命令往返的话，这个列表就会被某一局拖住。
type LiveInfo struct {
	MatchID uint
	Status  string
	Turn    Seat
	// Breaker 是这一局谁开的球。「再约一局」靠它决定下一局换谁开球。
	Breaker Seat
	ShotSeq int
	Players [2]PlayerBrief
}

// Match 是一局对局。所有状态只在 run() 这条协程里读写。
type Match struct {
	opts Options
	log  *slog.Logger

	cmd       chan command
	done      chan struct{}
	closeOnce sync.Once

	// 只由 run() 访问，外面读状态一律走 Snapshot（命令往返）。
	state      MatchState
	conns      [2]Conn
	spectators []Conn

	// live 是观战列表要读的那份摘要，由 actor 每条命令处理完发布一次。
	// 外面读它不需要命令往返，所以永远不会被「正在算杆」挡住。
	live atomic.Pointer[LiveInfo]

	// spectatorCount 是 spectators 的原子镜像，只给「观战是不是满了」这个判断用。
	// 走命令往返当然也行，但握手前的预检每来一个人就要排一次队，
	// 而这个数只用于「明显满了就早点拒掉」，读到略旧的值无害。
	spectatorCount int32

	// 结束时间给 Manager 的清扫用。单独加锁而不是走命令往返：
	// 后台清扫每秒扫一遍全部对局，为一局多排一次命令没有意义。
	finishedAtMu sync.Mutex
	finishedAt   time.Time

	// lastActive 是最近一次有人跟这局说话的时刻（UnixNano），给「房间是不是没人理了」用。
	// 用原子量而不是走命令往返：清扫是后台动作，为一局排一次命令不值得。
	lastActive int64

	// 聊天：lastChat 按座位记上一次发言的时刻，用来挡连发；chatSeq 是消息编号。
	// 两者都只在 run() 里读写，不需要锁。
	lastChat [2]time.Time
	chatSeq  int64

	// botTimer 是机器人「想一想再出杆」的定时器，没有机器人或不该它走时为空。
	// 只在 actor 里复位，回调只负责把一条 cmdBotShoot 投回来，
	// 所以它既不会和玩家抢状态，也不会在 actor 退出后留下一个还在写状态的协程。
	botTimer *time.Timer
}

// NewMatch 创建并启动一局。state 里的球位、座位与玩家资料由调用方准备好。
func NewMatch(state MatchState, opts Options) *Match {
	if opts.TurnTimeout <= 0 {
		opts.TurnTimeout = DefaultTurnTimeout
	}
	if opts.Recorder == nil {
		opts.Recorder = NopRecorder{}
	}
	// 比分账本缺省补一份空的：单局测试与「只开一局」的调用方不必关心它，
	// 而装配层给的那一份会让同一对玩家的多局共享同一个账本。
	if opts.Series == nil {
		opts.Series = NewSeries()
	}

	match := &Match{
		opts: opts,
		log:  slog.With("match_id", state.MatchID),
		// 容量 64：一局里同时挂着的命令不会多，但开球那一杆要占着一秒半，
		// 这期间对方的快照请求、离开、超时都得排得下。
		cmd:  make(chan command, 64),
		done: make(chan struct{}),
	}
	match.state = state
	match.state.Deadline = time.Now().Add(opts.TurnTimeout)
	// 先记一次活跃再发布摘要：房间刚建好就没人理的话，清扫要能算出「已经放了多久」。
	match.touch()
	// 先发布一份初始摘要再起协程：房间阶段的对局也可能被列表读到。
	match.publishLive()

	go match.run()
	return match
}

// publishLive 把当前状态里的几个字段抄进 atomic 指针。只在 actor 里调用。
// 每条命令处理完都发布一次，而不是只在换人时发：
// 球的序号、状态（开局 / 结束）都会变，漏发就会让列表显示上一杆的序号。
func (m *Match) publishLive() {
	m.live.Store(&LiveInfo{
		MatchID: m.state.MatchID,
		Status:  m.state.Status,
		Turn:    m.state.Turn,
		Breaker: m.state.Breaker,
		ShotSeq: m.state.ShotSeq,
		Players: m.state.Players,
	})
}

// Live 读最近一次发布的摘要。第二个返回值为 false 表示这一局已经关掉。
//
// 不加锁、不进队列：它读的是一个原子指针，被丢弃的那个旧结构由 GC 回收。
func (m *Match) Live() (LiveInfo, bool) {
	select {
	case <-m.done:
		return LiveInfo{}, false
	default:
	}
	info := m.live.Load()
	if info == nil {
		return LiveInfo{}, false
	}
	return *info, true
}

// Join 把一条连接绑到座位上。同一座位重连会顶掉旧连接。
func (m *Match) Join(seat Seat, conn Conn) error {
	return m.call(command{kind: cmdJoin, seat: seat, conn: conn})
}

// Watch 把一条连接挂成观战者：能收全部广播，不能上行任何操作。
//
// 观战不占座位、不参与读秒，也不影响对局是否可以开始。
func (m *Match) Watch(conn Conn) error {
	return m.call(command{kind: cmdWatch, conn: conn})
}

// SpectatorCount 返回当前观战连接数。观战列表拿它算「还有几个位置」，
// 名额本身由 handleWatch 在 actor 里判。
func (m *Match) SpectatorCount() int {
	return int(atomic.LoadInt32(&m.spectatorCount))
}

// Shoot 出一杆。
//
// shot 是客户端算好的这一杆：服务端不再自己跑物理，直接采用它并按规则判定。
// 传 nil 表示老客户端没带结果，退回 pool-sim 现算。
// 会一直阻塞到这一杆处理完（本地结果时只是校验加广播，几十微秒）。
func (m *Match) Shoot(seat Seat, params sim.CueParams, shot *sim.Shot, seq int) error {
	return m.call(command{kind: cmdShoot, seat: seat, params: params, shot: shot, seq: seq})
}

// PlaceCue 摆放手中球。
func (m *Match) PlaceCue(seat Seat, x, y float64) error {
	return m.call(command{kind: cmdPlaceCue, seat: seat, x: x, y: y})
}

// RelayAim 把一方的瞄准姿态转给对面与观战者。
//
// 与 Shoot 不同，它不等回执、也不占用调用方：瞄准是每几十毫秒一帧的连续动作，
// actor 正在算一杆（开球要一两秒）时宁可丢掉这几帧，也不能让 WS 读循环堵在那里，
// 那会把对端的 uptime 与心跳一起拖垮。丢帧的代价只是对面看到的杆子顿一下。
//
// a、b 是击球点（左右塞、高低杆），与角度、力度同帧搬运，对面据此画出
// 同一颗球面点位——加塞是「这一杆怎么打」的一部分，晚一帧就看不到真动作了。
func (m *Match) RelayAim(seat Seat, phi, power, a, b float64) {
	select {
	case <-m.done:
		return
	case m.cmd <- command{kind: cmdAim, seat: seat, params: sim.CueParams{Phi: phi, Power: power, A: a, B: b}}:
	default:
		// actor 正忙，丢掉这一帧。
	}
}

// handleAim 广播一条瞄准帧。
//
// 只转发不落库、不进快照：它随时会被下一帧覆盖，重连的人从下一帧开始看就行。
// 轮次对不上的一律丢弃——否则对手停在自己回合时画出的杆子会一直挂在那边。
func (m *Match) handleAim(cmd command) {
	if m.state.Status != model.PoolStatusPlaying || cmd.seat != m.state.Turn {
		return
	}
	m.broadcastExcept(cmd.seat, m.wrap(MsgAim, AimData{
		Seat:  cmd.seat,
		Phi:   cmd.params.Phi,
		Power: cmd.params.Power,
		A:     cmd.params.A,
		B:     cmd.params.B,
	}))
}

// RelayChat 把一条聊天投给 actor，由它发号并转给对手与观战者。
//
// 与 RelayAim 一样用非阻塞投递：actor 正在算一杆时宁可丢掉这一句，
// 也不能让 WS 读循环卡在那里。丢一句聊天的代价就是对方没看见，重发即可。
func (m *Match) RelayChat(seat Seat, text string) {
	select {
	case <-m.done:
		return
	case m.cmd <- command{kind: cmdChat, seat: seat, text: text}:
	default:
	}
}

// handleChat 广播一条聊天。
//
// 只转发给**别人**：发送方在自己那边已经把这条消息落进列表了（本地回显），
// 再回一份会让同一句话闪一下。观战者照发，他们看的也是这块屏。
//
// 两个闸门都在这里：空消息直接丢（前端也会拦，但客户端不可信），
// 同一座位两条之间至少隔 chatInterval，防止按住回车刷屏。
// 不满足就静默丢弃——为一句闲聊回一条错误只会让面板上多一行红字。
func (m *Match) handleChat(cmd command) {
	if cmd.text == "" {
		return
	}
	now := time.Now()
	if !m.lastChat[cmd.seat].IsZero() && now.Sub(m.lastChat[cmd.seat]) < chatInterval {
		return
	}
	m.lastChat[cmd.seat] = now
	m.chatSeq++

	m.broadcastExcept(cmd.seat, m.wrap(MsgChat, ChatData{
		Seat: cmd.seat,
		Text: cmd.text,
		ID:   m.chatSeq,
		TS:   now.UnixMilli(),
	}))
}

// Leave 主动退出，判负。
func (m *Match) Leave(seat Seat) error {
	return m.call(command{kind: cmdLeave, seat: seat})
}

// ID 返回对局编号。房间号与编号建好之后就不再变，所以在 actor 外面直读。
func (m *Match) ID() uint {
	return m.state.MatchID
}

// RoomCode 返回房号。与 ID 同理：建好之后不变，HTTP 层直接读，不必排队等 actor。
func (m *Match) RoomCode() string {
	return m.state.RoomCode
}

// FillSeat 把某个座位上的玩家资料补上。房间刚有人进来时调用。
//
// 只改资料、不动座位归属：座位 1 空着这件事在库里是 p1_id 为 NULL，
// 在内存里就是 Players[1] 的两项都是零值。
func (m *Match) FillSeat(seat Seat, brief PlayerBrief) error {
	return m.call(command{kind: cmdFillSeat, seat: seat, brief: brief})
}

// SetReady 设置某个座位的准备状态。双方都准备好时对局自动开局。
//
// 只允许在 waiting 阶段改：开局之后再点准备没有意义，
// 放过去会让「准备」这个动作看起来能撤销，实际上什么都不做。
func (m *Match) SetReady(seat Seat, ready bool) error {
	return m.call(command{kind: cmdReady, seat: seat, x: boolToFloat(ready)})
}

// boolToFloat 把准备标记塞进命令的 x 字段，省一个只用于一个命令的字段。
func boolToFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// NotifyDisconnect 告诉对局某个座位的连接断了。
//
// 断线不判负：对局继续按读秒走，重连上来会拿到完整快照。
// 这里只做两件事：把这个连接从登记表摘掉（如果它还是当前连接），
// 以及通知对手「对方掉线了」，让对方别以为是自己卡住。
func (m *Match) NotifyDisconnect(seat Seat, conn Conn) error {
	return m.call(command{kind: cmdDisconnect, seat: seat, conn: conn})
}

// Snapshot 取一份快照。viewer 是看的人坐在哪；-1 表示观战。
func (m *Match) Snapshot(viewer Seat) (Snapshot, error) {
	reply := make(chan Snapshot, 1)
	select {
	case m.cmd <- command{kind: cmdSnapshot, seat: viewer, state: reply}:
	case <-m.done:
		return Snapshot{}, ErrMatchClosed
	}

	select {
	case snapshot := <-reply:
		return snapshot, nil
	case <-m.done:
		return Snapshot{}, ErrMatchClosed
	}
}

// Close 停掉 actor 并断开所有连接。
func (m *Match) Close() {
	m.closeOnce.Do(func() {
		select {
		case m.cmd <- command{kind: cmdClose}:
		case <-m.done:
		}
	})
}

// Done 返回一个在 actor 退出后关闭的通道。
func (m *Match) Done() <-chan struct{} {
	return m.done
}

// call 投递一条命令并等它的结果。
func (m *Match) call(cmd command) error {
	cmd.reply = make(chan error, 1)
	select {
	case m.cmd <- cmd:
	case <-m.done:
		return ErrMatchClosed
	}

	select {
	case err := <-cmd.reply:
		return err
	case <-m.done:
		return ErrMatchClosed
	}
}

// run 是 actor 主循环：串行处理命令与读秒，退出时收尾。
func (m *Match) run() {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	defer close(m.done)

	for {
		select {
		case cmd := <-m.cmd:
			if cmd.kind == cmdClose {
				m.shutdown()
				return
			}
			// 任何一条命令都算「有人在这局上」：大厅与房间页是纯 HTTP 轮询，
			// 没有长连接，光看连接数分不出「有人守着」与「人早走了」。
			m.touch()
			m.handle(cmd)
			m.publishLive()
			m.armTimer(timer)
		case <-timer.C:
			m.handleTimeout()
			m.publishLive()
			m.armTimer(timer)
		}
	}
}

// armTimer 按当前读秒截止时间重设计时器。
//
// 每次处理完命令都重设一次，而不是只在换人时设：一杆打完可能同一个人继续打，
// 那也需要重新读秒，否则会被上一杆的截止时间立刻判超时。
func (m *Match) armTimer(timer *time.Timer) {
	if m.state.Status != model.PoolStatusPlaying || m.state.Deadline.IsZero() {
		timer.Stop()
		return
	}
	wait := time.Until(m.state.Deadline)
	if wait < 0 {
		wait = 0
	}
	timer.Reset(wait)
}

// handle 分发一条命令。所有状态改动都发生在这里。
func (m *Match) handle(cmd command) {
	switch cmd.kind {
	case cmdJoin:
		cmd.reply <- m.handleJoin(cmd)
	case cmdWatch:
		cmd.reply <- m.handleWatch(cmd)
	case cmdShoot:
		cmd.reply <- m.handleShoot(cmd)
	case cmdPlaceCue:
		cmd.reply <- m.handlePlaceCue(cmd)
	case cmdAim:
		// 瞄准帧没有回执：进来只为走一遍单写者约束，让广播与状态改动同源。
		m.handleAim(cmd)
	case cmdChat:
		// 聊天同样没有回执：发不出去时（限流、空串）静默丢弃即可，
		// 为一句闲聊回一条错误只会让面板上多一行红字。
		m.handleChat(cmd)
	case cmdSnapshot:
		cmd.state <- m.state.snapshot(cmd.seat, time.Now())
	case cmdLeave:
		cmd.reply <- m.handleLeave(cmd)
	case cmdReady:
		cmd.reply <- m.handleReady(cmd)
	case cmdFillSeat:
		cmd.reply <- m.handleFillSeat(cmd)
	case cmdDisconnect:
		cmd.reply <- m.handleDisconnect(cmd)
	case cmdBotShoot:
		// 机器人没有连接，也就没人等回执；错误只进日志。
		cmd.reply <- m.handleBotShoot()
	}
}

// handleJoin 绑定连接并把完整快照发给它。
//
// 先发 joined 再发 state：joined 里有座位号，前端要靠它决定自己在左边还是右边；
// 顺序发反的话，客户端拿到快照时还不知道自己是谁。
func (m *Match) handleJoin(cmd command) error {
	if !cmd.seat.Valid() {
		return ErrBadParams
	}
	// 同一座位重复进房视为重连：顶掉旧连接，不报错。
	if old := m.conns[cmd.seat]; old != nil && old != cmd.conn {
		old.Close()
	}
	m.conns[cmd.seat] = cmd.conn

	cmd.conn.Send(m.wrap(MsgJoined, JoinedData{
		MatchID:  m.state.MatchID,
		RoomCode: m.state.RoomCode,
		Seat:     cmd.seat,
	}))
	cmd.conn.Send(m.wrap(MsgState, m.state.snapshot(cmd.seat, time.Now())))

	// 通知对手：这人回来了。
	if peer := m.conns[cmd.seat.Other()]; peer != nil {
		peer.Send(m.wrap(MsgPeer, PeerData{Event: PeerJoined, Seat: cmd.seat}))
	}
	return nil
}

// handleWatch 接纳一名观战者：先回 joined（座位是 SeatSpectator）再发快照。
//
// 快照的视角同样用 SeatSpectator：观战者若拿到某一方的视角，
// 界面上会把那一方的球当成「我的球」，看起来像坐错了座位。
func (m *Match) handleWatch(cmd command) error {
	if len(m.spectators) >= maxSpectators {
		return ErrSpectatorFull
	}
	m.spectators = append(m.spectators, cmd.conn)
	atomic.StoreInt32(&m.spectatorCount, int32(len(m.spectators)))

	cmd.conn.Send(m.wrap(MsgJoined, JoinedData{
		MatchID:  m.state.MatchID,
		RoomCode: m.state.RoomCode,
		Seat:     SeatSpectator,
	}))
	cmd.conn.Send(m.wrap(MsgState, m.state.snapshot(SeatSpectator, time.Now())))
	return nil
}

// removeSpectator 按连接身份摘掉一个观战者。
//
// 不按下标删：同一个人重连时旧连接的回调可能晚到，
// 那时它在切片里的位置已经被别人占了，按下标删会误伤后来者。
func (m *Match) removeSpectator(conn Conn) {
	for index, item := range m.spectators {
		if item == conn {
			m.spectators = append(m.spectators[:index], m.spectators[index+1:]...)
			break
		}
	}
	atomic.StoreInt32(&m.spectatorCount, int32(len(m.spectators)))
}

// handleShoot 算一杆并应用规则结论。这是整个 actor 里唯一会阻塞的地方。
func (m *Match) handleShoot(cmd command) error {
	if m.state.Status != model.PoolStatusPlaying {
		return ErrNotPlaying
	}
	if cmd.seat != m.state.Turn {
		return ErrNotYourTurn
	}
	if err := validateParams(cmd.params); err != nil {
		return err
	}
	// 只有母球不在台上（刚摔袋）才必须先落位：那时它的坐标还在袋里，
	// z 为负，直接送进物理会算不出结果。母球好端端摆在台上时，
	// 「手中球」的意思是「可以挪」，不是「必须挪」——拦着不让打，
	// 玩家看到台上明明有颗白球却被要求「先把母球放到台面上」，只会当成 bug。
	if m.state.BallInHand && !m.state.CuePlaced && !m.state.cueOnTable() {
		return ErrMustPlaceCue
	}

	// 这一杆的物理结果优先用客户端算好的那一份。
	//
	// 客户端为此在本地跑了一个二维模型（web/src/game/predict.ts）：松手那一刻
	// 画面就开始动，不用等一个来回。如果服务端再算一遍，两边落点必然不同，
	// 只能靠补间把球滑过去，玩家看到的就是「击球瞬间卡一下、球堆整体挪位」。
	// 采用客户端的结果之后，服务端状态与出杆方的画面出自同一次计算，
	// 对面收到的也是同一份帧，不存在谁在追谁。
	shot := cmd.shot
	if shot != nil {
		if err := validateShotPayload(shot, m.state.simBalls()); err != nil {
			return err
		}
		// 这份结果不是服务端算的，耗时一律记 0：客户的数字混进 sim_ms，
		// 「物理服务到底慢不慢」这个指标就彻底失真了。
		shot.SimMS = 0
	} else {
		var err error
		shot, err = m.simulate(cmd.params)
		if err != nil {
			return err
		}
	}

	m.playShot(cmd.seat, cmd.params, shot)
	return nil
}

// simulate 让物理服务算一杆。
//
// 调用方是 actor，所以这里可以阻塞——一次开球要一两秒，
// 这期间别人的命令排在队列里等着，与玩家出杆时完全一样。
func (m *Match) simulate(params sim.CueParams) (*sim.Shot, error) {
	shot, err := m.opts.Sim.Shoot(context.Background(), sim.Request{
		Balls:    m.state.simBalls(),
		Cue:      params,
		SampleHz: 30,
	})
	if err != nil {
		// 这里只记日志并返回错误：错误帧由 WS 层按 error 码统一回一条。
		// 两处都发的话，玩家会看到两条一模一样的「球桌暂时算不动」。
		m.log.Error("simulate shot", "error", err)
		return nil, ErrSimFailed
	}
	return shot, nil
}

// ShotPlayRate 是前端播放一杆轨迹的倍速，必须与 web/src/game/animator.ts
// 的 SHOT_PLAY_RATE 一致。
//
// 服务端算完一杆就把帧发走了，客户端却是按这个倍速（0.8，比物理慢）慢慢放，
// 所以「这一杆在我们这边还要播多久」= 物理时长 ÷ 这个数。
const ShotPlayRate = 0.8

// 机器人轮到自己之后「想一想」的时间。
//
// 这是**在球停稳之后**再加的一段：不给的话，最后一颗球刚停，对面的杆
// 就从另一个方向冒出来，看起来像球被瞬移了。
//
// 出杆前的等待还必须叠加「上一杆在客户端播了多久」（见 shotPlayDelay）：
// 只在服务端计时的话，机器人会在玩家屏幕里球还在滚的时候出杆——
// 那时客户端还有一杆动画没放完，两杆的帧会叠在一起，画面直接错位。
const (
	BotThinkDelay  = 1000 * time.Millisecond
	BotThinkJitter = 300 * time.Millisecond
)

// shotPlayDelay 把一杆的物理时长换算成客户端播完它需要的真实时间。
//
// shot.Duration 是秒（pool-sim 的口径），前端 0.8 倍速播放，
// 所以真实耗时是它的 1.25 倍。空轨迹返回 0，不额外拖时间。
func shotPlayDelay(shot *sim.Shot) time.Duration {
	if shot == nil || shot.Duration <= 0 {
		return 0
	}
	return time.Duration(shot.Duration / ShotPlayRate * float64(time.Second))
}

// armBot 在轮到机器人时安排一杆，其他时候把定时器撤掉。
//
// after 是「刚才那一杆在客户端还要播多久」：playShot 结尾传 shotPlayDelay(shot)，
// 开局与超时这两种没有新轨迹的情况传 0。机器人真正出杆的时刻 =
// after + BotThinkDelay + 随机抖动，所以它的杆总是落在玩家看到球停稳之后。
//
// 只由 actor 调用。定时器回调只做一件事：把 cmdBotShoot 投回 actor，
// 所以所有状态改动仍然只发生在 run() 这一条协程里——
// 让回调直接算杆的话，它读到的球位可能正被别的命令改到一半。
func (m *Match) armBot(after time.Duration) {
	if m.botTimer != nil {
		m.botTimer.Stop()
		m.botTimer = nil
	}
	if m.state.Status != model.PoolStatusPlaying || m.state.Turn != m.state.Bot {
		return
	}
	delay := after + BotThinkDelay + time.Duration(rand.Int63n(int64(BotThinkJitter)))
	m.botTimer = time.AfterFunc(delay, func() {
		if err := m.call(command{kind: cmdBotShoot}); err != nil {
			// 对局已经关掉是正常路径（玩家退出了、被清扫了），不值得记一条告警。
			if !errors.Is(err, ErrMatchClosed) {
				m.log.Warn("bot shoot", "error", err)
			}
		}
	})
}

// handleBotShoot 让机器人出一杆。
//
// 流程与玩家出杆一致：先校验轮次，必要时落位母球，然后算参数、交给物理服务、
// 走同一套规则与应用。唯一的区别是物理结果由服务端算，因为机器人没有客户端。
func (m *Match) handleBotShoot() error {
	if m.state.Status != model.PoolStatusPlaying || m.state.Turn != m.state.Bot {
		return nil
	}

	// 母球不在台上（上一杆摔袋）时必须先落位：那颗球的坐标还在袋里、z 为负，
	// 直接送进物理会算不出结果。
	if !m.state.cueOnTable() {
		x, y, ok := m.botCueSpot()
		if !ok {
			m.log.Warn("bot has nowhere to place the cue ball")
			return nil
		}
		if err := m.handlePlaceCue(command{seat: m.state.Bot, x: x, y: y}); err != nil {
			m.log.Warn("bot place cue", "error", err)
			return nil
		}
	}

	plans := m.botPlans()
	if len(plans) == 0 {
		// 一颗能打的球都没有（比如台面上只剩对手的球）：等读秒判超时，
		// 总比硬打一杆空杆、白白送自由球给玩家强。
		m.log.Warn("bot has no plan", "match_id", m.state.MatchID)
		return nil
	}

	// 逐套试。算出来的线看着能进，物理上仍可能因为切得太薄、球挤在一起
	// 而先碰到别的球；这时换下一套，比直接把自由球送给玩家体面。
	var (
		fallback       *sim.Shot
		fallbackParams sim.CueParams
	)
	for _, plan := range plans {
		shot, err := m.simulate(plan.params)
		if err != nil {
			return err
		}
		if fallback == nil {
			fallback, fallbackParams = shot, plan.params
		}
		if JudgeShot(m.state.ruleState(), shot).Foul {
			continue
		}
		m.playShot(m.state.Bot, plan.params, shot)
		return nil
	}

	// 每套都犯规：认罚打完第一套。停在这里的话这一杆就永远打不出去，
	// 玩家会对着一个不动的球桌等四十秒。
	m.log.Warn("bot falls back to a foul shot", "match_id", m.state.MatchID)
	m.playShot(m.state.Bot, fallbackParams, fallback)
	return nil
}

// playShot 把一杆的结果与规则结论落到状态上，再广播出去。
//
// 玩家出杆与机器人出杆共用这一段：两边唯一的差别是「谁算的物理」，
// 而从「一杆结果」往后的一切（规则、换人、落库、下一杆归谁）必须一模一样，
// 否则人机对局会跑出一条真人局没有的路径。
func (m *Match) playShot(seat Seat, params sim.CueParams, shot *sim.Shot) {
	verdict := JudgeShot(m.state.ruleState(), shot)
	m.applyShot(shot, verdict)

	// 广播结果：轨迹给所有人，犯规与结算各自带一条，前端按顺序播。
	shotData := ShotData{
		Seq:      m.state.ShotSeq,
		Seat:     seat,
		Params:   params,
		Duration: shot.Duration,
		Events:   shot.Events,
		Frames:   shot.Frames,
		Final:    shot.Final,
		SimMS:    shot.SimMS,
	}
	m.broadcast(m.wrap(MsgShot, shotData))

	if verdict.Foul {
		m.broadcast(m.wrap(MsgFoul, FoulData{
			Code:       verdict.ReasonCode,
			Reason:     verdict.Reason,
			BallInHand: verdict.BallInHand,
			Seat:       seat,
		}))
	}

	if verdict.Winner != nil {
		m.broadcast(m.wrap(MsgResult, ResultData{
			Winner: *verdict.Winner,
			Reason: verdict.WinReason,
			Series: m.state.Series,
		}))
	} else {
		m.broadcast(m.wrap(MsgTurn, TurnData{
			Seat:       m.state.Turn,
			DeadlineMS: m.remainingMS(),
		}))
	}

	// 再补一份完整快照。
	//
	// 上面那几条帧只带增量：shot 有轨迹，foul 有文案，turn 有读秒，
	// 都不含「定组结果」与「手中球」这两项。少了这份快照，
	// 前端在第一次进球之后仍然显示「未定组」，犯了规也拿不到 ball_in_hand，
	// 于是没法落位母球，只能反复出杆被服务端拒掉——对局就卡死在这里。
	//
	// 前端在动画播放期间会把这条快照压住，播完再应用（见 match.vue 的缓冲队列）：
	// 直接应用会把球瞬移到终局位置，看起来像跳帧。
	m.broadcastState()

	// 落库失败不影响对局继续：玩家已经在打下一杆了，
	// 这里只记日志，回放缺一杆比当场卡住轻得多。
	if err := m.opts.Recorder.SaveShot(context.Background(), ShotRecord{
		MatchID:   m.state.MatchID,
		Seq:       m.state.ShotSeq,
		ShooterID: m.state.Players[seat].UserID,
		Seat:      seat,
		Params:    params,
		Events:    shot.Events,
		Frames:    shot.Frames,
		Duration:  shot.Duration,
		SimMS:     shot.SimMS,
	}); err != nil {
		m.log.Error("save shot", "seq", m.state.ShotSeq, "error", err)
	}

	// 这一杆打完，轮次可能已经交到机器人手上，就在这里把它的定时器装上。
	// 放在 playShot 而不是各个调用点：玩家出杆、机器人出杆、超时换人
	// 三条路都会走到这里，漏掉任何一条，机器人都会停在那儿等到超时。
	//
	// 等这一杆在我们这边播完再起算：客户端是 0.8 倍速放的，
	// 服务端早就算完了，不加这段延迟机器人会在球还在滚的时候出杆。
	m.armBot(shotPlayDelay(shot))
}

// applyShot 把一杆的物理结果与规则结论落到状态上。
func (m *Match) applyShot(shot *sim.Shot, verdict Verdict) {
	// 击球方在本杆结束前还是 state.Turn，换人在最后一步做。
	shooter := m.state.Turn

	// 终局球位：pool-sim 会把送进去的每颗球都报回来，落袋的带 on_table=false。
	for _, ball := range shot.Final {
		m.state.setBall(ball)
	}

	// 开球把 8 号打进要重摆，放回三角形中心那个槽位。
	// 按槽位取坐标而不是按球号去 StandardRack 里找：那张表里的球号只是占位，
	// 每局真正的球号是 rackBalls 排的，只有槽位坐标是恒定的。
	if verdict.ReSpotEight {
		slot := StandardRack[eightBallSlot+1] // +1：第一项是母球
		m.state.setBall(sim.ResultBall{ID: sim.EightBallID, X: slot.X, Y: slot.Y, OnTable: true})
	}

	// 定组：击球方得到本杆第一颗落袋球所属的组，对手自动拿另一组。
	if verdict.AssignedGroup != GroupNone {
		m.state.Groups[shooter] = verdict.AssignedGroup
		m.state.Groups[shooter.Other()] = verdict.AssignedGroup.Other()
	}

	m.state.BreakDone = true
	m.state.ShotSeq++
	m.state.BallInHand = verdict.BallInHand
	// 开球已经打出去了，之后的自由球都是整张台面：开球线的限制只属于第一杆。
	m.state.PlaceZone = PlaceAnywhere
	// 新的一次手中球要重新摆：上一杆那颗球的位置和落袋状态都不作数了。
	m.state.CuePlaced = false
	m.state.Turn = verdict.NextSeat
	// 这一杆打出来了，这个人的连续超时计数归零。
	m.state.Timeouts[shooter] = 0

	if verdict.Winner != nil {
		m.state.Status = model.PoolStatusFinished
		m.state.Winner = verdict.Winner
		m.state.WinReason = verdict.WinReason
		m.state.Deadline = time.Time{}
		m.finish(verdict.WinReason)
		return
	}
	m.state.Deadline = time.Now().Add(m.opts.TurnTimeout)
}

// handlePlaceCue 摆放手中球。
//
// 只校验「在台面内且没压住别的球」，不做「母球是否贴库」这种细致判定：
// 物理侧会把非法球位算出一个怪结果，而那正是玩家自己摆出来的。
//
// 摆完不收回 BallInHand：玩家可以接着挪，直到出杆（见 handleShoot 的闸门）。
func (m *Match) handlePlaceCue(cmd command) error {
	if m.state.Status != model.PoolStatusPlaying {
		return ErrNotPlaying
	}
	if cmd.seat != m.state.Turn {
		return ErrNotYourTurn
	}
	if !m.state.BallInHand {
		return ErrNotBallInHand
	}
	if !insideTable(cmd.x, cmd.y) {
		return ErrBadParams
	}
	// 压住别的球不行。允许的话下一步物理要先花一步把它们挤开，玩家看到的是
	// 「白球跟另一颗球叠在一起」——那画面像渲染坏了，而这其实是个非法球位。
	// 前端拖动时也会把虚影推开（match.vue 的 cueSpot），这里再挡一次是因为
	// 协议是公开的：手搓坐标的人不该能把球摆进另一颗球里。
	if m.state.overlapsBall(cmd.x, cmd.y) {
		return ErrBadParams
	}
	// 开球时只允许摆在开球线之后。球心压线不算越界，与 insideTable 一样
	// 留 1mm 余量：玩家把球摆在恰好贴线的位置，不该因为浮点误差被拒。
	if m.state.PlaceZone == PlaceBehindHead && cmd.y > cueHeadString+0.001 {
		return ErrBadParams
	}

	cue, ok := m.state.ballByID(sim.CueBallID)
	if !ok {
		return ErrBadParams
	}
	// z 必须归零。母球落袋后 z 是袋里的负值，只改 x/y 的话母球会留在台面以下，
	// pooltool 拿到「台面下的球」会抛 argmin of an empty sequence（500），
	// 表现为「落位之后怎么都出不了杆」，而服务端看不出任何问题。
	cue.X, cue.Y, cue.Z = cmd.x, cmd.y, 0
	cue.OnTable = true
	m.state.setBall(cue)
	m.state.CuePlaced = true

	m.broadcastState()
	return nil
}

// handleLeave 主动退出，对手直接获胜。
func (m *Match) handleLeave(cmd command) error {
	if m.state.Status != model.PoolStatusPlaying {
		return ErrNotPlaying
	}
	winner := cmd.seat.Other()
	m.state.Status = model.PoolStatusFinished
	m.state.Winner = &winner
	m.state.WinReason = WinLeave
	m.state.Deadline = time.Time{}
	m.finish(WinLeave)
	m.broadcast(m.wrap(MsgResult, ResultData{Winner: winner, Reason: WinLeave, Series: m.state.Series}))
	return nil
}

// handleFillSeat 把进房的人的昵称与头像填到座位上。
//
// 顺手把这一侧的准备标记清掉：座位可能刚换过人（上一任准备好了又走了），
// 留着那个 true 会让「两个人都准备了」在对局还没开始时就成立。
func (m *Match) handleFillSeat(cmd command) error {
	if !cmd.seat.Valid() {
		return ErrBadParams
	}
	m.state.Players[cmd.seat] = cmd.brief
	m.state.Ready[cmd.seat] = false
	m.broadcastState()
	return nil
}

// handleReady 处理准备。双方齐了就把房间推进成对局。
func (m *Match) handleReady(cmd command) error {
	if m.state.Status != model.PoolStatusWaiting {
		return ErrNotPlaying
	}
	if !cmd.seat.Valid() {
		return ErrBadParams
	}

	m.state.Ready[cmd.seat] = cmd.x > 0
	if !m.state.Ready[Seat0] || !m.state.Ready[Seat1] {
		return nil
	}

	// 双方都准备：开局。读秒从现在开始算，不是从进房开始。
	m.state.Status = model.PoolStatusPlaying
	// 连续约战的大比分在真正开局这一刻读：进房时对手可能还没来，
	// 那时候两个人是谁都不确定，比分无从查起。
	m.state.Series = m.opts.Series.SeatScores(
		m.state.Players[Seat0].UserID,
		m.state.Players[Seat1].UserID,
	)
	// 开球方就是此刻该出杆的一方。记下来是为了结算之后「再约」能轮换开球：
	// Turn 每杆都会变，只有 Breaker 能回答「这一局是谁先开的」。
	m.state.Breaker = m.state.Turn
	// 开球这一杆给开球方手中球，但限定在开球线之后。
	// 摆球表里那颗母球坐标只是一个默认点，玩家可以沿着开球线后重新摆——
	// 真实球桌开球就是这个规矩，钉死一个点会让人觉得「白球不听话」。
	m.state.BallInHand = true
	m.state.PlaceZone = PlaceBehindHead
	m.state.CuePlaced = false
	m.state.Deadline = time.Now().Add(m.opts.TurnTimeout)
	m.broadcastState()
	m.broadcast(m.wrap(MsgTurn, TurnData{Seat: m.state.Turn, DeadlineMS: m.remainingMS()}))
	// 机器人坐开球位时，开局这一刻就该它想杆了。
	// 没有新轨迹要播，只算「想一想」那一段。
	m.armBot(0)
	return nil
}

// handleDisconnect 处理掉线：摘掉连接并通知对手。
//
// 通知对手这一步必须跟着「真的摘掉了」一起做：
// 重连之后旧连接的回调才姗姗来迟时，若照样发一条 offline，
// 双方会看到「对手掉线了」而对手其实正坐在对面——这一条会一直挂着，
// 直到下一次有人进出房间才被覆盖。
func (m *Match) handleDisconnect(cmd command) error {
	// 观战者走了跟对局无关：既不判负，也不用通知玩家。
	// 观战名单里全是连接，靠 cmd.seat 分不出来是哪一位，只能按连接身份摘。
	if cmd.seat == SeatSpectator {
		m.removeSpectator(cmd.conn)
		return nil
	}

	// 只摘掉还登记着的这条：玩家可能已经用新连接重连上来，
	// 旧连接的退出回调晚到一步，不能把新连接一起清掉，也不该说成对手掉线。
	if m.conns[cmd.seat] != cmd.conn {
		return nil
	}
	m.conns[cmd.seat] = nil

	if peer := m.conns[cmd.seat.Other()]; peer != nil {
		peer.Send(m.wrap(MsgPeer, PeerData{Event: PeerOffline, Seat: cmd.seat}))
	}
	return nil
}

// handleTimeout 处理读秒到点：超时一次换人，连续两次判负。
func (m *Match) handleTimeout() {
	if m.state.Status != model.PoolStatusPlaying {
		return
	}
	seat := m.state.Turn
	m.state.Timeouts[seat]++

	if m.state.Timeouts[seat] >= TimeoutLossThreshold {
		winner := seat.Other()
		m.state.Status = model.PoolStatusFinished
		m.state.Winner = &winner
		m.state.WinReason = WinTimeout
		m.state.Deadline = time.Time{}
		m.finish(WinTimeout)
		m.broadcast(m.wrap(MsgResult, ResultData{Winner: winner, Reason: WinTimeout, Series: m.state.Series}))
		return
	}

	// 单次超时只换人并给对手手中球，让局面往前走。
	m.state.Turn = seat.Other()
	m.state.BallInHand = true
	// 超时判罚的自由球没有区域限制：开球线那条规定只对开球那一杆有效。
	m.state.PlaceZone = PlaceAnywhere
	m.state.CuePlaced = false
	m.state.Deadline = time.Now().Add(m.opts.TurnTimeout)

	m.broadcast(m.wrap(MsgFoul, FoulData{
		Code:       "timeout",
		Reason:     "超时未击球",
		BallInHand: true,
		Seat:       seat,
	}))
	m.broadcast(m.wrap(MsgTurn, TurnData{Seat: m.state.Turn, DeadlineMS: m.remainingMS()}))

	// 单次超时把母球交给对手，这个 ball_in_hand 只存在于状态里。
	// 不补快照的话，接手那方的界面看不到「手中球」，直接出杆会被 ErrMustPlaceCue 拒，
	// 而拒绝之后状态没有任何变化，于是他会一直撞在同一句提示上——对局卡死。
	m.broadcastState()
	// 超时换人之后可能轮到机器人：不重新装定时器，它会一直站到下一轮超时。
	// 超时没有新轨迹，只要「想一想」那一段。
	m.armBot(0)
}

// finish 结算落库，并把结果记进日志。
func (m *Match) finish(reason string) {
	m.finishedAtMu.Lock()
	m.finishedAt = time.Now()
	m.finishedAtMu.Unlock()

	m.log.Info("match finished", "reason", reason)

	record := MatchRecord{
		MatchID:   m.state.MatchID,
		WinReason: reason,
		Shots:     m.state.ShotSeq,
		EndedAt:   time.Now(),
	}
	if m.state.Winner != nil {
		record.Winner = *m.state.Winner
		record.WinnerID = m.state.Players[*m.state.Winner].UserID
		record.LoserID = m.state.Players[m.state.Winner.Other()].UserID

		// 连着打的比分：赢的人 +1。同时改状态里的那一份，
		// 这样结算之后前端再拉快照，顶上显示的就是刚打完的新比分。
		winner := *m.state.Winner
		m.state.Series[winner] += 1
		m.opts.Series.AddWin(record.WinnerID, record.LoserID)
	}
	if err := m.opts.Recorder.FinishMatch(context.Background(), record); err != nil {
		m.log.Error("finish match", "error", err)
	}
}

// shutdown 关掉所有连接，由 Close 触发。
func (m *Match) shutdown() {
	// 定时器先停：它的回调会往 actor 的通道里投命令，
	// 而 run() 马上就要返回了，那条命令等不到人处理。
	if m.botTimer != nil {
		m.botTimer.Stop()
		m.botTimer = nil
	}
	for index := range m.conns {
		if m.conns[index] != nil {
			m.conns[index].Close()
			m.conns[index] = nil
		}
	}
	for _, conn := range m.spectators {
		conn.Close()
	}
	m.spectators = nil
	atomic.StoreInt32(&m.spectatorCount, 0)
}

// remainingMS 返回这一杆还剩多少毫秒。
func (m *Match) remainingMS() int {
	if m.state.Deadline.IsZero() {
		return 0
	}
	left := int(time.Until(m.state.Deadline) / time.Millisecond)
	if left < 0 {
		return 0
	}
	return left
}

// wrap 给数据套上下行信封。
func (m *Match) wrap(kind string, data any) Message {
	return Message{Type: kind, TS: time.Now().UnixMilli(), Data: data}
}

// broadcast 发给所有已连接的座位与观战者。
func (m *Match) broadcast(msg Message) {
	for _, conn := range m.conns {
		if conn != nil {
			conn.Send(msg)
		}
	}
	for _, conn := range m.spectators {
		conn.Send(msg)
	}
}

// broadcastExcept 发给除某个座位以外的所有人（观战者全都发）。
//
// 瞄准帧用它：发回给本人只会让本地画面被自己的旧帧顶一下，
// 让杆子抖一下，纯属浪费一条消息。
func (m *Match) broadcastExcept(seat Seat, msg Message) {
	for index, conn := range m.conns {
		if conn == nil || Seat(index) == seat {
			continue
		}
		conn.Send(msg)
	}
	for _, conn := range m.spectators {
		conn.Send(msg)
	}
}

// broadcastState 给每个座位发它自己视角的快照，观战者发中立视角。
//
// 快照里带 seat（看的人坐哪），所以不能算一份发给两个人：
// 那样其中一方会把自己认成对手，界面直接左右翻转。
func (m *Match) broadcastState() {
	for seat := Seat0; seat <= Seat1; seat++ {
		if conn := m.conns[seat]; conn != nil {
			conn.Send(m.wrap(MsgState, m.state.snapshot(seat, time.Now())))
		}
	}
	if len(m.spectators) == 0 {
		return
	}
	// 观战者共用一份中立快照：它们的视角完全一样，不必每人都算一遍。
	spectatorView := m.wrap(MsgState, m.state.snapshot(SeatSpectator, time.Now()))
	for _, conn := range m.spectators {
		conn.Send(spectatorView)
	}
}

// validateParams 校验杆法范围。区间与 pool-sim 的 schema 完全一致，
// 两边都校验是刻意的：这一层挡住的大部分是前端 bug，能省下一次跨语言调用。
func validateParams(params sim.CueParams) error {
	if params.Power < 0 || params.Power > 1 {
		return ErrBadParams
	}
	if params.A < -1 || params.A > 1 || params.B < -1 || params.B > 1 {
		return ErrBadParams
	}
	if params.Theta < -1.5 || params.Theta > 1.5 {
		return ErrBadParams
	}
	return nil
}

// 客户端上行那份整杆结果的量级上限。
//
// 这几条不是防作弊用的（谁来算这件事本来就交给出杆方了），挡的是写错的实现：
// 帧数或事件数量级不对时，广播出去的 shot 帧会把每个观战者的发件箱撑爆，
// 最后表现成所有人一起掉线，而日志里只有一片「send buffer full」。
const (
	// maxShotFrames 按「24 秒 × 120 Hz」再加一倍余量。本地模型的关键帧是
	// 120Hz（为了在 120Hz 屏上播放顺滑，见 web/src/game/predict.ts 的 FRAME_DT），
	// 时长上限 24 秒（阻尼减半后满力单球要滚近 18 秒），正常一杆七八百帧，
	// 四千八对应 24 秒的 2880 帧仍宽出一截。
	maxShotFrames = 4800
	// maxShotEvents 是一杆里最多几条碰撞事件。开球实测一两百条。
	maxShotEvents = 1200
	// maxShotSeconds 是一杆允许的最长时长，比本地模型的 24 秒上限再宽 6 秒：
	// 客户端按自己的时钟累加，与上行里的 duration 会有毫秒级出入，
	// 贴着上限走的话合法长杆会被误拒。
	maxShotSeconds = 30.0
	// shotCoordSlack 是终局坐标允许越过台面内沿的距离。
	// 球落袋时球心就停在袋口圆心上，而袋口圆心本来在台面外十几毫米，
	// 所以不能按「必须落在台面内」判，否则每一杆有球进袋都会被拒。
	shotCoordSlack = 0.3
)

// validateShotPayload 校验客户端带上来的整杆结果。
//
// 只做形状与量级上的校验，不重算物理：这份结果就是这一杆的权威数据，
// 重算一遍等于把「两边各算一次」的老问题又搬回来。
// balls 是这一杆开始时台面上还在的球，终局必须与它一一对应。
func validateShotPayload(shot *sim.Shot, balls []sim.RequestBall) error {
	if len(shot.Frames) == 0 || len(shot.Frames) > maxShotFrames {
		return ErrBadParams
	}
	if len(shot.Events) > maxShotEvents {
		return ErrBadParams
	}
	if shot.Duration < 0 || shot.Duration > maxShotSeconds {
		return ErrBadParams
	}
	// 终局必须一颗不多、一颗不少：少了某颗球，那颗球在服务端就停在原地，
	// 表现为「我这边球被撞开了，对手那边还在原位」；多了则是凭空摆上桌。
	if len(shot.Final) != len(balls) {
		return ErrBadParams
	}
	expected := make(map[string]bool, len(balls))
	for _, ball := range balls {
		expected[ball.ID] = true
	}
	seen := make(map[string]bool, len(shot.Final))
	for _, ball := range shot.Final {
		if !expected[ball.ID] || seen[ball.ID] {
			return ErrBadParams
		}
		seen[ball.ID] = true
		if !validShotCoord(ball.X, ball.Y) {
			return ErrBadParams
		}
	}
	return nil
}

// validShotCoord 判断一个终局坐标能不能收。
//
// 上界放到台面外 0.3 米：进袋的球停在袋口圆心上、已经被判出界的球也不会太远。
// 这一层挡的是 NaN、Inf，以及「把毫米当米」这类数量级错位。
func validShotCoord(x, y float64) bool {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return false
	}
	return x >= -shotCoordSlack && x <= tableWidth+shotCoordSlack &&
		y >= -shotCoordSlack && y <= tableLength+shotCoordSlack
}

// insideTable 判断一个坐标是不是落在台面内。
//
// 台面内沿是 0.9906 × 1.9812，但球心最多只能贴到离库边一个球半径处。
// 半径与前端 table.ts 的 BALL_RADIUS 是同一个数（标准球的 1.2 倍），
// 判定留 1mm 余量，避免玩家把球摆在恰好擦库的位置被拒。
func insideTable(x, y float64) bool {
	const margin = 0.001
	return x >= ballRadius-margin && x <= tableWidth-ballRadius+margin &&
		y >= ballRadius-margin && y <= tableLength-ballRadius+margin
}

// 台面内沿尺寸，与前端 table.ts 的 TABLE_WIDTH / TABLE_LENGTH 是同一组数。
const (
	tableWidth  = 0.9906
	tableLength = 1.9812
	// cueHeadString 是开球线离开球端库边的距离：台长的四分之一。
	// 与前端 table.ts 的 CUE_HEAD_STRING 是同一个值，改一处必须改另一处。
	cueHeadString = tableLength / 4
	// ballRadius 与前端 table.ts 的 BALL_RADIUS 是同一颗球（比标准球大两成）。
	// 它同时是「母球能不能摆在这」与「球是否出界」的判定半径。
	ballRadius = 0.03429
	// ballRadiusHair 是判重叠时让出来的发丝级余量：球心距正好 2R 是合法的
	// 相切摆位，不该让浮点误差把「贴着摆」判成压球。
	ballRadiusHair = 0.001
)

// overlapsBall 判断母球落在 (x, y) 会不会压住台面上别的球。
//
// 母球自己不算：它马上就要被挪到这个点上。
func (s *MatchState) overlapsBall(x, y float64) bool {
	limit := ballRadius*2 - ballRadiusHair
	for _, ball := range s.Balls {
		if !ball.OnTable || ball.ID == sim.CueBallID {
			continue
		}
		dx := ball.X - x
		dy := ball.Y - y
		if dx*dx+dy*dy < limit*limit {
			return true
		}
	}
	return false
}
