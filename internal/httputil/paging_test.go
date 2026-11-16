package httputil

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/domain"
)

// newCtx 造一个带查询串的 gin.Context。
// 参数解析只读 query，不需要起路由，直接用 httptest 包一个请求就够。
func newCtx(t *testing.T, rawQuery string) *gin.Context {
	t.Helper()

	// 解析失败时回的是 400 而不是 panic，但要断言具体错误值，
	// 所以这里只关心 context 本身。
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/x?"+rawQuery, nil)
	return c
}

// TestParsePageDefaults 验证不传参数时的默认值。
// 默认值写错的表现很隐蔽：前端不传 page_size 时列表只剩 1 条，看起来像数据丢了。
func TestParsePageDefaults(t *testing.T) {
	page, size, err := ParsePage(newCtx(t, ""))
	if err != nil {
		t.Fatalf("默认参数不该报错: %v", err)
	}
	if page != 1 || size != 10 {
		t.Errorf("page=%d size=%d，期望 1/10", page, size)
	}
}

// TestParsePageRejects 验证非法分页参数统一回 ErrInvalidInput。
//
// 只判错误不为 nil 不够：handler 靠 errors.Is 决定回 400 还是 500，
// 换成别的错误值就会变成 500，前端看到的是「服务器出错」。
func TestParsePageRejects(t *testing.T) {
	bad := []string{
		"page=0", "page=-1", "page=abc",
		"page_size=0", "page_size=51", "page_size=x",
		"page=999999999&page_size=50", // 乘完会溢出 int
	}
	for _, q := range bad {
		if _, _, err := ParsePage(newCtx(t, q)); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s：错误 = %v，期望 ErrInvalidInput", q, err)
		}
	}
}

// TestParseCursorDefaults 验证游标参数缺省值：before_id=0、limit=20。
// before_id=0 的语义是「最新一页」，前端第一次进聊天页就不传这个参数。
func TestParseCursorDefaults(t *testing.T) {
	before, limit, err := ParseCursor(newCtx(t, ""))
	if err != nil {
		t.Fatalf("默认参数不该报错: %v", err)
	}
	if before != 0 || limit != DefaultCursorLimit {
		t.Errorf("before_id=%d limit=%d，期望 0/%d", before, limit, DefaultCursorLimit)
	}
}

// TestParseCursorValues 验证正常取值。
func TestParseCursorValues(t *testing.T) {
	before, limit, err := ParseCursor(newCtx(t, "before_id=42&limit=50"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if before != 42 || limit != 50 {
		t.Errorf("before_id=%d limit=%d，期望 42/50", before, limit)
	}
}

// TestParseCursorRejects 验证游标参数的非法输入。
//
// before_id 用 ParseUint 而不是 Atoi：负数必须直接拒绝。
// 不拒绝的话 uint(-1) 会变成一个极大的数，查询照样能跑，
// 只是默默返回空列表——用户会以为历史消息被删了。
func TestParseCursorRejects(t *testing.T) {
	bad := []string{
		"before_id=-1", "before_id=abc", "before_id=1.5",
		"limit=0", "limit=51", "limit=x", "limit=-3",
	}
	for _, q := range bad {
		if _, _, err := ParseCursor(newCtx(t, q)); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s：错误 = %v，期望 ErrInvalidInput", q, err)
		}
	}
}
