package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/response"
)

// HeaderRequestID 是客户端可以自带的追踪号请求头。
const HeaderRequestID = "X-Request-ID"

const maxRequestIDLen = 64

// RequestID 给每个请求分配一个追踪号，写进响应头、gin.Context 和错误响应体。
// 用户报障时把响应头里的编号给后端，日志里能直接搜到同一次请求。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderRequestID)
		if !validRequestID(id) {
			id = newRequestID()
		}
		c.Set(response.RequestIDKey, id)
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

// validRequestID 只接受字母、数字、下划线、连字符，长度不超过 64。
// 客户端传来的值会进日志和响应头，先过滤掉换行与控制字符，避免日志注入。
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
