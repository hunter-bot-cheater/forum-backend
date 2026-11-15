package router

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// readyTimeout 是一次就绪检查的总预算。
// 探活方（Nginx、监控、穿透工具）一般 1~3 秒就超时，
// 这里必须比它短，否则对方先断开，我们还在等数据库。
const readyTimeout = 2 * time.Second

// healthCheck 是一个依赖探活项。
// name 会作为响应里 checks 的键（mysql / redis），check 返回 nil 表示这个依赖可用。
// 用函数而不是直接传 *gorm.DB，是为了单元测试能塞假检查，不必真的连库。
type healthCheck struct {
	name  string
	check func(ctx context.Context) error
}

// healthResp 是 /healthz 与 /readyz 的响应体。
// 故意不套 {code, message, data} 那层业务壳：探活方只看状态码和 status 字段，
// 套上壳反而让 Nginx、K8s、监控面板要写额外的解析规则。
type healthResp struct {
	Status string            `json:"status"` // ok 或 fail
	Checks map[string]string `json:"checks,omitempty"`
}

// registerHealth 挂上两个运维用的探活接口，都放在根路径、不挂鉴权与限流：
//
//	GET /healthz  存活检查，只要进程还能处理请求就返回 200，不查任何外部依赖；
//	GET /readyz   就绪检查，逐个探测 MySQL / Redis，任一失败返回 503。
//
// 两者必须分开：存活检查如果也查数据库，MySQL 抖动一下就会被外部工具判定为进程死了、
// 直接重启，故障反而被放大；就绪检查失败只是把流量摘走，进程继续活着等依赖恢复。
func registerHealth(r *gin.Engine, checks []healthCheck) {
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, healthResp{Status: "ok"})
	})

	r.GET("/readyz", func(c *gin.Context) {
		// 总预算 2 秒，顺序探测：第一项超时后 ctx 已过期，第二项会立即失败，
		// 所以最坏情况也是 2 秒左右，不会累加到 4 秒。
		ctx, cancel := context.WithTimeout(c.Request.Context(), readyTimeout)
		defer cancel()

		results := make(map[string]string, len(checks))
		failed := false
		for _, hc := range checks {
			if err := hc.check(ctx); err != nil {
				// 具体错误只写日志：里面有连接地址、驱动报错，不宜回给调用方。
				log.Printf("readyz check %s failed: %v", hc.name, err)
				results[hc.name] = "fail"
				failed = true
				continue
			}
			results[hc.name] = "ok"
		}

		if failed {
			c.JSON(http.StatusServiceUnavailable, healthResp{Status: "fail", Checks: results})
			return
		}
		c.JSON(http.StatusOK, healthResp{Status: "ok", Checks: results})
	})
}
