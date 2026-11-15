package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// limitBody 限制请求体最多 n 字节，防止超大 body 占满内存。
//
// MaxBytesReader 不是「超过就丢弃」而是「读到第 n+1 个字节时返回错误」：
// 请求会被放进来，但 handler 里解析 body 时会失败，于是返回 400。
// 这样不用在中间件里预读一遍 body，也不影响正常的流式处理。
func limitBody(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}
