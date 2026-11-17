import type { Router } from 'vue-router'

// goBack 统一的「返回上一层」。
//
// 直接用 router.back() 有两个坑：
//  1. 新标签页里直接打开这个地址时历史里没有上一页，点了没反应；
//  2. 两个页面互相提供返回入口时（个人主页 ⇄ 编辑资料），「返回」如果各自 push 对方，
//     每点一次就往历史里压一条新记录，两页会一直弹来弹去，用户看到的就是死循环。
//
// 所以先看 vue-router 记在 history.state.back 里的上一条地址：
// 命中 blocked（互跳的那几页）或者根本没有上一页，就 replace 到 fallback。
// 用 replace 而不是 push：返回本身不该再往历史里加记录，
// 否则按浏览器后退还是绕回同一圈。
export function goBack(router: Router, fallback: string, blocked: string[] = []) {
  const state = window.history.state as { back?: unknown } | null
  const previous = typeof state?.back === 'string' ? state.back : ''
  if (previous !== '' && !isBlocked(previous, blocked)) {
    router.back()
    return
  }
  void router.replace(fallback)
}

// isBlocked 判断上一条地址是不是「不能回去的那几页」。
// history.state.back 存的是 fullPath，可能带 query 或 hash，所以按 path 段比较，
// 但要求边界对齐：前缀 /settings 不能把 /settingsabc 也算进去。
function isBlocked(previous: string, blocked: string[]) {
  for (const prefix of blocked) {
    if (previous === prefix) {
      return true
    }
    for (const sep of ['?', '#', '/']) {
      if (previous.startsWith(prefix + sep)) {
        return true
      }
    }
  }
  return false
}
