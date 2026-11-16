package comment

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/httputil"
	"forum-backend/pkg/response"
)

// CommentHandler 评论接口，只依赖 service，不认识 GORM。
// 方法名与 ArticleHandler 对齐：List / Create / Delete。
type CommentHandler struct {
	svc *CommentService
}

// NewCommentHandler 由装配层调用，注入已构造好的 service。
func NewCommentHandler(svc *CommentService) *CommentHandler {
	return &CommentHandler{svc: svc}
}

// List godoc
// @Summary      文章评论列表
// @Tags         comments
// @Produce      json
// @Param        id         path   int  true   "文章 ID"
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=comment.commentListResp}
// @Failure      400  {string}  string    "id 或分页参数不正确"
// @Router       /articles/{id}/comments [get]
// List 处理 GET /api/articles/:id/comments。
// 这条路由里的 :id 是文章 id，不是评论 id。
func (h *CommentHandler) List(c *gin.Context) {
	// 列表是公开接口，不需要取 httputil.CurrentUserID。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}
	page, pageSize, err := httputil.ParsePage(c)
	if err != nil {
		httputil.MapServiceError(c, "list comments", err)
		return
	}
	list, total, err := h.svc.List(c.Request.Context(), uint(id), page, pageSize)
	if err != nil {
		httputil.MapServiceError(c, "list comments", err)
		return
	}

	// toCommentListResp 内部保证 list 与 replies 都是 []，空结果不会输出 null。
	resp := commentListResp{
		List:     toCommentListResp(list),
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	response.OK(c, resp)
}

// Locate godoc
// @Summary      定位评论在文章评论列表中的位置
// @Description  通知点进来时用：返回所属根评论与它的序号，前端据此算出该拉第几页。
// @Tags         comments
// @Produce      json
// @Param        id   path  int  true  "评论 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=comment.commentAnchorResp}
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      404  {string}  string    "评论不存在"
// @Router       /comments/{id}/locate [get]
// Locate 处理 GET /api/comments/:id/locate。
// 路径里的 :id 是评论 id，不是文章 id；结果里带 article_id，前端可以据此校验是否同一篇。
func (h *CommentHandler) Locate(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	// 0 也拦掉：主键从 1 开始，传 0 只会白查一次库。
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}
	anchor, err := h.svc.Locate(c.Request.Context(), uint(id))
	if err != nil {
		httputil.MapServiceError(c, "locate comment", err)
		return
	}
	// Anchor 与对外结构的字段名、类型、顺序完全一致，直接转换。
	// 以后任一边加字段都会在这里编译失败，正好提醒同步，比逐字段抄一遍安全。
	response.OK(c, commentAnchorResp(anchor))
}

// Create godoc
// @Summary      发表评论
// @Tags         comments
// @Accept       json
// @Produce      json
// @Param        id    path  int                        true  "文章 ID"
// @Param        body  body  comment.createCommentReq   true  "评论内容与可选的 parent_id"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=comment.commentResp}
// @Failure      400  {string}  string    "id 或请求体格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Failure      404  {string}  string    "文章或父评论不存在"
// @Security     Bearer
// @Router       /articles/{id}/comments [post]
// Create 处理 POST /api/articles/:id/comments。
// 三个输入的来源要分清楚：文章 id 来自路径，作者 id 来自 JWT，内容与 parent_id 来自请求体。
// parent_id 大于 0 时就是一条回复，服务端会校验父评论属于这篇文章。
func (h *CommentHandler) Create(c *gin.Context) {
	// 路由挂在 JWTAuth 分组下；httputil.CurrentUserID 再兜一层，避免 id 为 0 时写出脏数据。
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 这里的 :id 是文章 id。先转成数字，别把字符串拼进任何查询。
	articleID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	// 只往 content 一个字段的结构体里绑。请求体里伪造的 article_id、user_id
	// 会被 encoding/json 忽略，不会进入后面的 service 调用。
	var req createCommentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}

	m, err := h.svc.Create(c.Request.Context(), uint(articleID), userID, req.ParentID, req.Content)
	if err != nil {
		httputil.MapServiceError(c, "create comment", err)
		return
	}

	// 新建时没有 Preload("User")，m.User 是零值，toCommentResp 会省略 author。
	response.OK(c, toCommentResp(m))
}

// Delete godoc
// @Summary      删除自己的评论
// @Tags         comments
// @Produce      json
// @Param        id   path  int  true  "评论 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=object}
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Failure      403  {string}  string    "只能删除自己的评论"
// @Failure      404  {string}  string    "评论不存在"
// @Security     Bearer
// @Router       /comments/{id} [delete]
// Delete 处理 DELETE /api/comments/:id。
// 这条路由的 :id 是评论 id，与 Create 里的文章 id 不是一回事。
func (h *CommentHandler) Delete(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	// 所有权判断在 service 的 WHERE 里，handler 不查库。
	if err := h.svc.Delete(c.Request.Context(), uint(id), userID); err != nil {
		httputil.MapServiceError(c, "delete comment", err)
		return
	}

	// 当前 response 包只有 OK(200)，删除成功是 200 且 data 为 null。
	// 00 章合同里的 204 要等响应包扩展 NoContent 之后一起改。
	response.OK(c, nil)
}
