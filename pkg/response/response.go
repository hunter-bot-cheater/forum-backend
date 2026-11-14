// Package response 统一 API 响应格式：{ code, message, data }。
package response

import "github.com/gin-gonic/gin"

// 业务码
const (
	CodeOK    = 0
	CodeError = 1
)

// JSON 统一成功/失败响应
func JSON(c *gin.Context, httpStatus, code int, message string, data any) {
	c.JSON(httpStatus, gin.H{
		"code":    code,
		"message": message,
		"data":    data,
	})
}

// OK 成功
func OK(c *gin.Context, data any) {
	JSON(c, 200, CodeOK, "ok", data)
}

// Error 失败
func Error(c *gin.Context, httpStatus int, message string) {
	JSON(c, httpStatus, CodeError, message, nil)
}
