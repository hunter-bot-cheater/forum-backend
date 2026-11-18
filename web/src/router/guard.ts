// 路由守卫的判断规则。
//
// 单独成文件而不是写在 index.ts 里：那份文件在模块求值时就 createRouter、
// 引入 Layout 与一堆页面组件，单测为了验证几条跳转规则去加载整棵组件树不值得。
// 这里只有纯逻辑，输入是目标路由与 token。

// 已登录时不该再停留的两页。
//
// 这里只列登录与注册。判断条件不能写成「已登录访问任何公开页就回列表」：
// 文章详情、热榜、用户主页都是公开页，那样写会把登录用户点开的每一篇文章
// 都弹回列表，等于登录之后反而看不了内容。
const AUTH_PAGES = ['Login', 'Register']

// GuardTarget 是一次导航的结论。true 是放行，字符串或对象是改道。
export type GuardTarget = true | string | { path: string; query: { redirect: string } }

// guardTarget 是路由守卫的全部判断，输入只有目标路由和当前 token。
//
// to 的类型收窄到实际用到的三个字段，测试里构造对象不必凑齐整个 RouteLocation；
// 真正的守卫把 to 直接传进来，RouteLocationNormalized 结构上兼容这三个字段。
export function guardTarget(
  to: { name?: unknown; meta: { public?: boolean }; fullPath: string },
  token: string,
): GuardTarget {
  const isPublic = to.meta.public === true

  if (!token) {
    // 带上 fullPath 当 redirect，登录成功后能回到用户本来想去的地方。
    return isPublic ? true : { path: '/login', query: { redirect: to.fullPath } }
  }

  // name 的静态类型是 string | symbol | undefined，统一成字符串再比。
  if (AUTH_PAGES.includes(String(to.name))) {
    return '/articles'
  }
  return true
}
