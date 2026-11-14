package router

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/middleware"
	"forum-backend/pkg/response"
)

// installFallbacks 接管 gin.Default 里返回纯文本的几个默认行为。
// 无路由、方法不匹配、panic 三条路径都改成统一 JSON 响应体。
// 单独放一个文件，是为了让契约测试可以直接装配它，不用构造真实 DB/Redis 依赖。
func installFallbacks(r *gin.Engine) {
	r.HandleMethodNotAllowed = true
	r.Use(gin.Logger())
	r.Use(middleware.RequestID())
	r.Use(gin.CustomRecovery(handlePanic))

	r.NoRoute(func(c *gin.Context) {
		response.ErrorWithCode(c, http.StatusNotFound, response.ErrCodeRouteNotFound, "接口不存在")
	})

	r.NoMethod(func(c *gin.Context) {
		response.ErrorWithCode(c, http.StatusMethodNotAllowed, response.ErrCodeMethodNotAllowed, "请求方法不支持")
	})
}

// handlePanic 把 panic 细节留在服务端日志，客户端只拿到通用文案和追踪号。
func handlePanic(c *gin.Context, recovered any) {
	id := c.GetString(response.RequestIDKey)
	log.Printf("panic recovered: request_id=%s method=%s path=%s error=%v",
		id, c.Request.Method, c.Request.URL.Path, recovered)
	response.ErrorWithCode(c, http.StatusInternalServerError, response.ErrCodeInternal, "服务暂时不可用，请稍后重试")
	c.Abort()
}
