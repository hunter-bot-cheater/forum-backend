package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/response"
)

// BanChecker 回答「这个用户被封到什么时候」，零值表示没被封。
//
// 接口定义在消费方这一侧而不是 ban 包里：middleware 不该 import 具体的存储实现，
// 否则每个只用到中间件的测试都得连一次数据库。实现由 router 注入 ban.Store。
type BanChecker interface {
	BannedUntil(ctx context.Context, userID uint) (time.Time, error)
}

// RequireActive 拦住被封禁用户的写操作，必须挂在 JWTAuth 之后。
//
// 只拦写不拦读：封禁的目的是不让这个人继续制造内容，
// 而不是让他连自己以前发过的东西都看不了。连读一起封等于把人从站内彻底抹掉，
// 也无助于他弄明白自己为什么被处罚。
func RequireActive(checker BanChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetUint(userIDKey)
		until, err := checker.BannedUntil(c.Request.Context(), userID)
		if err != nil {
			// 查不到封禁状态时拒绝写入。此时数据库本身已经不可用，
			// 放行只会让被处罚的人趁故障窗口继续发，而写库迟早也会失败。
			response.ErrorWithCode(c, http.StatusInternalServerError, response.ErrCodeInternal, "服务暂时不可用，请稍后重试")
			c.Abort()
			return
		}
		if until.IsZero() {
			c.Next()
			return
		}

		// 解除时间写进文案：用户最想知道的是「什么时候能再发」。
		// 只说「你被封了」通常立刻换来一条询问何时解封的工单。
		// 格式化前先转本地时区：DSN 里 loc 设的是 Local，读出来本该已经是本地时间，
		// 这里再转一次是为了让文案不受将来改动 DSN 时区的影响。
		// 不加秒：封禁精度到天，多两位数字反而让人以为是精确到秒的到期时刻。
		response.ErrorWithCode(c, http.StatusForbidden, response.ErrCodeBanned,
			"账号已被限制发言，解除时间 "+until.Local().Format("2006-01-02 15:04"))
		c.Abort()
	}
}
