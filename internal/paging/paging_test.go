package paging

import (
	"errors"
	"testing"

	"forum-backend/internal/domain"
)

// TestNewValid 验证合法参数原样返回，并顺带守住默认值常量。
// DefaultPageSize 被改错时，所有不传 page_size 的列表都会变短或变长，
// 表现是「翻页条数不对」，很难联想到一个常量身上。
func TestNewValid(t *testing.T) {
	q, err := New(1, DefaultPageSize)
	if err != nil {
		t.Fatalf("合法参数不该报错: %v", err)
	}
	if q.Page != 1 || q.PageSize != DefaultPageSize {
		t.Errorf("page=%d size=%d，期望 1/%d", q.Page, q.PageSize, DefaultPageSize)
	}

	// 上界本身必须能过：限定 1~50 时，50 是合法值而不是越界。
	q, err = New(3, MaxPageSize)
	if err != nil {
		t.Fatalf("page_size 取上界不该报错: %v", err)
	}
	if q.PageSize != MaxPageSize {
		t.Errorf("size=%d，期望 %d", q.PageSize, MaxPageSize)
	}
}

// TestNewRejects 验证非法参数统一回 ErrInvalidInput。
//
// 只判 err != nil 不够：调用方靠 errors.Is(err, domain.ErrInvalidInput)
// 决定回 400，换成别的错误值就会变成 500，前端看到的是「服务器出错」。
func TestNewRejects(t *testing.T) {
	cases := []struct {
		name     string
		page     int
		pageSize int
	}{
		{"页码为 0", 0, 10},
		{"页码为负", -1, 10},
		{"每页条数为 0", 1, 0},
		{"每页条数为负", 1, -10},
		{"每页条数超上界", 1, MaxPageSize + 1},
		// 乘完会溢出 int：这一条如果漏判，(page-1)*pageSize 变成负数，
		// GORM 把 Offset(-n) 当成取消条件，页越界时静默返回第一页。
		// 1<<30 乘 50 约 5.4e10，已经超过 64 位 int 之外的常规范围，
		// 也必然超过代码里用 32 位上限做的保护线。
		{"偏移量溢出", 1 << 30, MaxPageSize},
	}
	for _, tc := range cases {
		if _, err := New(tc.page, tc.pageSize); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s（page=%d size=%d）：错误 = %v，期望 ErrInvalidInput",
				tc.name, tc.page, tc.pageSize, err)
		}
	}
}

// TestOffset 验证偏移量按 (page-1)*pageSize 计算。
// 第一页必须是 0：算成 page*pageSize 会让每个列表都从第二页开始。
func TestOffset(t *testing.T) {
	cases := []struct {
		page     int
		pageSize int
		want     int
	}{
		{1, 10, 0},
		{2, 10, 10},
		{3, 20, 40},
		{1, 50, 0},
	}
	for _, tc := range cases {
		q, err := New(tc.page, tc.pageSize)
		if err != nil {
			t.Fatalf("page=%d size=%d 不该报错: %v", tc.page, tc.pageSize, err)
		}
		if got := q.Offset(); got != tc.want {
			t.Errorf("page=%d size=%d：Offset=%d，期望 %d", tc.page, tc.pageSize, got, tc.want)
		}
	}
}
