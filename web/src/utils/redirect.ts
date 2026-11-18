// 登录后的回跳地址处理。
//
// 路由守卫把来处写在 ?redirect= 里，登录页读出来决定往哪跳。
// 这个值来自地址栏，任何人都能改，所以必须过一遍白名单式校验。

// 默认落点。登录页与注册页在拿不到合法来处时都回文章列表。
export const DEFAULT_REDIRECT = '/articles'

// safeRedirect 把 ?redirect= 的原始值变成一个可以放心跳的站内路径。
//
// 只接受以单个斜杠开头的路径。这几类值会被打回默认落点：
//   - 空值、数组（重复参数时 vue-router 给的是数组）
//   - https://evil.example 这种绝对地址
//   - //evil.example 这种协议相对地址，浏览器会当成外站
//   - /\\evil.example，部分浏览器把反斜杠当斜杠处理，同样能跳出站
//
// 宁可丢一次来处，也不能把刚登录的用户送出站。
export function safeRedirect(raw: unknown, fallback: string = DEFAULT_REDIRECT): string {
  if (typeof raw !== 'string') {
    return fallback
  }
  if (!raw.startsWith('/') || raw.startsWith('//') || raw.startsWith('/\\')) {
    return fallback
  }
  return raw
}
