package floor

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/httputil"
	"forum-backend/pkg/response"
)

// Handler 抢楼活动的 HTTP 入口，只依赖本模块的 service。
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// parseEventID 从路径里取活动 id。失败时已经写好 400 响应，调用方直接 return。
func parseEventID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return 0, false
	}
	return uint(id), true
}

// Create godoc
// @Summary      创建抢楼活动
// @Tags         floors
// @Accept       json
// @Produce      json
// @Param        body  body  floor.createEventReq  true  "活动配置"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=floor.eventResp}
// @Failure      400  {string}  string  "配置不合法"
// @Failure      401  {string}  string  "未登录"
// @Security     Bearer
// @Router       /floor-events [post]
// Create 处理 POST /api/floor-events。
// 中奖楼层在创建时固定，之后不提供修改接口，理由写在 service.Create 上。
func (h *Handler) Create(c *gin.Context) {
	var req createEventReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "请求体格式不正确，时间请用 RFC3339")
		return
	}
	ev, err := h.svc.Create(c.Request.Context(), req)
	if err != nil {
		httputil.MapServiceError(c, "create floor event", err)
		return
	}
	// 返回与 Detail 完全一致的形状，前端不用为「刚创建」单独写一套解析。
	resp, err := h.svc.Detail(c.Request.Context(), ev.ID)
	if err != nil {
		httputil.MapServiceError(c, "load created floor event", err)
		return
	}
	response.OK(c, resp)
}

// Detail godoc
// @Summary      抢楼活动详情与中奖名单
// @Tags         floors
// @Produce      json
// @Param        id   path  int  true  "活动 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=floor.eventResp}
// @Failure      400  {string}  string  "id 格式不正确"
// @Failure      404  {string}  string  "活动不存在"
// @Router       /floor-events/{id} [get]
// Detail 处理 GET /api/floor-events/:id。
func (h *Handler) Detail(c *gin.Context) {
	eventID, ok := parseEventID(c)
	if !ok {
		return
	}
	resp, err := h.svc.Detail(c.Request.Context(), eventID)
	if err != nil {
		httputil.MapServiceError(c, "get floor event", err)
		return
	}
	response.OK(c, resp)
}

// Grab godoc
// @Summary      抢一次楼
// @Tags         floors
// @Accept       json
// @Produce      json
// @Param        id    path  int             true  "活动 ID"
// @Param        body  body  floor.grabReq  true  "抢楼内容"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=floor.grabResp}
// @Failure      400  {string}  string  "参数不合法"
// @Failure      401  {string}  string  "未登录"
// @Failure      404  {string}  string  "活动不存在"
// @Failure      409  {string}  string  "活动未在进行中，或这一楼已被抢走"
// @Security     Bearer
// @Router       /floor-events/{id}/grab [post]
// Grab 处理 POST /api/floor-events/:id/grab。
// 楼层号一律由服务端 INCR 发，请求体里伪造 floor 字段会被 encoding/json 忽略。
func (h *Handler) Grab(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}
	eventID, ok := parseEventID(c)
	if !ok {
		return
	}
	var req grabReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}
	resp, err := h.svc.Grab(c.Request.Context(), eventID, userID, req.Content)
	if err != nil {
		httputil.MapServiceError(c, "grab floor", err)
		return
	}
	response.OK(c, resp)
}

// Mine godoc
// @Summary      我抢到的中奖楼层
// @Tags         floors
// @Produce      json
// @Param        id   path  int  true  "活动 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=[]floor.winnerResp}
// @Failure      401  {string}  string  "未登录"
// @Security     Bearer
// @Router       /floor-events/{id}/mine [get]
// Mine 处理 GET /api/floor-events/:id/mine。
func (h *Handler) Mine(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}
	eventID, ok := parseEventID(c)
	if !ok {
		return
	}
	list, err := h.svc.Mine(c.Request.Context(), eventID, userID)
	if err != nil {
		httputil.MapServiceError(c, "list my floors", err)
		return
	}
	response.OK(c, list)
}
