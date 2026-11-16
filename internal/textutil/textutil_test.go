package textutil

import (
	"strings"
	"testing"
)

// TestRuneLen 验证字符数按 rune 算而不是按字节。
// 拿英文和中文各测一次：utf8mb4 下一个汉字 3 字节，
// 两者字节数差 3 倍，字符数相同，用来暴露「把 len 当字符数」的写法。
func TestRuneLen(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"空串", "", 0},
		{"纯英文", "hello", 5},
		{"纯中文", "你好世界", 4},
		{"中英混排", "hi 你好", 5},
		// emoji 在 utf8mb4 里占 4 字节，但仍然是 1 个字符。
		{"emoji", "👍", 1},
	}
	for _, tc := range cases {
		if got := RuneLen(tc.in); got != tc.want {
			t.Errorf("%s：RuneLen(%q) = %d，期望 %d", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestTooLongRunes 验证按字符判断的边界：等于上限合法，超过一个才拒绝。
// 这一条是「中文标题被误拒」的回归测试：
// 128 个汉字是 384 字节，按字节判会被拒绝，按字符判必须通过。
func TestTooLongRunes(t *testing.T) {
	atLimit := strings.Repeat("好", 128)
	if TooLongRunes(atLimit, 128) {
		t.Error("128 个汉字正好等于上限，不该判超长")
	}

	overLimit := strings.Repeat("好", 129)
	if !TooLongRunes(overLimit, 128) {
		t.Error("129 个汉字超过上限，应当判超长")
	}

	// 同样长度的英文是 128 字节，两种写法都该得到同一个结论。
	if TooLongRunes(strings.Repeat("a", 128), 128) {
		t.Error("128 个字母正好等于上限，不该判超长")
	}
}

// TestTooLongBytes 验证按字节判断的边界。
// 这个函数服务的是 text 列，列容量本身按字节计，
// 所以 3000 个汉字（9000 字节）在 10000 的上限下是合法的。
func TestTooLongBytes(t *testing.T) {
	// 3000 个汉字 = 9000 字节，未超 10000。
	if TooLongBytes(strings.Repeat("好", 3000), 10000) {
		t.Error("9000 字节未超过 10000，不该判超长")
	}
	// 4000 个汉字 = 12000 字节，超过 10000。
	if !TooLongBytes(strings.Repeat("好", 4000), 10000) {
		t.Error("12000 字节超过 10000，应当判超长")
	}
	if TooLongBytes(strings.Repeat("a", 10000), 10000) {
		t.Error("正好 10000 字节，不该判超长")
	}
}
