package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/response"
)

// RequireAdmin 只放行配置里的管理员，必须挂在 JWTAuth 之后。
//
// 它读的是 JWTAuth 写进 context 的 user id，所以顺序不能反：
// 挂在前面时谁都会拿到 0，结果是所有人都被拒，看起来像「管理员也进不去」。
//
// 白名单在构造时转成 map，每个请求只做一次哈希查找。
// 名单通常只有几个人，用切片逐个比对也能跑，但这是热路径上的接口，
// 没有理由为省一行代码让它线性扫描。
func RequireAdmin(adminIDs []uint) gin.HandlerFunc {
	allowed := make(map[uint]struct{}, len(adminIDs))
	for _, id := range adminIDs {
		// 0 不是合法用户 id，即使配置里写了也要跳过，
		// 否则「未登录」的请求（取出的 id 是 0）会被当成管理员放行。
		if id != 0 {
			allowed[id] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		userID := c.GetUint(userIDKey)
		if _, ok := allowed[userID]; !ok {
			// 403 而不是 404：这里不隐藏资源，路径本身是公开已知的写接口，
			// 让人知道「你不是管理员」比假装接口不存在更好排查。
			response.ErrorWithCode(c, http.StatusForbidden, response.ErrCodeForbidden, "没有权限执行这个操作")
			c.Abort()
			return
		}
		c.Next()
	}
}
