package httputil

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/domain"
)

// MaxPageSize 每页最多返回多少条。
// 不设上限的话，有人传 page_size=1000000 就能把整张表拉进内存。
const MaxPageSize = 50

// ParsePage 从查询串里解析 page 与 page_size，缺省为第 1 页、每页 10 条。
// 返回错误时一定是 domain.ErrInvalidInput，调用方交给 MapServiceError 转成 400。
//
// service 层用这两个值算 Offset：offset = (page - 1) * pageSize。
func ParsePage(c *gin.Context) (int, int, error) {
	page := 1
	pageSize := 10

	// page：页码，从 1 开始。传 0 或负数、传字母都算参数错误。
	rawPage := c.Query("page")
	if rawPage != "" {
		// Atoi 把字符串转成 int，转换失败会返回 error 而不是 0。
		v, err := strconv.Atoi(rawPage)
		if err != nil || v < 1 {
			return 0, 0, fmt.Errorf("%w: page must be a positive integer", domain.ErrInvalidInput)
		}
		page = v
	}

	// page_size：每页条数，限制在 1 到 MaxPageSize 之间。
	rawSize := c.Query("page_size")
	if rawSize != "" {
		v, err := strconv.Atoi(rawSize)
		if err != nil || v < 1 || v > MaxPageSize {
			return 0, 0, fmt.Errorf("%w: page_size must be between 1 and 50", domain.ErrInvalidInput)
		}
		pageSize = v
	}

	// 极大 page 会让 (page-1)*pageSize 溢出 int，变成负数 Offset 查出错乱的数据。
	// 乘之前先判上界：用 int 最大值除以每页条数，超过就拒绝。
	if page > (1<<31-1)/pageSize {
		return 0, 0, fmt.Errorf("%w: page is too large", domain.ErrInvalidInput)
	}
	return page, pageSize, nil
}
