// Package middleware 存放 Gin 中间件。
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/jwt"
	"forum-backend/pkg/response"
)

// JWTAuth 校验 Authorization: Bearer <token>，并把 userID 放入 Gin context。
func JWTAuth(jwtManager *jwt.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authorization := c.GetHeader("Authorization")
		scheme, tokenString, ok := strings.Cut(authorization, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(tokenString) == "" {
			// 无 token 是常见客户端错误，Gin 的访问日志会记录 401，不额外打印错误日志。
			response.ErrorWithCode(c, http.StatusUnauthorized, response.ErrCodeUnauthorized, "未登录")
			c.Abort()
			return
		}

		userID, err := jwtManager.Parse(strings.TrimSpace(tokenString))
		if err != nil {
			// 过期或无效 token 也属于预期的 401，不记录 token 内容或重复错误日志。
			response.ErrorWithCode(c, http.StatusUnauthorized, response.ErrCodeTokenInvalid, "登录已过期，请重新登录")
			c.Abort()
			return
		}

		c.Set("userID", userID)
		c.Next()
	}
}
