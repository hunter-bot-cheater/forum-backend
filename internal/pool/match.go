package pool

import (
	"context"
	"errors"
	"log/slog"
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
}

// 一杆的读秒时长。职业比赛常见 30 秒，这里留 40 秒：
// 手机端拖动瞄准本来就慢，加上开球时服务端要算一秒半。
const DefaultTurnTimeout = 40 * time.Second

type cmdKind int

const (
	cmdJoin cmdKind = iota
	cmdShoot
	cmdPlaceCue
	cmdSnapshot
	cmdReady
	cmdLeave
	cmdDisconnect
	cmdWatch
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
	params sim.CueParams
	seq    int
	x, y   float64
	reply  chan error
	state  chan Snapshot
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
}

// NewMatch 创建并启动一局。state 里的球位、座位与玩家资料由调用方准备好。
func NewMatch(state MatchState, opts Options) *Match {
	if opts.TurnTimeout <= 0 {
		opts.TurnTimeout = DefaultTurnTimeout
	}
	if opts.Recorder == nil {
		opts.Recorder = NopRecorder{}
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
	// 先发布一份初始摘要再起协程：房间阶段的对局也可能被列表读到。
	match.publishLive()

	go match.run()
	return match
}

// publishLive 把当前状态里的几个字段抄进 atomic 指针。只在 actor 里调用。
//
// 每条命令处理完都发布一次，而不是只在换人时发：
// 球的序号、状态（开局 / 结束）都会变，漏发就会让列表显示上一杆的序号。
func (m *Match) publishLive() {
	m.live.Store(&LiveInfo{
		MatchID: m.state.MatchID,
		Status:  m.state.Status,
		Turn:    m.state.Turn,
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

// Shoot 出一杆。会一直阻塞到这一杆算完（开球约 1.5 秒）。
func (m *Match) Shoot(seat Seat, params sim.CueParams, seq int) error {
	return m.call(command{kind: cmdShoot, seat: seat, params: params, seq: seq})
}

// PlaceCue 摆放手中球。
func (m *Match) PlaceCue(seat Seat, x, y float64) error {
	return m.call(command{kind: cmdPlaceCue, seat: seat, x: x, y: y})
}

// Leave 主动退出，判负。
func (m *Match) Leave(seat Seat) error {
	return m.call(command{kind: cmdLeave, seat: seat})
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
	case cmdSnapshot:
		cmd.state <- m.state.snapshot(cmd.seat, time.Now())
	case cmdLeave:
		cmd.reply <- m.handleLeave(cmd)
	case cmdReady:
		cmd.reply <- m.handleReady(cmd)
	case cmdDisconnect:
		cmd.reply <- m.handleDisconnect(cmd)
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
	if m.state.BallInHand {
		// 手里还握着球就出杆，说明客户端没等落位结果，直接拒。
		return ErrMustPlaceCue
	}

	shot, err := m.opts.Sim.Shoot(context.Background(), sim.Request{
		Balls:    m.state.simBalls(),
		Cue:      cmd.params,
		SampleHz: 30,
	})
	if err != nil {
		// 这里只记日志并返回错误：错误帧由 WS 层按 error 码统一回一条。
		// 两处都发的话，玩家会看到两条一模一样的「球桌暂时算不动」。
		m.log.Error("simulate shot", "error", err)
		return ErrSimFailed
	}

	verdict := JudgeShot(m.state.ruleState(), shot)
	m.applyShot(shot, verdict)

	// 广播结果：轨迹给所有人，犯规与结算各自带一条，前端按顺序播。
	shotData := ShotData{
		Seq:      m.state.ShotSeq,
		Seat:     cmd.seat,
		Params:   cmd.params,
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
			Seat:       cmd.seat,
		}))
	}

	if verdict.Winner != nil {
		m.broadcast(m.wrap(MsgResult, ResultData{Winner: *verdict.Winner, Reason: verdict.WinReason}))
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
		ShooterID: m.state.Players[cmd.seat].UserID,
		Seat:      cmd.seat,
		Params:    cmd.params,
		Events:    shot.Events,
		Frames:    shot.Frames,
		Duration:  shot.Duration,
		SimMS:     shot.SimMS,
	}); err != nil {
		m.log.Error("save shot", "seq", m.state.ShotSeq, "error", err)
	}
	return nil
}

// applyShot 把一杆的物理结果与规则结论落到状态上。
func (m *Match) applyShot(shot *sim.Shot, verdict Verdict) {
	// 击球方在本杆结束前还是 state.Turn，换人在最后一步做。
	shooter := m.state.Turn

	// 终局球位：pool-sim 会把送进去的每颗球都报回来，落袋的带 on_table=false。
	for _, ball := range shot.Final {
		m.state.setBall(ball)
	}

	// 开球把 8 号打进要重摆，位置用开局时的坐标。
	if verdict.ReSpotEight {
		for _, item := range StandardRack {
			if item.ID == sim.EightBallID {
				m.state.setBall(sim.ResultBall{ID: item.ID, X: item.X, Y: item.Y, OnTable: true})
			}
		}
	}

	// 定组：击球方得到本杆第一颗落袋球所属的组，对手自动拿另一组。
	if verdict.AssignedGroup != GroupNone {
		m.state.Groups[shooter] = verdict.AssignedGroup
		m.state.Groups[shooter.Other()] = verdict.AssignedGroup.Other()
	}

	m.state.BreakDone = true
	m.state.ShotSeq++
	m.state.BallInHand = verdict.BallInHand
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
	m.state.BallInHand = false

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
	m.broadcast(m.wrap(MsgResult, ResultData{Winner: winner, Reason: WinLeave}))
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
	m.state.Deadline = time.Now().Add(m.opts.TurnTimeout)
	m.broadcastState()
	m.broadcast(m.wrap(MsgTurn, TurnData{Seat: m.state.Turn, DeadlineMS: m.remainingMS()}))
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
		m.broadcast(m.wrap(MsgResult, ResultData{Winner: winner, Reason: WinTimeout}))
		return
	}

	// 单次超时只换人并给对手手中球，让局面往前走。
	m.state.Turn = seat.Other()
	m.state.BallInHand = true
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
	}
	if err := m.opts.Recorder.FinishMatch(context.Background(), record); err != nil {
		m.log.Error("finish match", "error", err)
	}
}

// shutdown 关掉所有连接，由 Close 触发。
func (m *Match) shutdown() {
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

// insideTable 判断一个坐标是不是落在台面内。
//
// 台面内沿是 0.9906 × 1.9812，但母球半径 28.575mm，球心最多只能贴到离库边一个半径处。
// 判定留 1mm 余量，避免玩家把球摆在恰好擦库的位置被拒。
func insideTable(x, y float64) bool {
	const radius = 0.028575
	const margin = 0.001
	return x >= radius-margin && x <= tableWidth-radius+margin &&
		y >= radius-margin && y <= tableLength-radius+margin
}

// 台面内沿尺寸，与前端 table.ts 的 TABLE_WIDTH / TABLE_LENGTH 是同一组数。
const (
	tableWidth  = 0.9906
	tableLength = 1.9812
)
