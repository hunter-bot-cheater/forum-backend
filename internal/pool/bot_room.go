package pool

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"forum-backend/internal/httputil"
	"forum-backend/internal/model"
	"forum-backend/pkg/response"
)

// StartBotMatch 处理 POST /api/pool/rooms/bot：开一局人机对战。
//
// 为什么不是一个「给房间加机器人」的接口：人机局没有准备这一步，
// 也不需要房号给别人。它要的就是「点一下，立刻能打」——
// 走房间那套（建房 → 等准备 → 开局）等于把三次点击塞给只想练两杆的人。
//
// 玩家恒坐座位 0 并且先开球；机器人坐座位 1，由 actor 在轮到它时自己出杆。
func (h *Handler) StartBotMatch(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 查「我有没有在打」与建新房之间必须串行：连着点两下会开出两局，
	// 而库里同时存在两条 playing 的行，「我的房间」就会在两行之间跳。
	unlock := h.lockRoom(userID)
	defer unlock()

	// 已经在打就原样还回去，不再开第二局。
	// 这里读的是内存里的 actor：库里有行、内存里没有的那一种（进程重启过）
	// 球位已经找不回来，往下走会新开一局，比把人送回一局打不动的残局强。
	if row, err := h.store.CurrentMatchFor(ctx, userID); err == nil {
		if row.Status == model.PoolStatusPlaying {
			if match, ok := h.manager.Get(row.ID); ok {
				h.writeRoom(c, match, seatOfRow(row, userID))
				return
			}
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		httputil.LogRequestError(c, "load current match", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}

	// 自己那间等着开打的房、以及在别人房里占着的座位都收掉：
	// 不收的话「我的房间」会在两行之间来回跳，刚报给朋友的房号也会突然作废。
	h.retireOwnRooms(ctx, userID, 0)
	h.vacateGuestSeats(ctx, userID, 0)

	briefs, err := h.users.Briefs(ctx, []uint{userID})
	if err != nil {
		httputil.LogRequestError(c, "load bot player", err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用")
		return
	}
	player := briefOf(briefs, userID)

	// 房号撞唯一索引就换一个重试，与 openRoom 同一套：查与插之间有窗口，
	// 唯一索引才是可靠判据。
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		code, err := NewRoomCode()
		if err != nil {
			lastErr = err
			continue
		}
		created, err := h.store.CreateBotMatch(ctx, userID, code)
		if err != nil {
			lastErr = err
			continue
		}
		match := h.manager.Create(newBotState(created.ID, created.RoomCode, player))
		h.writeRoom(c, match, Seat0)
		return
	}

	httputil.LogRequestError(c, "create bot match", lastErr)
	response.Error(c, http.StatusInternalServerError, "开局失败，请重试")
}
