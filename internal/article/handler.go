package article

import (
	"forum-backend/internal/httputil"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/response"
)

// ArticleHandler 文章接口，只依赖同包的 service，不认识 GORM。
type ArticleHandler struct {
	svc *ArticleService
}

func NewArticleHandler(svc *ArticleService) *ArticleHandler {
	return &ArticleHandler{svc: svc}
}

// List godoc
// @Summary      文章分页列表
// @Tags         articles
// @Produce      json
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=articleListResp}
// @Failure      400  {string}  string    "分页参数越界"
// @Router       /articles [get]
// List 处理 GET /api/articles。
func (h *ArticleHandler) List(c *gin.Context) {
	page, pageSize, err := httputil.ParsePage(c)
	if err != nil {
		httputil.MapServiceError(c, "list articles", err) //统一错误映射
		return
	}

	articles, total, err := h.svc.List(c.Request.Context(), page, pageSize)
	if err != nil {
		httputil.MapServiceError(c, "list articles", err)
		return
	}

	list := make([]articleResp, 0, len(articles)) //从空开始 申请了len(articles)的容量，避免多次扩容
	for _, a := range articles {
		list = append(list, toArticleResp(a))
	}

	resp := articleListResp{
		List:     list,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	response.OK(c, resp)
}

// Detail godoc
// @Summary      文章详情
// @Tags         articles
// @Produce      json
// @Param        id   path  int  true  "文章 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=articleResp}
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      404  {string}  string    "文章不存在"
// @Router       /articles/{id} [get]
// Detail 处理 GET /api/articles/:id。
func (h *ArticleHandler) Detail(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64) //从 URL 路径中提取名为 "id" 的参数（字符串），
	//并将其安全地转换成无符号 64 位整数（uint64），用于后续的业务逻辑
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}
	a, err := h.svc.Detail(c.Request.Context(), uint(id))
	if err != nil {
		httputil.MapServiceError(c, "article detail", err)
		return
	}
	response.OK(c,toArticleDetailResp(a))
}

// Create godoc
// @Summary      发布文章
// @Tags         articles
// @Accept       json
// @Produce      json
// @Param        body body createArticleReq true "标题与正文"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=articleResp}
// @Failure      400  {string}  string    "请求体格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /articles [post]
// Create 处理 POST /api/articles。
func (h *ArticleHandler) Create(c *gin.Context) {
	// 从 Gin context 取身份。GetUint 在类型不对或没值时返回 0，
	// httputil.CurrentUserID 把 0 判成失败，避免把 0 号用户写进数据库。
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 只往两个字段的结构体里绑。请求体里额外的 user_id 之类字段
	// 会被 encoding/json 忽略，不会进入后面的 service 调用。
	var req createArticleReq
	if err := c.ShouldBindJSON(&req); err != nil { //从 HTTP 请求中，把客户端发来的 JSON 数据，提取出来并塞进 req 这个结构体里
		// JSON 语法错、字段类型错、body 超过 limitBody 的 1 MiB，都落在这里。
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}

	// userID 是第三个参数，来自 token；title 与 content 才是客户端给的。
	a, err := h.svc.Create(c.Request.Context(), userID, req.Title, req.Content)
	if err != nil {
		httputil.MapServiceError(c, "create article", err)
		return
	}

	// 只把 DTO 交给响应包，数据库模型不会直接出现在响应里。
	response.OK(c, toArticleResp(a))
}

// Delete 处理 DELETE /api/articles/:id。
func (h *ArticleHandler) Delete(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 路径参数先转成数字。非法 id 在 handler 边界挡掉，
	// 不把字符串拼进 SQL 条件。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	// 所有权判断在 service 的 WHERE 里完成，handler 不查库。
	if err := h.svc.Delete(c.Request.Context(), uint(id), userID); err != nil {
		httputil.MapServiceError(c, "delete article", err)
		return
	}

	// 当前 response 包只有 OK(200)，所以删除成功返回 200 且 data 为 null。
	// 00 章合同里的 204 属于目标状态，要等响应包加 NoContent 之后一起改。
	response.OK(c, nil)
}
