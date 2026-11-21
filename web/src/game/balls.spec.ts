import { describe, expect, it } from 'vitest'
import { ballLookOf } from './balls'

// 球的外观是纯查表，不碰 canvas，所以可以在 node 环境里直接断言。
// 号码贴图本身（paintBall）依赖 2d 上下文，交给真实浏览器验收。

describe('ballLookOf', () => {
  it('母球是白球且不画号码', () => {
    const look = ballLookOf('cue')
    expect(look.number).toBe(0)
    expect(look.striped).toBe(false)
  })

  it('8 号球是黑球，号码为 8', () => {
    const look = ballLookOf('8')
    expect(look.color).toBe('#22262e')
    expect(look.number).toBe(8)
    expect(look.striped).toBe(false)
  })

  it('1 到 7 号是全色球', () => {
    for (let n = 1; n <= 7; n += 1) {
      const look = ballLookOf(String(n))
      expect(look.number).toBe(n)
      expect(look.striped).toBe(false)
    }
  })

  it('9 到 15 号是花色球，颜色与本色的 1 到 7 号相同', () => {
    for (let n = 9; n <= 15; n += 1) {
      const look = ballLookOf(String(n))
      const solid = ballLookOf(String(n - 8))
      expect(look.number).toBe(n)
      expect(look.striped).toBe(true)
      expect(look.color).toBe(solid.color)
    }
  })

  it('七个全色球颜色互不相同', () => {
    const colors = new Set<string>()
    for (let n = 1; n <= 7; n += 1) {
      colors.add(ballLookOf(String(n)).color)
    }
    expect(colors.size).toBe(7)
  })

  it('非法球号退回母球外观，不抛异常', () => {
    for (const id of ['', '0', '16', 'abc', '1.5']) {
      const look = ballLookOf(id)
      expect(look.number).toBe(0)
      expect(look.striped).toBe(false)
    }
  })
})
