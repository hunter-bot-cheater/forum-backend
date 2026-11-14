// Package response 统一 API 响应格式：{ code, message, data }。
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 业务码。所有失败共用 CodeError，具体类型看 error_code 字段。
const (
	CodeOK    = 0
	CodeError = 1
)

// RequestIDKey 是 request id 在 gin.Context 里的键。
// middleware 写入，pkg/response 读取，依赖方向保持单向。
const RequestIDKey = "request_id"

// 可机读的错误码。前端按这个字段分支，改中文文案不会破坏判断。
const (
	ErrCodeBadRequest       = "bad_request"
	ErrCodeUnauthorized     = "unauthorized"
	ErrCodeTokenInvalid     = "token_invalid"
	ErrCodeForbidden        = "forbidden"
	ErrCodeNotFound         = "not_found"
	ErrCodeRouteNotFound    = "route_not_found"
	ErrCodeMethodNotAllowed = "method_not_allowed"
	ErrCodeConflict         = "conflict"
	ErrCodeInvalidInput     = "invalid_input"
	ErrCodeInvalidReference = "invalid_reference"
	ErrCodeAlreadyExists    = "already_exists"
	ErrCodeFloorNotActive   = "floor_not_active"
	ErrCodeFloorTaken       = "floor_taken"
	ErrCodeRateLimited      = "rate_limited"
	ErrCodeInternal         = "internal_error"
)

// JSON 统一成功响应：{ code, message, data }。
func JSON(c *gin.Context, httpStatus, code int, message string, data any) {
	c.JSON(httpStatus, gin.H{
		"code":    code,
		"message": message,
		"data":    data,
	})
}

// OK 成功。
func OK(c *gin.Context, data any) {
	JSON(c, http.StatusOK, CodeOK, "ok", data)
}

// Error 失败。error_code 按 HTTP 状态码推导，调用方不用改。
func Error(c *gin.Context, httpStatus int, message string) {
	ErrorWithCode(c, httpStatus, "", message)
}

// ErrorWithCode 失败，并给出可机读的错误码。
// 重点路径用它细分语义，比如活动未开始与楼层被抢走都是 409，
// 靠 floor_not_active 与 floor_taken 区分，前端不用读中文文案。
func ErrorWithCode(c *gin.Context, httpStatus int, errorCode string, message string) {
	if errorCode == "" {
		errorCode = defaultErrorCode(httpStatus)
	}
	if httpStatus == http.StatusUnauthorized {
		// 401 按 RFC 7235 带上认证方案，标准 HTTP 客户端能识别。
		c.Header("WWW-Authenticate", `Bearer realm="forum"`)
	}
	c.JSON(httpStatus, gin.H{
		"code":       CodeError,
		"message":    message,
		"data":       nil,
		"error_code": errorCode,
		"request_id": c.GetString(RequestIDKey),
	})
}

// defaultErrorCode 给未显式指定错误码的调用点一个稳定兜底。
func defaultErrorCode(httpStatus int) string {
	switch httpStatus {
	case http.StatusBadRequest:
		return ErrCodeBadRequest
	case http.StatusUnauthorized:
		return ErrCodeUnauthorized
	case http.StatusForbidden:
		return ErrCodeForbidden
	case http.StatusNotFound:
		return ErrCodeNotFound
	case http.StatusMethodNotAllowed:
		return ErrCodeMethodNotAllowed
	case http.StatusConflict:
		return ErrCodeConflict
	case http.StatusTooManyRequests:
		return ErrCodeRateLimited
	default:
		if httpStatus >= http.StatusInternalServerError {
			return ErrCodeInternal
		}
		return "http_error"
	}
}
