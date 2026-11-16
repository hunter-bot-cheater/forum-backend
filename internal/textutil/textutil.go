// Package textutil 收敛「长度上限」的判断，把单位写在调用点上。
//
// 这套代码里存在两类长度上限，单位不同，混用不会报错、只会静默算错：
//   - 列声明是 varchar(n) 的，MySQL 按字符计，一个汉字算 1 个；
//   - 列声明是 text 的，MySQL 按字节计，一个汉字在 utf8mb4 里占 3 个字节。
//
// Go 的 len(string) 取的是字节数。拿它去判 varchar 列，42 个汉字就会被
// 128 的上限判成超长，用户看到的是「标题太长」，实际标题还不到两行。
// 所以这里不提供裸的 len，只提供两个名字里带单位的函数：
// 调用点一眼就能看出该用字符还是字节，评审时也能直接对照列定义。
package textutil

import "unicode/utf8"

// RuneLen 返回字符数。等价于 len([]rune(s))，但不额外分配一个切片。
func RuneLen(s string) int {
	return utf8.RuneCountInString(s)
}

// TooLongRunes 判断字符数是否超过 max，用于 varchar(n) 这类按字符计的列。
//
// 边界是「超过」，等于 max 仍算合法：varchar(128) 能存下整整 128 个字符。
func TooLongRunes(s string, max int) bool {
	return utf8.RuneCountInString(s) > max
}

// TooLongBytes 判断字节数是否超过 max，用于 text 这类按字节计的列，
// 以及「防止把整个文件贴进来」的请求体上限。
//
// 为什么不统一按字符算：text 列的实际容量是 65535 字节，
// 50000 个汉字是 150000 字节，按字符放行会直接撞上 MySQL 的列上限，
// 插入时报 data too long，用户拿到的是 500 而不是「内容太长」。
func TooLongBytes(s string, max int) bool {
	return len(s) > max
}

// TruncateRunes 把字符串截到最多 max 个字符，超出部分丢掉，不补省略号。
//
// 按 rune 而不是字节：中文按字节截会切出半个字，发出去是乱码。
// 调用方是「发给大模型前压一下长度」——模型只看开头就能判断主题，
// 把几万字正文原样发过去既慢又费额度。
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if RuneLen(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
