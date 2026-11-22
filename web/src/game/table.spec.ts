import { describe, expect, it } from 'vitest'
import {
  BALL_RADIUS,
  CORNER_JAW_BEND,
  CORNER_MOUTH,
  CUSHION_WIDTH,
  POCKET_MOUTHS,
  POCKETS,
  RACK_EIGHTBALL,
  SIDE_JAW_BEND,
  SIDE_MOUTH,
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

  it('中袋开口比角袋宽，洞口两者一样大', () => {
    const corner = POCKETS.find((p) => p.id === 'lb')
    const side = POCKETS.find((p) => p.id === 'lc')
    expect(corner).toBeDefined()
    expect(side).toBeDefined()
    expect(SIDE_MOUTH).toBeGreaterThan(CORNER_MOUTH)
    expect(side!.radius).toBeCloseTo(corner!.radius, 9)
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

  it('尺寸按参考图的比例：洞 2.17 球径，角袋 2.08、中袋 2.25', () => {
    const d = BALL_RADIUS * 2
    expect((POCKETS[0].radius * 2) / d).toBeCloseTo(2.17, 1)
    expect(CORNER_MOUTH / d).toBeCloseTo(2.08, 1)
    expect(SIDE_MOUTH / d).toBeCloseTo(2.25, 1)
  })
})

describe('袋口斜边', () => {
  // 角袋与中袋的斜边不是一类东西，规则直接来自参考图的观察：
  //   角袋：两条斜边各朝洞口偏 40°（袋口角 140°），因此只差 10°、几乎平行；
  //   中袋：两条斜边各偏 14°（袋口角 104°），收口更平、更宽。
  // 用几何断言锁住，免得以后调数值时又把两类调成一种。
  function dirOf(a: { x: number; y: number }, b: { x: number; y: number }) {
    return { dx: b.x - a.x, dy: b.y - a.y }
  }

  // 两条直线的夹角，只关心 0~90°。
  function angleBetween(a: { dx: number; dy: number }, b: { dx: number; dy: number }) {
    const cos =
      (a.dx * b.dx + a.dy * b.dy) /
      (Math.hypot(a.dx, a.dy) * Math.hypot(b.dx, b.dy))
    return (Math.acos(Math.min(1, Math.max(-1, cos))) * 180) / Math.PI
  }

  it('角袋两条斜边各朝洞口偏 40 度，彼此只差 10 度', () => {
    for (let i = 0; i < POCKETS.length; i += 1) {
      const p = POCKETS[i]
      if (p.kind !== 'corner') {
        continue
      }
      const m = POCKET_MOUTHS[i]
      const a = dirOf(m.noseA, m.jawA)
      const b = dirOf(m.noseB, m.jawB)
      // 颊板 A 长在下库里（库边脸沿 x），颊板 B 长在侧库里（库边脸沿 y），
      // 所以「与库边脸成多少度」对 B 要拿 |dx| 比 |dy|。
      expect((Math.atan2(Math.abs(a.dy), Math.abs(a.dx)) * 180) / Math.PI).toBeCloseTo(
        CORNER_JAW_BEND,
        6,
      )
      expect((Math.atan2(Math.abs(b.dx), Math.abs(b.dy)) * 180) / Math.PI).toBeCloseTo(
        CORNER_JAW_BEND,
        6,
      )
      expect(angleBetween(a, b)).toBeCloseTo(90 - CORNER_JAW_BEND * 2, 6)
    }
  })

  it('角袋开口宽度就是两个鼻尖之间的距离', () => {
    for (let i = 0; i < POCKETS.length; i += 1) {
      if (POCKETS[i].kind !== 'corner') {
        continue
      }
      const m = POCKET_MOUTHS[i]
      expect(Math.hypot(m.noseB.x - m.noseA.x, m.noseB.y - m.noseA.y)).toBeCloseTo(
        CORNER_MOUTH,
        6,
      )
    }
  })

  it('中袋斜边偏 14 度，梯形长底边朝台面内部', () => {
    for (let i = 0; i < POCKETS.length; i += 1) {
      const p = POCKETS[i]
      if (p.kind !== 'side') {
        continue
      }
      const m = POCKET_MOUTHS[i]
      const mouth = Math.hypot(m.noseB.x - m.noseA.x, m.noseB.y - m.noseA.y)
      const back = Math.hypot(m.jawB.x - m.jawA.x, m.jawB.y - m.jawA.y)
      const a = dirOf(m.noseA, m.jawA)
      const b = dirOf(m.noseB, m.jawB)
      expect(mouth).toBeCloseTo(SIDE_MOUTH, 6)
      // 长底边朝台内 = 库边脸上的开口比库边外沿的收口宽。
      expect(back).toBeLessThan(mouth)
      expect(angleBetween(a, b)).toBeGreaterThan(0)
      // 斜边离开库边法线的角度就是 SIDE_JAW_BEND。
      expect((Math.atan2(Math.abs(b.dy), Math.abs(b.dx)) * 180) / Math.PI).toBeCloseTo(
        SIDE_JAW_BEND,
        6,
      )
      const faceX = p.x < 0 ? 0 : TABLE_WIDTH
      expect(m.noseA.x).toBeCloseTo(faceX, 6)
    }
  })

  it('洞是整圆，中袋有一半压在台呢上', () => {
    for (const p of POCKETS) {
      if (p.kind !== 'side') {
        continue
      }
      const faceX = p.x < 0 ? 0 : TABLE_WIDTH
      expect(Math.abs(p.x - faceX)).toBeLessThan(p.radius)
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
