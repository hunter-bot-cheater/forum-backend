import { beforeEach, describe, expect, it, vi } from 'vitest'
import { goBack } from '@/utils/nav'

// 返回按钮的用例。这里锁的是「怎么选回哪」这一层：
//   - 有上一页就真回上一页（保留列表分页、滚动位置）
//   - 上一页是「互跳的黑名单页」就不回去，直接落到兜底页
//   - 没有上一页（新标签直接打开）也不要把人踢出站点
//
// 为什么值得测：个人主页和编辑资料曾经各自 push 对方，
// 两页的「返回」互相指向对面，每点一次往历史里压一条，看起来就是死循环。
// 这条规则一旦被改回「固定 push 对方」，坑立刻复现。

const router = {
  back: vi.fn(),
  replace: vi.fn(),
}

beforeEach(() => {
  router.back.mockReset()
  router.replace.mockReset()
  window.history.replaceState({}, '')
})

// setHistoryBack 模拟 vue-router 记录的上一页地址。
// 它存在 history.state.back 里，是 fullPath（可能带 query）。
function setHistoryBack(fullPath: string | null) {
  window.history.replaceState({ back: fullPath }, '')
}

describe('goBack', () => {
  it('有上一页时真的回上一页', () => {
    setHistoryBack('/articles?page=3')

    goBack(router as never, '/articles')

    expect(router.back).toHaveBeenCalledTimes(1)
    expect(router.replace).not.toHaveBeenCalled()
  })

  it('上一页在黑名单里就跳过它，落到兜底页', () => {
    // 从编辑资料点「返回」时会看到这种历史：上一条正是互跳的那一页。
    setHistoryBack('/settings/profile')

    goBack(router as never, '/articles', ['/settings'])

    expect(router.back).not.toHaveBeenCalled()
    expect(router.replace).toHaveBeenCalledWith('/articles')
  })

  it('黑名单按路径段匹配，前缀相同的其它地址不算命中', () => {
    setHistoryBack('/settingsabc')

    goBack(router as never, '/articles', ['/settings'])

    expect(router.back).toHaveBeenCalledTimes(1)
  })

  it('没有上一页时用 replace 兜底，不往历史里再压一条', () => {
    setHistoryBack(null)

    goBack(router as never, '/articles')

    expect(router.back).not.toHaveBeenCalled()
    expect(router.replace).toHaveBeenCalledWith('/articles')
  })
})
