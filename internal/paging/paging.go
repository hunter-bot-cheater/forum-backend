// Package paging 收敛分页参数校验、偏移量计算与列表响应形状。
//
// 每个列表接口本来都要做三件完全相同的事：给缺省值、判上下界、算 (page-1)*pageSize。
// 这三件事分散在 handler 和 service 里各写一遍时，最容易出的偏差是上界不一致
// （handler 允许 50、service 允许 100），表现是「小页正常、大页报错」，
// 而且只有翻到很深的分页才会发现。所以判断只留这一处。
//
// 这里不 import gin：service 层也要用它，而 service 不应该知道 HTTP 的存在。
// 查询串的解析仍在 httputil.ParsePage，它负责取字符串，校验交给 New。
package paging

import (
	"fmt"

	"forum-backend/internal/domain"
)

const (
	// DefaultPageSize 是不传 page_size 时的每页条数。
	// 10 条对文章列表刚好一屏，对通知、评论这类矮条目偏少，但保持统一口径更重要：
	// 前端每个列表都能用同一个默认值，不用记「这个接口默认 10、那个默认 20」。
	DefaultPageSize = 10

	// MaxPageSize 是每页条数上限。
	// 不设上限的话，传 page_size=1000000 就能把整张表拉进内存。
	// 50 是「一屏放得下」和「一次别查太多」之间的折中。
	MaxPageSize = 50

	// DefaultCursorLimit 是游标分页不给 limit 时的条数。
	// 20 条大约是一屏半，够填满首屏又不会一次拉太多。
	DefaultCursorLimit = 20
)

// Query 是一组已经校验过的分页参数。
//
// 用结构体而不是两个 int 传递，是为了让 Offset 只能从校验过的值算出来：
// 手写 (page-1)*pageSize 的地方很容易漏掉 page=0 的情况
// （GORM 把 Offset(-10) 当成「取消 Offset」，于是 page=0 会静默返回第一页）。
type Query struct {
	Page     int
	PageSize int
}

// New 校验页码与每页条数，返回可直接用于查询的 Query。
//
// 返回的错误一定是 domain.ErrInvalidInput，调用方交给 MapServiceError 转成 400。
// handler 与 service 都调它：handler 拦的是用户传错，service 拦的是
// 非 HTTP 入口（后台脚本、命令行工具）直接调用，两边口径才不会漂。
func New(page, pageSize int) (Query, error) {
	if page < 1 {
		return Query{}, fmt.Errorf("%w: page must be >= 1", domain.ErrInvalidInput)
	}
	if pageSize < 1 || pageSize > MaxPageSize {
		return Query{}, fmt.Errorf("%w: page_size must be between 1 and %d", domain.ErrInvalidInput, MaxPageSize)
	}

	// 极大 page 会让 (page-1)*pageSize 溢出 int，变成负数 Offset 查出错乱的数据。
	// 乘之前先判上界：用 32 位整数上限除以每页条数，超过就拒绝。
	// 用 32 位而不是 int 的位宽，是因为 32 位构建上 int 就是 32 位，一份代码两处都能跑。
	if page-1 > (1<<31-1)/pageSize {
		return Query{}, fmt.Errorf("%w: page is too large", domain.ErrInvalidInput)
	}
	return Query{Page: page, PageSize: pageSize}, nil
}

// Offset 返回这一页要跳过的行数。
// 只由 New 产出的 Query 调用，所以这里不再判断 page 是否合法。
func (q Query) Offset() int {
	return (q.Page - 1) * q.PageSize
}

// 关于「为什么没有抽一个泛型的分页响应结构体」：
// 五个列表接口的 data 都有 list/total/page/page_size 四个字段，看上去很适合
// 定义成 Resp[T] 让各模块嵌入。实际做过一版，swag 解析匿名嵌入的泛型实例时
// 直接报 cannot find type definition，接口文档里那五个接口的响应结构会变成空。
// 文档是给人看的契约，比少写四个字段重要，所以响应结构体仍留在各模块的 dto.go。
// 这里只抽真正会被复制的判断逻辑：上下界、溢出、偏移量。
