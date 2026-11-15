package router

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/metrics"
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
	// 1. RequestID 生成追踪号，必须排在最前面，后面所有日志才能带上它；
	//    也必须在 Recovery 之前，否则 panic 日志里取不到 request_id，线上排查会断链
	// 2. AccessLog 记录访问日志，装在 Recovery 外面才能记到 panic 被兜成 500 的结果
	// 3. CustomRecovery 捕获 panic，换成我们的 handlePanic
	//
	// 探活与指标抓取不进访问日志：它们会被外部每隔几秒调一次，
	// 一天几万条 ok 日志会把真正有用的业务日志冲掉，也会污染耗时分位数。
	r.Use(middleware.RequestID())
	r.Use(middleware.AccessLog("/healthz", "/readyz", "/metrics"))
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
	// 计数和日志都记：计数用来配告警（不为 0 就说明有代码在崩），日志用来定位。
	metrics.HTTPPanics.Inc()
	// 用请求的 ctx 打日志，request_id 由 contextHandler 自动补上；
	// 这里不再手写 request_id=%s，否则会重复两个同名字段。
	slog.ErrorContext(c.Request.Context(), "panic recovered",
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"error", fmt.Sprint(recovered),
		"stack", string(debug.Stack()),
	)
	response.ErrorWithCode(c, http.StatusInternalServerError, response.ErrCodeInternal, "服务暂时不可用，请稍后重试")
	c.Abort()
}
