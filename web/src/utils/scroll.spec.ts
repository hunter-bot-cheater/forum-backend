import { beforeEach, describe, expect, it, vi } from 'vitest'
import { keepInPlace } from '@/utils/scroll'

// 翻页时把分页器钉在原位。锁的是「差值方向」和「没有元素时不炸」这两件事：
// 方向写反的话，本来只是轻微一抖会变成整屏乱跳，比不做还糟。

// fakeEl 按调用次数返回预设的 top 值：
// 第一次是翻页前（构造 restore 时），第二次是渲染后再测。
function fakeEl(tops: number[]) {
  let calls = 0
  return {
    isConnected: true,
    getBoundingClientRect: () => {
      const top = tops[Math.min(calls, tops.length - 1)]
      calls += 1
      return { top } as DOMRect
    },
  } as unknown as Element
}

beforeEach(() => {
  vi.restoreAllMocks()
})

describe('keepInPlace', () => {
  it('元素被内容顶下去时，把页面往下滚同样的距离', () => {
    const scrollBy = vi.spyOn(window, 'scrollBy').mockImplementation(() => {})
    // 翻页后列表变长，分页器从 700 掉到 900。
    const el = fakeEl([700, 900])

    const restore = keepInPlace(el)
    restore()

    expect(scrollBy).toHaveBeenCalledTimes(1)
    expect(scrollBy).toHaveBeenCalledWith(0, 200)
  })

  it('元素位置没变时不要动滚动（否则每次翻页都会轻微抖一下）', () => {
    const scrollBy = vi.spyOn(window, 'scrollBy').mockImplementation(() => {})
    const el = fakeEl([700, 700])

    keepInPlace(el)()

    expect(scrollBy).not.toHaveBeenCalled()
  })

  it('分页器已被 v-if 摘掉时安静退出', () => {
    const scrollBy = vi.spyOn(window, 'scrollBy').mockImplementation(() => {})
    const el = fakeEl([700, 900])
    ;(el as unknown as { isConnected: boolean }).isConnected = false

    keepInPlace(el)()

    expect(scrollBy).not.toHaveBeenCalled()
  })

  it('没有元素时返回一个空函数，调用不报错', () => {
    expect(() => keepInPlace(null)()).not.toThrow()
  })
})
