// Package response 定义全项目统一的 API 响应格式。
//
// 成功：{ "code": 0, "message": "ok", "data": ... }
// 失败：{ "code": 1, "message": "人看的文案", "data": null,
//
//	"error_code": "机读的码", "request_id": "追踪号" }
//
// 三套信息各管一件事，别混用：
//   - HTTP 状态码：给通用客户端和网关看的（200 / 401 / 409 / 500）
//   - code：只表示成功还是失败，前端做第一个分支
//   - error_code：细分原因，前端按它走具体逻辑，改中文文案也不会破坏判断
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
// 新增一种失败语义时才加新常量，不要为每个接口各造一个。
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

// JSON 输出统一成功响应。
// handler 里不要在成功路径手写 c.JSON，否则字段名和结构会各处漂移，前端得写多套拆包。
func JSON(c *gin.Context, httpStatus, code int, message string, data any) {
	c.JSON(httpStatus, gin.H{
		"code":    code,
		"message": message,
		"data":    data,
	})
}

// OK 是成功响应的常用入口，等价于 200 + code 0。
// data 允许为 nil，比如删除接口没有返回内容。
func OK(c *gin.Context, data any) {
	JSON(c, http.StatusOK, CodeOK, "ok", data)
}

// Error 输出失败响应，不指定 error_code，由 HTTP 状态码推导一个默认值。
// 语义不够细的地方（如同一状态码有多种原因）改用 ErrorWithCode。
func Error(c *gin.Context, httpStatus int, message string) {
	ErrorWithCode(c, httpStatus, "", message)
}

// ErrorWithCode 输出失败响应，并给出可机读的错误码。
//
// 重点路径用它细分语义，比如「活动未开始」与「楼层被抢走」都是 409，
// 靠 floor_not_active 与 floor_taken 区分，前端不用读中文文案。
// 失败响应固定带 request_id，用户报障时报这个号，日志里能直接定位。
func ErrorWithCode(c *gin.Context, httpStatus int, errorCode string, message string) {
	if errorCode == "" {
		errorCode = defaultErrorCode(httpStatus)
	}
	if httpStatus == http.StatusUnauthorized {
		// 401 按 RFC 7235 要带上认证方案，标准 HTTP 客户端（curl、浏览器、SDK）靠它识别怎么认证。
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
// 只覆盖常见状态码，其余按 5xx / 4xx 归到 internal_error 或 http_error。
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
