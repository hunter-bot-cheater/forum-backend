package pool

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
	"sync"
	"time"
)

// 邀请的状态。一条邀请只有四种归宿，转移是单向的。
const (
	InvitePending  = "pending"
	InviteAccepted = "accepted"
	InviteDeclined = "declined"
	InviteExpired  = "expired"
)

// 邀请的有效期。过了这个点，点开邀请只会看到「邀请已失效」。
//
// 不做得更长是因为对局本身要两边同时在线：一条挂了半小时的邀请，
// 对方点接受时邀请人早就干别的去了，进房也只能干等。
const InviteTTL = 60 * time.Second

// 一个人同一时刻只能挂一条待接受的邀请，否则可以刷列表骚扰。
var ErrInviteBusy = errors.New("pool: you already have a pending invite")

// ErrInviteMutual 表示两个人几乎同时互相邀请。
//
// 这种情形必须拦下来：两边各建了一间房，谁先接受就把另一间作废，
// 后点的人必然进不去，两个页面还会各显示一间自己的房。
var ErrInviteMutual = errors.New("pool: the other side already invited you")

// ErrInviteGone 表示邀请不存在、不是发给你的、或者已经用过 / 过期。
var ErrInviteGone = errors.New("pool: invite is gone")

// Invite 是一条邀请。它只活在内存里：邀请的寿命只有一分钟，
// 落库反而要额外做清理，而通知中心要展示的那条另走 notification 表。
type Invite struct {
	ID        string
	FromID    uint
	ToID      uint
	MatchID   uint
	RoomCode  string
	Status    string
	CreatedAt time.Time
}

// Invites 是邀请登记表，进程内唯一。
type Invites struct {
	mu      sync.Mutex
	byID    map[string]*Invite
	ttl     time.Duration
	nowFunc func() time.Time
}

// NewInvites 由装配层调用。
func NewInvites() *Invites {
	return &Invites{
		byID:    make(map[string]*Invite),
		ttl:     InviteTTL,
		nowFunc: time.Now,
	}
}

// Create 登记一条邀请。同一个发起人已有待接受邀请时返回 ErrInviteBusy。
func (r *Invites) Create(fromID, toID, matchID uint, roomCode string) (*Invite, error) {
	id, err := randomID()
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, invite := range r.byID {
		if invite.Status != InvitePending || r.expired(invite) {
			continue
		}
		if invite.FromID == fromID {
			return nil, ErrInviteBusy
		}
		// 对方已经先邀请我了：这时候再发一条，等于两边各开一间房，
		// 谁先接受谁就把另一间作废。直接让他去接受对方那条。
		if invite.FromID == toID && invite.ToID == fromID {
			return nil, ErrInviteMutual
		}
	}

	invite := &Invite{
		ID:        id,
		FromID:    fromID,
		ToID:      toID,
		MatchID:   matchID,
		RoomCode:  roomCode,
		Status:    InvitePending,
		CreatedAt: r.nowFunc(),
	}
	r.byID[id] = invite
	return invite, nil
}

// Get 按 id 取邀请。已过期的会顺手标成 expired 再返回，调用方看到状态就知道该说什么。
func (r *Invites) Get(id string) (*Invite, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	invite, ok := r.byID[id]
	if !ok {
		return nil, false
	}
	if invite.Status == InvitePending && r.expired(invite) {
		invite.Status = InviteExpired
	}
	return invite, true
}

// PendingFor 返回发给某个人的、还没过期的邀请，最近的在前面。
func (r *Invites) PendingFor(userID uint) []*Invite {
	r.mu.Lock()
	defer r.mu.Unlock()

	pending := make([]*Invite, 0, 1)
	for _, invite := range r.byID {
		if invite.ToID != userID || invite.Status != InvitePending {
			continue
		}
		if r.expired(invite) {
			invite.Status = InviteExpired
			continue
		}
		pending = append(pending, invite)
	}

	// 一个人同时只会有一条（Create 时挡掉了），这里仍按时间排一次，
	// 是因为将来可能放开「同时收多条」，排序逻辑留在原地不用改。
	if len(pending) > 1 {
		sortInvitesNewestFirst(pending)
	}
	return pending
}

// SetStatus 改邀请状态。只有 pending 能改，避免重复接受把已用过的邀请再翻出来。
func (r *Invites) SetStatus(id, status string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	invite, ok := r.byID[id]
	if !ok || invite.Status != InvitePending {
		return false
	}
	invite.Status = status
	return true
}

// CancelOutgoing 把某个人发出去、还没被接受的邀请全部标成已失效，返回条数。
//
// 用在「他改去接受别人的邀请」时：他原来那间房会被作废，
// 挂着的邀请再被点开也进不去了。不标掉的话对方页面上还留着一张
// 看起来能点的卡片，点下去只会收到「房间已被占用」。
func (r *Invites) CancelOutgoing(fromID uint) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	canceled := 0
	for _, invite := range r.byID {
		if invite.FromID == fromID && invite.Status == InvitePending {
			invite.Status = InviteExpired
			canceled++
		}
	}
	return canceled
}

// Sweep 清掉已经过期的邀请，返回清掉的条数。
//
// 不清理的话这张 map 会一直涨：每条邀请都要活满一分钟才失效，
// 而活跃用户每分钟都可能发一条。
func (r *Invites) Sweep() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	removed := 0
	for id, invite := range r.byID {
		// 留一点余量，让刚好在边界上的邀请还能被看到「已失效」而不是直接消失。
		if r.nowFunc().Sub(invite.CreatedAt) > 2*r.ttl {
			delete(r.byID, id)
			removed++
		}
	}
	return removed
}

// expired 判断一条邀请是否过期。调用方必须已经持锁。
func (r *Invites) expired(invite *Invite) bool {
	return r.nowFunc().Sub(invite.CreatedAt) > r.ttl
}

// sortInvitesNewestFirst 按创建时间倒序排。
func sortInvitesNewestFirst(invites []*Invite) {
	for i := 1; i < len(invites); i++ {
		for j := i; j > 0 && invites[j].CreatedAt.After(invites[j-1].CreatedAt); j-- {
			invites[j], invites[j-1] = invites[j-1], invites[j]
		}
	}
}

// randomID 生成邀请 id。用 crypto/rand 而不是自增：
// 邀请 id 会出现在 URL 里，可枚举的 id 等于让别人替你接受邀请。
func randomID() (string, error) {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// NewRoomCode 生成 6 位数字房号。
//
// 6 位是因为要能口头报出来、也能手输；纯数字省掉「0 和 O」这类歧义字符。
// 撞号由 pool_match 的唯一索引兜住，调用方换一个重试。
func NewRoomCode() (string, error) {
	limit := big.NewInt(1000000)
	number, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", err
	}
	// 前导 0 也要保留：房号是 6 位字符串，不是数字。
	code := number.String()
	for len(code) < 6 {
		code = "0" + code
	}
	return code, nil
}
