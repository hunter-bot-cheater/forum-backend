import { describe, expect, it } from 'vitest'
import {
  BALL_RADIUS,
  CUSHION_WIDTH,
  POCKETS,
  RACK_EIGHTBALL,
  TABLE_LENGTH,
  TABLE_WIDTH,
  remainingBallsOfGroup,
} from './table'

// 台面常量是从 pooltool 导出的，这些用例锁住三件事：
// 数据没被误改、袋口数量与开口大小对得上、摆球确实落在台面内。
// 它们不测渲染，只测「几何本身自洽」。

describe('台面尺寸', () => {
  it('七尺台是 990.6 × 1981.2 毫米', () => {
    expect(TABLE_WIDTH).toBeCloseTo(0.9906, 6)
    expect(TABLE_LENGTH).toBeCloseTo(1.9812, 6)
  })

  it('长宽比接近 2:1，竖屏渲染时不会被拉成正方形', () => {
    const ratio = TABLE_LENGTH / TABLE_WIDTH
    expect(ratio).toBeGreaterThan(1.9)
    expect(ratio).toBeLessThan(2.1)
  })

  it('球半径小于库边厚度，球贴库时不会越过库边外沿', () => {
    expect(BALL_RADIUS).toBeLessThan(CUSHION_WIDTH)
  })
})

describe('袋口', () => {
  it('六个袋口，四个角袋两个中袋', () => {
    expect(POCKETS).toHaveLength(6)
    expect(POCKETS.filter((p) => p.kind === 'corner')).toHaveLength(4)
    expect(POCKETS.filter((p) => p.kind === 'side')).toHaveLength(2)
  })

  it('中袋开口比角袋略大', () => {
    const corner = POCKETS.find((p) => p.id === 'lb')
    const side = POCKETS.find((p) => p.id === 'lc')
    expect(corner).toBeDefined()
    expect(side).toBeDefined()
    expect(side!.radius).toBeGreaterThan(corner!.radius)
  })

  it('中袋贴在左右库边的中点', () => {
    const mids = POCKETS.filter((p) => p.kind === 'side')
    for (const p of mids) {
      expect(p.y).toBeCloseTo(TABLE_LENGTH / 2, 6)
    }
  })

  it('袋口开口大于球直径，球才能落进去', () => {
    for (const p of POCKETS) {
      expect(p.radius * 2).toBeGreaterThan(BALL_RADIUS * 2)
    }
  })
})

describe('八球摆球', () => {
  it('16 颗球，球号不重复', () => {
    expect(RACK_EIGHTBALL).toHaveLength(16)
    const ids = new Set(RACK_EIGHTBALL.map((b) => b.id))
    expect(ids.size).toBe(16)
  })

  it('含母球与 1 到 15 号', () => {
    const ids = RACK_EIGHTBALL.map((b) => b.id)
    expect(ids).toContain('cue')
    for (let n = 1; n <= 15; n += 1) {
      expect(ids).toContain(String(n))
    }
  })

  it('所有球心都在台面内，且离库边至少一个球半径', () => {
    for (const b of RACK_EIGHTBALL) {
      expect(b.x).toBeGreaterThan(BALL_RADIUS)
      expect(b.x).toBeLessThan(TABLE_WIDTH - BALL_RADIUS)
      expect(b.y).toBeGreaterThan(BALL_RADIUS)
      expect(b.y).toBeLessThan(TABLE_LENGTH - BALL_RADIUS)
    }
  })

  it('球与球不重叠', () => {
    const minGap = BALL_RADIUS * 2 - 0.0005
    for (let i = 0; i < RACK_EIGHTBALL.length; i += 1) {
      for (let j = i + 1; j < RACK_EIGHTBALL.length; j += 1) {
        const a = RACK_EIGHTBALL[i]
        const b = RACK_EIGHTBALL[j]
        expect(Math.hypot(a.x - b.x, a.y - b.y)).toBeGreaterThanOrEqual(minGap)
      }
    }
  })

  it('8 号球在三角框中心，母球在开球线之后', () => {
    const eight = RACK_EIGHTBALL.find((b) => b.id === '8')
    const cue = RACK_EIGHTBALL.find((b) => b.id === 'cue')
    expect(eight).toBeDefined()
    expect(cue).toBeDefined()
    // 8 号在第三排正中，比三角中心略靠远端。
    expect(eight!.x).toBeCloseTo(0.4953, 3)
    expect(eight!.y).toBeGreaterThan(1.55)
    expect(eight!.y).toBeLessThan(1.62)
    // 开球线在世界 y = 1/4 长处，母球必须在线后（更靠近开球端）。
    expect(cue!.y).toBeLessThan(TABLE_LENGTH * 0.25)
  })

  it('球堆整个落在台面远端那半边', () => {
    for (const ball of RACK_EIGHTBALL) {
      if (ball.id === 'cue') {
        continue
      }
      expect(ball.y).toBeGreaterThan(TABLE_LENGTH * 0.7)
    }
  })
})

describe('remainingBallsOfGroup', () => {
  it('未定组时两边都不该有球', () => {
    expect(remainingBallsOfGroup(['1', '2', '9'], undefined)).toEqual([])
  })

  it('全色组只取 1 到 7 号', () => {
    const ids = ['1', '2', '7', '8', '9', '12', 'cue']
    expect(remainingBallsOfGroup(ids, 'solids')).toEqual(['1', '2', '7'])
  })

  it('花色组只取 9 到 15 号', () => {
    const ids = ['1', '7', '9', '12', '15', '8']
    expect(remainingBallsOfGroup(ids, 'stripes')).toEqual(['9', '12', '15'])
  })

  it('8 号球不属于任何一组', () => {
    expect(remainingBallsOfGroup(['8'], 'solids')).toEqual([])
    expect(remainingBallsOfGroup(['8'], 'stripes')).toEqual([])
  })

  it('非法球号被跳过，不会算进任何一组', () => {
    const ids = ['0', '16', 'cue', 'abc', '3']
    expect(remainingBallsOfGroup(ids, 'solids')).toEqual(['3'])
  })
})
