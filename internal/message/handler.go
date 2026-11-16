// Package message 的 HTTP 入口：解析参数、调 service、把结果或错误翻译成响应。
package message

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/httputil"
	"forum-backend/pkg/response"
)

// Handler 只做三件事：取身份、解参数、调 service。
// 业务规则（字数、自己发自己、会话创建）全在 Service 里，这里不重复判断。
type Handler struct {
	svc *Service
}

// NewHandler 由 router 在装配期调用一次。
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Send godoc
// @Summary      给某人发私信
// @Description  会话不存在时自动创建。收件人来自路径，发件人来自 token。
// @Description  可选 client_msg_id 作幂等键：同一个 key 重复提交只落一条，返回第一次那条。
// @Tags         messages
// @Accept       json
// @Produce      json
// @Param        user_id  path  int              true  "收件人 ID"
// @Param        body     body  message.sendReq  true  "消息正文 1~1000 字符；client_msg_id 可选"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=message.messageResp}
// @Failure      400  {string}  string  "正文为空或超长"
// @Failure      401  {string}  string  "未登录"
// @Failure      404  {string}  string  "收件人不存在"
// @Security     Bearer
// @Router       /conversations/with/{user_id}/messages [post]
// Send 处理 POST /api/conversations/with/:user_id/messages。
func (h *Handler) Send(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	peerID, ok := pathUserID(c)
	if !ok {
		return
	}

	// 绑定失败通常是 content 传了非字符串。具体错误只进服务端日志，
	// 对调用方回固定文案，避免把 JSON 解析细节泄露出去。
	var req sendReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeBadRequest, "请求体格式不正确")
		return
	}

	msg, err := h.svc.Send(c.Request.Context(), userID, peerID, req.Content, req.ClientMsgID)
	if err != nil {
		httputil.MapServiceError(c, "send message", err)
		return
	}
	response.OK(c, toMessageResp(MessageItem{Message: *msg, Mine: true}))
}

// Conversations godoc
// @Summary      我的会话列表
// @Description  按最后消息时间倒序，带对方资料、最后一条消息与未读数。
// @Tags         messages
// @Produce      json
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最多 50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=message.conversationListResp}
// @Failure      400  {string}  string  "分页参数不合法"
// @Failure      401  {string}  string  "未登录"
// @Security     Bearer
// @Router       /conversations [get]
// Conversations 处理 GET /api/conversations。
func (h *Handler) Conversations(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	page, pageSize, err := httputil.ParsePage(c)
	if err != nil {
		httputil.MapServiceError(c, "conversation list", err)
		return
	}

	items, total, err := h.svc.Conversations(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		httputil.MapServiceError(c, "conversation list", err)
		return
	}
	response.OK(c, toConversationListResp(items, total, page, pageSize, userID))
}

// Messages godoc
// @Summary      与某人的消息列表
// @Description  游标分页：不传 before_id 取最新一页，往上翻时把上一页的 next_before_id 作为 before_id 传回来；同页内按时间正序。
// @Tags         messages
// @Produce      json
// @Param        user_id    path   int  true   "对方的用户 ID"
// @Param        before_id  query  int  false  "游标：只取 id 小于它的消息，缺省 0 表示最新一页"
// @Param        limit      query  int  false  "每页条数，默认 20，最多 50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=message.messageListResp}
// @Failure      400  {string}  string  "分页参数不合法"
// @Failure      401  {string}  string  "未登录"
// @Failure      404  {string}  string  "用户不存在"
// @Security     Bearer
// @Router       /conversations/with/{user_id}/messages [get]
// Messages 处理 GET /api/conversations/with/:user_id/messages。
func (h *Handler) Messages(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	peerID, ok := pathUserID(c)
	if !ok {
		return
	}

	beforeID, limit, err := httputil.ParseCursor(c)
	if err != nil {
		httputil.MapServiceError(c, "message list", err)
		return
	}

	pageData, err := h.svc.Messages(c.Request.Context(), userID, peerID, MessageQuery{
		BeforeID: beforeID,
		Limit:    limit,
	})
	if err != nil {
		httputil.MapServiceError(c, "message list", err)
		return
	}
	response.OK(c, toMessageListResp(pageData))
}

// MarkRead godoc
// @Summary      把与某人的会话标记为已读
// @Description  幂等，已读状态下调用返回 cleared=0，不报错。
// @Tags         messages
// @Produce      json
// @Param        user_id  path  int  true  "对方的用户 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=message.readResp}
// @Failure      401  {string}  string  "未登录"
// @Security     Bearer
// @Router       /conversations/with/{user_id}/read [put]
// MarkRead 处理 PUT /api/conversations/with/:user_id/read。
func (h *Handler) MarkRead(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	peerID, ok := pathUserID(c)
	if !ok {
		return
	}

	cleared, err := h.svc.MarkRead(c.Request.Context(), userID, peerID)
	if err != nil {
		httputil.MapServiceError(c, "mark conversation read", err)
		return
	}
	response.OK(c, readResp{Cleared: cleared})
}

// Unread godoc
// @Summary      我的未读私信总数
// @Description  供顶部角标使用，不区分会话。
// @Tags         messages
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=message.unreadResp}
// @Failure      401  {string}  string  "未登录"
// @Security     Bearer
// @Router       /conversations/unread [get]
// Unread 处理 GET /api/conversations/unread。
func (h *Handler) Unread(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	total, err := h.svc.UnreadTotal(c.Request.Context(), userID)
	if err != nil {
		httputil.MapServiceError(c, "unread total", err)
		return
	}
	response.OK(c, unreadResp{Unread: total})
}

// pathUserID 解析路径里的 :user_id。
//
// 解析失败时已经写好 400 响应，调用方直接 return 即可，
// 第二个返回值为 false 就代表「响应已发出，不要再继续」。
func pathUserID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("user_id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, "user_id 格式不正确")
		return 0, false
	}
	return uint(id), true
}
