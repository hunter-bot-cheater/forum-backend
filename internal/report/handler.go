package report

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/httputil"
	"forum-backend/pkg/response"
)

// Handler 是举报模块的 HTTP 入口。
// 每个方法都走同一个套路：取身份/解析参数 → 调 service → 成功交 DTO、失败交 MapServiceError。
type Handler struct {
	svc *Service
}

// NewHandler 注入举报 service，由 router 在装配期调用。
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Submit godoc
// @Summary      举报文章或评论
// @Description  同一用户对同一条内容只保留一条待处理举报；不能举报自己的内容
// @Tags         reports
// @Accept       json
// @Produce      json
// @Param        body  body      submitReq  true  "目标类型与 id、举报分类、可选补充说明"
// @Success      200   {object}  httputil.SwaggerEnvelope{data=submitResp}
// @Failure      400   {string}  string  "类型/分类不合法或补充说明过长"
// @Failure      404   {string}  string  "被举报的内容不存在或已删除"
// @Failure      409   {string}  string  "已经举报过且仍在等待处理"
// @Router       /reports [post]
// Submit 处理 POST /api/reports。
func (h *Handler) Submit(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	var req submitReq
	if err := c.ShouldBindJSON(&req); err != nil {
		// 缺必填字段、类型不对、或者 body 超过 limitBody 的 1 MiB 都落在这里。
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}

	// 两个结构体字段完全一致，直接转换；字段名改了一边而忘了另一边时，
	// 这里会编译不过，比手写一遍赋值更不容易漏。
	rep, err := h.svc.Submit(c.Request.Context(), userID, SubmitInput(req))
	if err != nil {
		httputil.MapServiceError(c, "submit report", err)
		return
	}
	response.OK(c, submitResp{ID: rep.ID, Status: rep.Status})
}

// List godoc
// @Summary      管理端举报列表
// @Description  默认只返回待处理；status 可传 pending / dismissed / removed / banned
// @Tags         admin
// @Produce      json
// @Param        status     query  string  false  "按状态筛选，缺省为 pending"
// @Param        page       query  int     false  "页码，默认 1"
// @Param        page_size  query  int     false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=reportListResp}
// @Failure      400  {string}  string  "status 或分页参数不合法"
// @Failure      403  {string}  string  "不是管理员"
// @Router       /admin/reports [get]
// List 处理 GET /api/admin/reports，只有管理员能调（白名单由 RequireAdmin 拦）。
func (h *Handler) List(c *gin.Context) {
	page, pageSize, err := httputil.ParsePage(c)
	if err != nil {
		httputil.MapServiceError(c, "list reports", err)
		return
	}

	entries, total, err := h.svc.List(c.Request.Context(), c.Query("status"), page, pageSize)
	if err != nil {
		httputil.MapServiceError(c, "list reports", err)
		return
	}

	list := make([]reportItem, 0, len(entries))
	for _, e := range entries {
		list = append(list, toReportItem(e))
	}
	response.OK(c, reportListResp{List: list, Total: total, Page: page, PageSize: pageSize})
}

// Handle godoc
// @Summary      处置一条举报
// @Description  dismiss 驳回、remove 下架内容、ban 下架内容并封禁作者（ban 必须带 ban_days）
// @Tags         admin
// @Accept       json
// @Produce      json
// @Param        id    path  int        true  "举报 id"
// @Param        body  body  handleReq  true  "处置动作与封禁天数"
// @Success      200   {string}  string  "处理完成，data 为 null"
// @Failure      400   {string}  string  "动作未知或 ban_days 越界"
// @Failure      403   {string}  string  "不是管理员"
// @Failure      404   {string}  string  "举报不存在"
// @Failure      409   {string}  string  "这条举报已经被处理过"
// @Router       /admin/reports/{id} [put]
// Handle 处理 PUT /api/admin/reports/:id。
func (h *Handler) Handle(c *gin.Context) {
	adminID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 路径参数是字符串，ParseUint 顺便挡掉负数与溢出的 id：
	// 直接 Atoi 再把负数转 uint 会得到一个巨大的 id，查出来是 404，但错误信息会误导排查。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, "举报 id 格式不正确")
		return
	}

	var req handleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}

	if err := h.svc.Handle(c.Request.Context(), adminID, uint(id), HandleInput(req)); err != nil {
		httputil.MapServiceError(c, "handle report", err)
		return
	}
	response.OK(c, nil)
}
