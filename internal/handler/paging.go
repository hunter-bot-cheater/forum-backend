package handler

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/service"
)

const maxPageSize = 50

// parsePage 解析分页参数。缺省给默认值，越界返回 ErrInvalidInput。
func parsePage(c *gin.Context) (int, int, error) {
	page := 1
	pageSize := 10

	rawPage := c.Query("page")
	if rawPage != "" {
		v, err := strconv.Atoi(rawPage) //strconv.Atoi 是 Go 标准库里用来把字符串转成整数的函数
		if err != nil || v < 1 {
			return 0, 0, fmt.Errorf("%w: page must be a positive integer", service.ErrInvalidInput)
		}
		page = v
	}

	rawSize := c.Query("page_size")
	if rawSize != "" {
		v, err := strconv.Atoi(rawSize)
		if err != nil || v < 1 || v > maxPageSize {
			return 0, 0, fmt.Errorf("%w: page_size must be between 1 and 50", service.ErrInvalidInput)
		}
		pageSize = v
	}

	// 极大 page 会让 Offset 溢出 int，乘之前先算上界。
	if page > (1<<31-1)/pageSize {
		return 0, 0, fmt.Errorf("%w: page is too large", service.ErrInvalidInput)
	}
	return page, pageSize, nil
}
