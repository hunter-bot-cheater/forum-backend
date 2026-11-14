package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// limitBody 限制请求体字节数，防止超大 body 占满内存。
func limitBody(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}
