package router

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/middleware"
	"forum-backend/pkg/response"
)

// installFallbacks 接管 gin 默认的几个「裸奔」行为：
// 无路由返回纯文本 404、方法不匹配返回纯文本 405、panic 返回空响应。
// 这三条路径前端都会解析失败，所以统一换成和业务接口一样的 JSON 结构。
//
// 单独放一个文件，是为了让契约测试可以直接装配它，不用构造真实 DB / Redis 依赖。
func installFallbacks(r *gin.Engine) {
	// gin 默认对「路径存在但方法不对」直接返回 404。打开这个开关才会走到 NoMethod，
	// 前端才能区分「接口路径写错了」和「方法用错了」。
	r.HandleMethodNotAllowed = true

	// 下面三个中间件的顺序不能乱：
	// 1. Logger 记录每个请求的访问日志
	// 2. RequestID 生成追踪号，必须排在 Recovery 前面，
	//    否则 panic 日志里取不到 request_id，线上排查会断链
	// 3. CustomRecovery 捕获 panic，换成我们的 handlePanic
	//
	// 探活接口不进访问日志：它们会被外部每隔几秒调一次，
	// 一天几万条 ok 日志会把真正有用的业务日志冲掉。
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		SkipPaths: []string{"/healthz", "/readyz"},
	}))
	r.Use(middleware.RequestID())
	r.Use(gin.CustomRecovery(handlePanic))

	// 请求打到了不存在的路径。
	r.NoRoute(func(c *gin.Context) {
		response.ErrorWithCode(c, http.StatusNotFound, response.ErrCodeRouteNotFound, "接口不存在")
	})

	// 路径对但 HTTP 方法不对，例如对只支持 GET 的接口发了 POST。
	r.NoMethod(func(c *gin.Context) {
		response.ErrorWithCode(c, http.StatusMethodNotAllowed, response.ErrCodeMethodNotAllowed, "请求方法不支持")
	})
}

// handlePanic 是 panic 的兜底出口：细节只留在服务端日志，客户端拿到通用文案加追踪号。
// 不把 panic 内容返回给前端，是因为里面可能带 SQL 语句、文件路径这类内部信息。
func handlePanic(c *gin.Context, recovered any) {
	id := c.GetString(response.RequestIDKey)
	log.Printf("panic recovered: request_id=%s method=%s path=%s error=%v",
		id, c.Request.Method, c.Request.URL.Path, recovered)
	response.ErrorWithCode(c, http.StatusInternalServerError, response.ErrCodeInternal, "服务暂时不可用，请稍后重试")
	c.Abort()
}
