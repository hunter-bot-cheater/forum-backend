package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"forum-backend/internal/service"
	"forum-backend/pkg/response"
)

// mapServiceError 把 service 与 GORM 的错误映射成统一的 HTTP 响应。
// 所有 handler 共用这一处，避免每个接口写一遍 switch 导致口径漂移。
func mapServiceError(c *gin.Context, operation string, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		response.Error(c, http.StatusBadRequest, "参数不符合要求")
	case errors.Is(err, service.ErrNotFound):
		response.Error(c, http.StatusNotFound, "资源不存在")
	case errors.Is(err, gorm.ErrRecordNotFound):
		response.Error(c, http.StatusNotFound, "资源不存在")
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		response.Error(c, http.StatusBadRequest, "引用的资源不存在")
	case errors.Is(err, gorm.ErrDuplicatedKey):
		response.Error(c, http.StatusConflict, "资源已存在")
	default:
		logRequestError(c, operation, err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用，请稍后重试")
	}
}

// currentUserID 取出 JWTAuth 放进 Gin context 的作者 id。
// GetUint 在类型断言失败时返回 0 且不报错，0 会被当成有效 id 写出脏数据。
func currentUserID(c *gin.Context) (uint, bool) {
	id := c.GetUint("userID")
	if id == 0 {
		return 0, false
	}
	return id, true
}
