package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/service"
	"forum-backend/pkg/response"
)

// CommentHandler 评论接口，只依赖 service，不认识 GORM。
// 方法名与 ArticleHandler 对齐：List / Create / Delete。
type CommentHandler struct {
	svc *service.CommentService
}

func NewCommentHandler(svc *service.CommentService) *CommentHandler {
	return &CommentHandler{svc: svc}
}

// List godoc
// @Summary      文章评论列表
// @Tags         comments
// @Produce      json
// @Param        id         path   int  true   "文章 ID"
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.commentListResp}
// @Failure      400  {string}  string    "id 或分页参数不正确"
// @Router       /articles/{id}/comments [get]
// List 处理 GET /api/articles/:id/comments。
// 这条路由里的 :id 是文章 id，不是评论 id。
func (h *CommentHandler) List(c *gin.Context) {
	// 列表是公开接口，不需要取 currentUserID。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}
	page, pageSize, err := parsePage(c)
	if err != nil {
		mapServiceError(c, "list comments", err)
		return
	}
	list, total, err := h.svc.List(c.Request.Context(), uint(id), page, pageSize)
	if err != nil {
		mapServiceError(c, "list comments", err)
		return
	}

	// make 从空切片开始，保证无评论时响应是 [] 而不是 null。
	items := make([]commentResp, 0, len(list))
	for _, m := range list {
		items = append(items, toCommentResp(m))
	}
	resp := commentListResp{
		List:     items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	response.OK(c, resp)
}

// Create godoc
// @Summary      发表评论
// @Tags         comments
// @Accept       json
// @Produce      json
// @Param        id    path  int                        true  "文章 ID"
// @Param        body  body  handler.createCommentReq   true  "评论内容"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.commentResp}
// @Failure      400  {string}  string    "id 或请求体格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Failure      404  {string}  string    "文章不存在"
// @Security     Bearer
// @Router       /articles/{id}/comments [post]
// Create 处理 POST /api/articles/:id/comments。
// 三个输入的来源要分清楚：文章 id 来自路径，作者 id 来自 JWT，内容来自请求体。
func (h *CommentHandler) Create(c *gin.Context) {
	// 路由挂在 JWTAuth 分组下；currentUserID 再兜一层，避免 id 为 0 时写出脏数据。
	userID, ok := currentUserID(c)
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

	m, err := h.svc.Create(c.Request.Context(), uint(articleID), userID, req.Content)
	if err != nil {
		mapServiceError(c, "create comment", err)
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
// @Success      200  {object}  handler.SwaggerEnvelope{data=object}
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Failure      403  {string}  string    "只能删除自己的评论"
// @Failure      404  {string}  string    "评论不存在"
// @Security     Bearer
// @Router       /comments/{id} [delete]
// Delete 处理 DELETE /api/comments/:id。
// 这条路由的 :id 是评论 id，与 Create 里的文章 id 不是一回事。
func (h *CommentHandler) Delete(c *gin.Context) {
	userID, ok := currentUserID(c)
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
		mapServiceError(c, "delete comment", err)
		return
	}

	// 当前 response 包只有 OK(200)，删除成功是 200 且 data 为 null。
	// 00 章合同里的 204 要等响应包扩展 NoContent 之后一起改。
	response.OK(c, nil)
}
