// Package middleware 存放 Gin 中间件。
// 中间件是「handler 之前先跑的一段逻辑」：c.Next() 表示继续往下走，
// c.Abort() 表示到此为止，后面的中间件和 handler 都不再执行。
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/jwt"
	"forum-backend/pkg/response"
)

// JWTAuth 校验请求头里的登录凭证，通过后把用户 id 放进 Gin context。
//
// 挂在需要登录的路由组上（见 internal/router/router.go），
// handler 里用 httputil.CurrentUserID(c) 读回这个 id，不用再自己解析 token。
// 任何失败都返回 401 并中断，绝不能「校验失败但继续往下跑」。
func JWTAuth(jwtManager *jwt.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 约定格式：Authorization: Bearer <token>。
		// strings.Cut 按第一个空格切成 scheme 与 token 两段，只会分配两个字符串，比 Split 省。
		authorization := c.GetHeader("Authorization")
		scheme, tokenString, ok := strings.Cut(authorization, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(tokenString) == "" {
			// 没带 token 是常见的客户端错误，Gin 访问日志已记录 401，不重复打错误日志。
			response.ErrorWithCode(c, http.StatusUnauthorized, response.ErrCodeUnauthorized, "未登录")
			c.Abort()
			return
		}

		// 验签和过期检查都在 Parse 里完成，失败只可能是签名不对或已过期。
		userID, err := jwtManager.Parse(strings.TrimSpace(tokenString))
		if err != nil {
			// 同样属于预期的 401，不要把 token 内容写进日志。
			response.ErrorWithCode(c, http.StatusUnauthorized, response.ErrCodeTokenInvalid, "登录已过期，请重新登录")
			c.Abort()
			return
		}

		// "userID" 是全项目的约定键名，别处读取都靠这个字符串，改名要全局搜。
		c.Set("userID", userID)
		c.Next()
	}
}
