// Package httputil 存放 HTTP 层共享的小工具：错误映射、取当前用户、分页解析、Swagger 壳。
// 这些函数原本散落在 handler 包，被所有接口共用。按模块拆分后，
// 每个模块包（user / article / comment / like / notification）都从这里取，避免重复定义。
package httputil

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/pkg/response"
)

// MapServiceError 把 service 层与 GORM 的错误翻译成统一的 HTTP 响应。
// 所有 handler 共用这一处，避免每个接口各写一遍 switch 导致状态码和文案漂移。
//
// switch 按顺序匹配、命中第一个就返回，所以具体错误要排在笼统的前面。
// 能识别的错误都不打日志（属于业务预期，比如没登录、被抢走），
// 落到 default 的才是真正的内部故障，那时才记 error 日志并返回 500。
func MapServiceError(c *gin.Context, operation string, err error) {
	switch {
	// 对自己做只允许对别人做的动作：单独立一条，文案比「参数不符合要求」清楚。
	case errors.Is(err, domain.ErrSelfAction):
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeInvalidInput, "不能对自己执行这个操作")
	// 参数不合法，通常是 service 校验输入后返回的。
	case errors.Is(err, domain.ErrInvalidInput):
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeInvalidInput, "参数不符合要求")
	// 我们自己定义的「查不到」，和 GORM 的「查不到」都要映射成 404。
	case errors.Is(err, domain.ErrNotFound):
		response.ErrorWithCode(c, http.StatusNotFound, response.ErrCodeNotFound, "资源不存在")
	case errors.Is(err, gorm.ErrRecordNotFound):
		response.ErrorWithCode(c, http.StatusNotFound, response.ErrCodeNotFound, "资源不存在")
	// 外键约束失败说明请求里引用的 id 不存在（比如给不存在的文章发评论），是客户端问题。
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeInvalidReference, "引用的资源不存在")
	// 抢楼两种失败都是 409，靠 error_code 区分是活动关了还是楼层被抢。
	case errors.Is(err, domain.ErrFloorNotActive):
		response.ErrorWithCode(c, http.StatusConflict, response.ErrCodeFloorNotActive, "活动未开始或已结束")
	case errors.Is(err, domain.ErrFloorTaken):
		response.ErrorWithCode(c, http.StatusConflict, response.ErrCodeFloorTaken, "这一楼已经被抢走了")
	// 重复提交与「资源已存在」不同：这里没有具体的唯一键冲突，
	// 是同一份内容在短时间内又发了一次，文案要让用户知道改一下再发就行。
	case errors.Is(err, domain.ErrDuplicateContent):
		response.ErrorWithCode(c, http.StatusConflict, response.ErrCodeAlreadyExists, "刚刚发过一模一样的内容，改一下再发吧")
	case errors.Is(err, domain.ErrRateLimited):
		response.ErrorWithCode(c, http.StatusTooManyRequests, response.ErrCodeRateLimited, "操作太频繁，请稍后再试")
	// 重复举报和重复处置都是 409，但原因完全不同：前者要用户等着，
	// 后者要管理员刷新列表看别人的处理结果，所以文案必须分开。
	case errors.Is(err, domain.ErrAlreadyReported):
		response.ErrorWithCode(c, http.StatusConflict, response.ErrCodeAlreadyExists, "你已经举报过这条内容，正在等待处理")
	case errors.Is(err, domain.ErrReportHandled):
		response.ErrorWithCode(c, http.StatusConflict, response.ErrCodeConflict, "这条举报已经被处理过了")
	case errors.Is(err, gorm.ErrDuplicatedKey):
		response.ErrorWithCode(c, http.StatusConflict, response.ErrCodeAlreadyExists, "资源已存在")
	default:
		// 真正没预料到的错误：细节进日志（带 request_id），客户端只拿到通用文案。
		LogRequestError(c, operation, err)
		response.ErrorWithCode(c, http.StatusInternalServerError, response.ErrCodeInternal, "服务暂时不可用，请稍后重试")
	}
}

// CurrentUserID 取出 JWTAuth 放进 Gin context 的作者 id。
// 只有挂了 JWTAuth 的路由组里才能取到 true；公开接口调用会拿到 0 和 false。
//
// 为什么不用 c.GetInt64 之类的直接取：GetUint 在键不存在或类型不对时返回 0 且不报错，
// 而 0 不是合法用户 id，若不检查就会被当成有效作者写进数据库，产生无人认领的脏数据。
func CurrentUserID(c *gin.Context) (uint, bool) {
	id := c.GetUint("userID")
	if id == 0 {
		return 0, false
	}
	return id, true
}

// LogRequestError 只用于记录内部错误，Gin Logger 已负责每个请求的访问日志，不要重复记。
// 带上 request_id：用户拿着响应头里的编号来问，日志里能直接对上同一次请求。
// 注意只记 operation 和 error，不要打印整个请求体，里面可能有密码和 token。
func LogRequestError(c *gin.Context, operation string, err error) {
	slog.ErrorContext(c.Request.Context(), "request failed",
		"operation", operation, "method", c.Request.Method, "route", c.FullPath(), "error", err)
}
