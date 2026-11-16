package httputil

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/domain"
	"forum-backend/internal/paging"
)

// MaxPageSize 每页最多返回多少条。
// 数值与 paging.MaxPageSize 是同一个来源，这里保留别名是为了不破坏已有的引用点。
const MaxPageSize = paging.MaxPageSize

// DefaultCursorLimit 是游标分页不给 limit 时的条数。
const DefaultCursorLimit = paging.DefaultCursorLimit

// ParsePage 从查询串里解析 page 与 page_size，缺省为第 1 页、每页 10 条。
// 返回错误时一定是 domain.ErrInvalidInput，调用方交给 MapServiceError 转成 400。
//
// 本函数只负责「把字符串取下来」，缺省值、上下界、溢出检查全部交给 paging.New：
// handler 与 service 走同一份规则，不会出现两边上界不一致的情况。
//
// service 层用这两个值算 Offset：offset = (page - 1) * pageSize，
// 更推荐直接调 paging.New 拿 Query，再调它的 Offset()。
func ParsePage(c *gin.Context) (int, int, error) {
	page := 1
	pageSize := paging.DefaultPageSize

	// page：页码，从 1 开始。传字母算参数错误。
	rawPage := c.Query("page")
	if rawPage != "" {
		// Atoi 把字符串转成 int，转换失败会返回 error 而不是 0。
		v, err := strconv.Atoi(rawPage)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: page must be a positive integer", domain.ErrInvalidInput)
		}
		page = v
	}

	// page_size：每页条数，范围由 paging.New 统一判定。
	rawSize := c.Query("page_size")
	if rawSize != "" {
		v, err := strconv.Atoi(rawSize)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: page_size must be between 1 and %d", domain.ErrInvalidInput, paging.MaxPageSize)
		}
		pageSize = v
	}

	// 真正判上下界与溢出的是这一句，负数、0、超大值都在这里被挡住。
	q, err := paging.New(page, pageSize)
	if err != nil {
		return 0, 0, err
	}
	return q.Page, q.PageSize, nil
}

// ParseCursor 从查询串里解析游标分页参数：before_id 与 limit。
//
// before_id 缺省为 0，表示「从最新一条往回取」；传上一页返回的
// next_before_id 就是继续往更早翻。limit 缺省 20，范围 1~MaxPageSize。
//
// 为什么消息列表不用 page：记录一直在往后追加，页码会漂。详见 message.MessageQuery。
func ParseCursor(c *gin.Context) (uint, int, error) {
	limit := paging.DefaultCursorLimit
	rawLimit := c.Query("limit")
	if rawLimit != "" {
		v, err := strconv.Atoi(rawLimit)
		if err != nil || v < 1 || v > paging.MaxPageSize {
			return 0, 0, fmt.Errorf("%w: limit must be between 1 and %d", domain.ErrInvalidInput, paging.MaxPageSize)
		}
		limit = v
	}

	var beforeID uint
	rawBefore := c.Query("before_id")
	if rawBefore != "" {
		// 用 ParseUint 而不是 Atoi：id 不可能是负数，负数直接算参数错误，
		// 免得 uint(-1) 变成一个巨大的数去查库。
		v, err := strconv.ParseUint(rawBefore, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: before_id must be a non-negative integer", domain.ErrInvalidInput)
		}
		beforeID = uint(v)
	}
	return beforeID, limit, nil
}
