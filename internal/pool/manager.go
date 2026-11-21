package pool

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Manager 是进程内所有进行中对局的登记表。
//
// 一局一个 actor 协程，Manager 只负责「按 id 找到它」和「没用的时候收掉」，
// 不持有任何对局状态的锁——状态锁在 actor 里，Manager 锁只保护这张 map。
type Manager struct {
	mu      sync.RWMutex
	matches map[uint]*Match
	opts    Options
}

// NewManager 由装配层创建，opts 会被复制给每一局。
// 没给仿真客户端就直接报错：让它跑到第一杆才 nil panic，排查成本高得多。
func NewManager(opts Options) (*Manager, error) {
	if opts.Sim == nil {
		return nil, errors.New("pool: simulator is required")
	}
	if opts.Recorder == nil {
		opts.Recorder = NopRecorder{}
	}
	if opts.TurnTimeout <= 0 {
		opts.TurnTimeout = DefaultTurnTimeout
	}
	return &Manager{
		matches: make(map[uint]*Match),
		opts:    opts,
	}, nil
}

// Create 用给定状态开一局并启动 actor。同一个 match id 重复创建会顶掉旧的。
func (m *Manager) Create(state MatchState) *Match {
	match := NewMatch(state, m.opts)

	m.mu.Lock()
	old, exists := m.matches[state.MatchID]
	m.matches[state.MatchID] = match
	m.mu.Unlock()

	// 旧的先摘掉再关：关的过程会让 actor 断连，拿着 Manager 锁做这件事没好处。
	if exists {
		old.Close()
	}
	return match
}

// Get 按 id 取一局。第二返回值是 false 表示这局不在内存里（没开过，或已被收掉）。
func (m *Manager) Get(matchID uint) (*Match, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	match, ok := m.matches[matchID]
	return match, ok
}

// Remove 把一局摘掉并停掉它的 actor。
func (m *Manager) Remove(matchID uint) {
	m.mu.Lock()
	match, ok := m.matches[matchID]
	delete(m.matches, matchID)
	m.mu.Unlock()

	if ok {
		match.Close()
	}
}

// Count 返回当前内存里的对局数，指标面板与压测用。
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.matches)
}

// List 返回当前内存里的全部对局，供观战列表枚举。
//
// 返回 *Match 而不是快照：调用方要自己决定用哪个视角取状态。
// 拿到的指针只用来发命令（Snapshot / SpectatorCount），不直接读字段，
// 状态仍然只在 actor 协程里。
func (m *Manager) List() []*Match {
	m.mu.RLock()
	defer m.mu.RUnlock()

	matches := make([]*Match, 0, len(m.matches))
	for _, match := range m.matches {
		matches = append(matches, match)
	}
	return matches
}

// CloseAll 停掉全部对局，供进程退出时调用。
func (m *Manager) CloseAll() {
	m.mu.Lock()
	matches := make([]*Match, 0, len(m.matches))
	for _, match := range m.matches {
		matches = append(matches, match)
	}
	m.matches = make(map[uint]*Match)
	m.mu.Unlock()

	for _, match := range matches {
		match.Close()
	}
}

// Janitor 定期收掉已经结束且没人连着的对局。
//
// 不收拾的后果是内存只增不减：一晚上几百局全部留在 map 里，每局还挂着十几颗球的位置。
// 退出靠 ctx：进程关停时 main 取消 context，这个 goroutine 随之结束。
func (m *Manager) Janitor(ctx context.Context, interval, keep time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweep(keep)
		}
	}
}

// sweep 收掉「已结束且超过 keep」的对局。
func (m *Manager) sweep(keep time.Duration) {
	now := time.Now()

	m.mu.RLock()
	candidates := make([]uint, 0)
	for id, match := range m.matches {
		// 这里读的是 actor 内部状态，理论上要经命令通道。
		// 收垃圾这个场景允许读到略旧的快照：最坏是多活一轮，不会收错。
		if match.idleFor(now) > keep {
			candidates = append(candidates, id)
		}
	}
	m.mu.RUnlock()

	for _, id := range candidates {
		slog.Debug("reap idle pool match", "match_id", id)
		m.Remove(id)
	}
}

// idleFor 返回这局已经结束多久（还在打则返回 0）。
//
// 用一个原子读的时间戳而不是每次问 actor：清扫是后台动作，
// 为了它给每局加一次命令往返不值得。
func (m *Match) idleFor(now time.Time) time.Duration {
	m.finishedAtMu.Lock()
	finishedAt := m.finishedAt
	m.finishedAtMu.Unlock()

	if finishedAt.IsZero() {
		return 0
	}
	return now.Sub(finishedAt)
}
