// token 持久化到 localStorage，刷新页面不丢登录态。
const TOKEN_KEY = 'forum_token'

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || ''
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token)
}

export function removeToken(): void {
  localStorage.removeItem(TOKEN_KEY)
}

// 登出时要一起清理的回调，由各个 store 在创建时注册自己。
//
// 为什么不在这里直接 import 那些 store：request.ts 要 import 本模块，
// store 又要 import request.ts，再加一条 store → auth → store 的边就成了循环依赖。
// 循环依赖在 ESM 里不一定报错，而是在某个模块初始化时拿到 undefined，
// 表现是随机的「Cannot read properties of undefined」，很难定位。
// 用回调注册表把方向反过来：store 认识 auth，auth 不认识任何具体的 store。
const logoutHooks: Array<() => void> = []

// onLogout 注册一个清理函数。只在 store 创建时调用一次。
// 重复注册会让同一个 reset 跑多遍——虽然幂等，但会把「谁注册了什么」搅乱。
export function onLogout(hook: () => void): void {
  logoutHooks.push(hook)
}

// runLogoutHooks 依次执行所有清理函数。
// 用普通 for 而不是 forEach：某个 hook 抛错时希望立刻停下，
// 让错误带着调用栈抛出去，而不是被 forEach 吞掉继续往下跑。
export function runLogoutHooks(): void {
  for (let i = 0; i < logoutHooks.length; i++) {
    logoutHooks[i]()
  }
}
