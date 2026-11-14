// Package httputil 存放 HTTP 层共享的小工具：错误映射、取当前用户、分页解析、Swagger 壳。
// 这些函数原本散落在 handler 包，被所有接口共用。按模块拆分后，
// 每个模块包（user / article / comment / like / notification）都从这里取，避免重复定义。
package httputil

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/pkg/response"
)

// MapServiceError 把 service 与 GORM 的错误映射成统一的 HTTP 响应。
// 所有 handler 共用这一处，避免每个接口写一遍 switch 导致口径漂移。
func MapServiceError(c *gin.Context, operation string, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeInvalidInput, "参数不符合要求")
	case errors.Is(err, domain.ErrNotFound):
		response.ErrorWithCode(c, http.StatusNotFound, response.ErrCodeNotFound, "资源不存在")
	case errors.Is(err, gorm.ErrRecordNotFound):
		response.ErrorWithCode(c, http.StatusNotFound, response.ErrCodeNotFound, "资源不存在")
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeInvalidReference, "引用的资源不存在")
	case errors.Is(err, domain.ErrFloorNotActive):
		response.ErrorWithCode(c, http.StatusConflict, response.ErrCodeFloorNotActive, "活动未开始或已结束")
	case errors.Is(err, domain.ErrFloorTaken):
		response.ErrorWithCode(c, http.StatusConflict, response.ErrCodeFloorTaken, "这一楼已经被抢走了")
	case errors.Is(err, gorm.ErrDuplicatedKey):
		response.ErrorWithCode(c, http.StatusConflict, response.ErrCodeAlreadyExists, "资源已存在")
	default:
		LogRequestError(c, operation, err)
		response.ErrorWithCode(c, http.StatusInternalServerError, response.ErrCodeInternal, "服务暂时不可用，请稍后重试")
	}
}

// CurrentUserID 取出 JWTAuth 放进 Gin context 的作者 id。
// GetUint 在类型断言失败时返回 0 且不报错，0 会被当成有效 id 写出脏数据。
func CurrentUserID(c *gin.Context) (uint, bool) {
	id := c.GetUint("userID")
	if id == 0 {
		return 0, false
	}
	return id, true
}

// LogRequestError 只记录内部错误，Gin Logger 已负责每个请求的访问日志。
// 带上 request_id，用户拿着响应头里的编号来问，日志里能直接对上同一次请求。
func LogRequestError(c *gin.Context, operation string, err error) {
	log.Printf("%s failed: request_id=%s method=%s route=%s error=%v",
		operation, c.GetString(response.RequestIDKey), c.Request.Method, c.FullPath(), err)
}
