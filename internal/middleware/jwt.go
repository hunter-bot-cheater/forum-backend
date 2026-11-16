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

// userIDKey 是登录用户 id 在 Gin context 里的键。
// 全项目（JWTAuth、OptionalJWTAuth、httputil.CurrentUserID）共用这一个常量，
// 改名时编译器会帮忙找，不用靠全局搜索字符串。
const userIDKey = "userID"

// JWTAuth 校验请求头里的登录凭证，通过后把用户 id 放进 Gin context。
//
// 挂在需要登录的路由组上（见 internal/router/router.go），
// handler 里用 httputil.CurrentUserID(c) 读回这个 id，不用再自己解析 token。
// 任何失败都返回 401 并中断，绝不能「校验失败但继续往下跑」。
func JWTAuth(jwtManager *jwt.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString, ok := bearerToken(c)
		if !ok {
			// 没带 token 是常见的客户端错误，Gin 访问日志已记录 401，不重复打错误日志。
			response.ErrorWithCode(c, http.StatusUnauthorized, response.ErrCodeUnauthorized, "未登录")
			c.Abort()
			return
		}

		// 验签和过期检查都在 Parse 里完成，失败只可能是签名不对或已过期。
		userID, err := jwtManager.ParseAccess(tokenString)
		if err != nil {
			// 同样属于预期的 401，不要把 token 内容写进日志。
			response.ErrorWithCode(c, http.StatusUnauthorized, response.ErrCodeTokenInvalid, "登录已过期，请重新登录")
			c.Abort()
			return
		}

		c.Set(userIDKey, userID)
		c.Next()
	}
}

// OptionalJWTAuth 解析登录凭证，解析成功就写入用户 id，失败或没带都放行。
//
// 给「公开但想知道访问者是谁」的接口用：个人主页要算 is_following，
// 关注与粉丝列表要算 followed_by_me。游客必须能看，登录用户要多一点上下文。
//
// token 过期时静默降级为游客，不返回 401：
// 主页是公开内容，因为一个过期 token 就打不开页面，用户会以为站点坏了。
// 需要登录信息的写接口一律用 JWTAuth，不要用这个。
func OptionalJWTAuth(jwtManager *jwt.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString, ok := bearerToken(c)
		if !ok {
			c.Next()
			return
		}

		// 解析失败直接当游客处理。这里不打日志：
		// 公开接口被爬虫或旧标签页带着过期 token 轮询是常态，记下来只会刷屏。
		userID, err := jwtManager.ParseAccess(tokenString)
		if err != nil {
			c.Next()
			return
		}

		c.Set(userIDKey, userID)
		c.Next()
	}
}

// bearerToken 从 Authorization 头里取出 token。
// 两个中间件共用这段解析，格式只有一处定义：
// 约定 Authorization: Bearer <token>，scheme 大小写不敏感。
func bearerToken(c *gin.Context) (string, bool) {
	// strings.Cut 按第一个空格切成 scheme 与 token 两段，只分配两个字符串，比 Split 省。
	scheme, tokenString, ok := strings.Cut(c.GetHeader("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		return "", false
	}
	return tokenString, true
}
