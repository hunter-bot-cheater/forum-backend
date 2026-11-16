// 本文件是「短时间重复发布」的判重：给标题与正文算一个摘要，用它占位。
//
// 为什么不做 HTTP 层的幂等键：
// 用户的真实行为是「连点两次发布」和「刷新页面又发了一遍」，
// 前端没法保证每次都生成稳定的 request id；而内容摘要是从请求体算出来的，
// 天然对这两种情况都成立，也不需要前端配合。
package article

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// articleDupWindow 是判重窗口。
//
// 10 分钟覆盖「连点、刷新重发、返回再发一次」这几种手滑；
// 再长就会误伤真正想重发的人（比如作者改了一个错字又改回来）。
// 与 30 分钟的浏览去重不同：那个窗口是「一次阅读会话」，这个是「一次发布动作」。
const articleDupWindow = 10 * time.Minute

// contentDigest 计算标题与正文的内容摘要。
//
// 用 SHA-256 取前 16 个十六进制字符：
//   - 不做归一化（不去空格、不转大小写）：换了标点或空行就是另一篇内容，该允许发；
//   - 只用 64 位是因为它只用来判「10 分钟内同一个人发过没」，
//     碰撞概率在单个用户的十条内容里可以忽略，而 key 短一半、日志也好看。
//
// 中间用 \x00 分隔：否则 title="ab" content="c" 与 title="a" content="bc" 会撞同一个摘要。
func contentDigest(title, content string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + content))
	return hex.EncodeToString(sum[:8])
}
