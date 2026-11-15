package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/logging"
	"forum-backend/pkg/response"
)

// HeaderRequestID 是客户端可以自带的追踪号请求头。
const HeaderRequestID = "X-Request-ID"

const maxRequestIDLen = 64

// RequestID 给每个请求分配一个追踪号。
// 这个号会同时出现在四个地方：响应头（用户能拿到）、gin.Context（下游代码能取到）、
// 请求的 context.Context（日志自动带上）、错误响应体（前端报错弹窗能显示）。
// 用户报障时报这个号，日志里能直接搜到同一次请求的全部记录。
//
// 允许客户端自带 X-Request-ID，方便前端或网关把一次用户操作的多个请求串起来；
// 但必须先过滤，因为外来值最终会写进日志。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderRequestID)
		if !validRequestID(id) {
			id = newRequestID()
		}
		c.Set(response.RequestIDKey, id)
		c.Header(HeaderRequestID, id)
		// 除了 gin.Context，还要塞进请求的 ctx：
		// service / worker / GORM 只拿得到 context.Context，看不到 gin.Context。
		// 塞进去之后它们用 slog.ErrorContext 打日志就会自动带上 request_id。
		c.Request = c.Request.WithContext(logging.WithRequestID(c.Request.Context(), id))
		c.Next()
	}
}

// validRequestID 只接受字母、数字、下划线、连字符，长度不超过 64。
// 客户端传来的值会进日志和响应头，先过滤掉换行与控制字符，避免日志注入
// （有人塞个 \n 就能在日志里伪造一行假记录）。
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if ch >= 'a' && ch <= 'z' {
			continue
		}
		if ch >= 'A' && ch <= 'Z' {
			continue
		}
		if ch >= '0' && ch <= '9' {
			continue
		}
		if ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

// newRequestID 用 16 字节随机数生成 32 位十六进制字符串。
// crypto/rand.Read 在 Go 1.24 之后保证不返回错误，这里仍留一个时间戳兜底。
func newRequestID() string {
	buf := make([]byte, 16)
	n, err := rand.Read(buf)
	if err != nil || n != len(buf) {
		return "req-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}
