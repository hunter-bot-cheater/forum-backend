package httputil

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/domain"
)

// MaxPageSize 每页最多返回多少条。
const MaxPageSize = 50

// ParsePage 解析分页参数。缺省给默认值，越界返回 domain.ErrInvalidInput。
func ParsePage(c *gin.Context) (int, int, error) {
	page := 1
	pageSize := 10

	rawPage := c.Query("page")
	if rawPage != "" {
		v, err := strconv.Atoi(rawPage) // strconv.Atoi 是 Go 标准库里用来把字符串转成整数的函数
		if err != nil || v < 1 {
			return 0, 0, fmt.Errorf("%w: page must be a positive integer", domain.ErrInvalidInput)
		}
		page = v
	}

	rawSize := c.Query("page_size")
	if rawSize != "" {
		v, err := strconv.Atoi(rawSize)
		if err != nil || v < 1 || v > MaxPageSize {
			return 0, 0, fmt.Errorf("%w: page_size must be between 1 and 50", domain.ErrInvalidInput)
		}
		pageSize = v
	}

	// 极大 page 会让 Offset 溢出 int，乘之前先算上界。
	if page > (1<<31-1)/pageSize {
		return 0, 0, fmt.Errorf("%w: page is too large", domain.ErrInvalidInput)
	}
	return page, pageSize, nil
}
