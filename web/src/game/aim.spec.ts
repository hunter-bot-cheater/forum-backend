import { describe, expect, it } from 'vitest'
import {
  adjustPhi,
  aimFromCue,
  normalizePhi,
  phiFromScreenVector,
  rotateByDrag,
  screenDirectionOf,
} from './aim'
import { BALL_RADIUS, TABLE_LENGTH, TABLE_WIDTH } from './table'
import type { BallState } from './types'

// 瞄准是「点哪打哪」的基础，错一毫米到球桌上就是打空。
// 这些用例锁住四件事：几何求交的数值、落袋球不参与、角度归一化、屏幕方向换算。

// ball 造一颗还在台面上的球。
function ball(id: string, x: number, y: number, onTable = true): BallState {
  return { id, x, y, z: 0, on_table: onTable }
}

// 母球放在台面中心偏下一点，避开开球线与摆球区。
const cue = ball('cue', 0.4953, 0.6)

describe('phiFromScreenVector', () => {
  it('向右拖是 0 度', () => {
    expect(phiFromScreenVector(10, 0)).toBeCloseTo(0, 9)
  })

  it('向上拖是 90 度（屏幕向上等于台面远端）', () => {
    expect(phiFromScreenVector(0, -10)).toBeCloseTo(Math.PI / 2, 9)
  })

  it('向下拖是 270 度', () => {
    expect(phiFromScreenVector(0, 10)).toBeCloseTo((Math.PI * 3) / 2, 9)
  })

  it('向左拖是 180 度', () => {
    expect(phiFromScreenVector(-10, 0)).toBeCloseTo(Math.PI, 9)
  })

  it('原地没动时给 0，不返回 NaN', () => {
    expect(phiFromScreenVector(0, 0)).toBe(0)
  })
})

describe('normalizePhi / adjustPhi', () => {
  it('负角度收进 0 到 2π', () => {
    expect(normalizePhi(-Math.PI / 2)).toBeCloseTo((Math.PI * 3) / 2, 9)
  })

  it('超过一圈的角度折回来', () => {
    expect(normalizePhi(Math.PI * 2 + 0.5)).toBeCloseTo(0.5, 9)
  })

  it('微调 0.1 度就是 0.1 度', () => {
    const before = 1
    expect(adjustPhi(before, 0.1)).toBeCloseTo(before + (0.1 * Math.PI) / 180, 9)
  })

  it('接近 360 度时加 0.1 度会回绕到 0 附近', () => {
    const near = (Math.PI * 2 * 359.95) / 360
    const after = adjustPhi(near, 0.1)
    expect(after).toBeGreaterThanOrEqual(0)
    expect(after).toBeLessThan(0.01)
  })

  it('往反方向微调不会变成负数', () => {
    expect(adjustPhi(0.001, -0.1)).toBeGreaterThan(Math.PI * 2 - 0.01)
  })
})

describe('aimFromCue：撞库', () => {
  it('朝右打，停在右库内缩一个球半径的位置', () => {
    const hit = aimFromCue([cue], 0)
    expect(hit.kind).toBe('cushion')
    expect(hit.distance).toBeCloseTo(TABLE_WIDTH - BALL_RADIUS - cue.x, 9)
    expect(hit.x).toBeCloseTo(TABLE_WIDTH - BALL_RADIUS, 9)
    expect(hit.y).toBeCloseTo(cue.y, 9)
  })

  it('朝上打，停在远端库边', () => {
    const hit = aimFromCue([cue], Math.PI / 2)
    expect(hit.kind).toBe('cushion')
    expect(hit.distance).toBeCloseTo(TABLE_LENGTH - BALL_RADIUS - cue.y, 9)
  })

  it('朝下打，停在这一侧的库边', () => {
    const hit = aimFromCue([cue], -Math.PI / 2)
    expect(hit.kind).toBe('cushion')
    expect(hit.distance).toBeCloseTo(cue.y - BALL_RADIUS, 9)
  })

  it('斜向 45 度撞更近的那条库边', () => {
    const hit = aimFromCue([cue], Math.PI / 4)
    expect(hit.kind).toBe('cushion')
    expect(hit.distance).toBeCloseTo((TABLE_WIDTH - BALL_RADIUS - cue.x) / Math.cos(Math.PI / 4), 6)
  })

  it('母球贴库朝库边打，距离接近 0 而不是负数', () => {
    const hugging = ball('cue', TABLE_WIDTH - BALL_RADIUS, 1.0)
    const hit = aimFromCue([hugging], 0)
    expect(hit.distance).toBeGreaterThanOrEqual(0)
    expect(hit.distance).toBeLessThan(1e-6)
  })
})

describe('aimFromCue：撞球', () => {
  it('正前方有球时停在两球相切的位置', () => {
    const target = ball('3', cue.x + 0.3, cue.y)
    const hit = aimFromCue([cue, target], 0)
    expect(hit.kind).toBe('ball')
    expect(hit.targetId).toBe('3')
    expect(hit.distance).toBeCloseTo(0.3 - BALL_RADIUS * 2, 9)
    // 幽灵球圆心到目标球心的距离正好是两个球半径。
    expect(Math.hypot(hit.x - target.x, hit.y - target.y)).toBeCloseTo(BALL_RADIUS * 2, 9)
  })

  it('球在背后不会被瞄准到', () => {
    const behind = ball('3', cue.x - 0.3, cue.y)
    const hit = aimFromCue([cue, behind], 0)
    expect(hit.kind).toBe('cushion')
    expect(hit.targetId).toBeNull()
  })

  it('球偏离射线一个球径之外不会命中', () => {
    const offset = ball('3', cue.x + 0.3, cue.y + BALL_RADIUS * 2.2)
    const hit = aimFromCue([cue, offset], 0)
    expect(hit.kind).toBe('cushion')
  })

  it('两颗球都在射线上时取更近的那颗', () => {
    const near = ball('5', cue.x + 0.2, cue.y)
    const far = ball('6', cue.x + 0.6, cue.y)
    const hit = aimFromCue([cue, far, near], 0)
    expect(hit.targetId).toBe('5')
  })

  it('已经落袋的球不参与瞄准', () => {
    const dropped = ball('3', cue.x + 0.3, cue.y, false)
    const hit = aimFromCue([cue, dropped], 0)
    expect(hit.kind).toBe('cushion')
  })

  it('球在射线上但被库边挡在前头时以库边为准', () => {
    // 贴着右库的球，母球朝右打，先撞到的仍是球——它比库边近半个球径。
    const wallBall = ball('7', TABLE_WIDTH - BALL_RADIUS * 2 - 0.001, cue.y)
    const hit = aimFromCue([cue, wallBall], 0)
    expect(hit.kind).toBe('ball')
    expect(hit.targetId).toBe('7')
  })
})

describe('aimFromCue：异常输入', () => {
  it('球局里没有母球时返回零长度，不抛异常', () => {
    const hit = aimFromCue([ball('1', 0.4, 0.6)], 0)
    expect(hit.distance).toBe(0)
    expect(hit.kind).toBe('cushion')
  })

  it('母球已落袋时同样按「没法瞄准」处理', () => {
    const dropped = ball('cue', 0.4, 0.6, false)
    const hit = aimFromCue([dropped], 0)
    expect(hit.distance).toBe(0)
  })

  it('幽灵球一定在射线上', () => {
    const target = ball('9', cue.x + 0.25, cue.y + 0.25)
    const phi = Math.PI / 4
    const hit = aimFromCue([cue, target], phi)
    const dx = hit.x - cue.x
    const dy = hit.y - cue.y
    expect(Math.hypot(dx, dy)).toBeCloseTo(hit.distance, 9)
    expect(dy / (dx || 1)).toBeCloseTo(Math.tan(phi), 6)
  })
})

describe('rotateByDrag', () => {
  it('往右拖，角度减小（屏幕顺时针）', () => {
    const after = rotateByDrag(Math.PI / 2, 10)
    expect(after).toBeLessThan(Math.PI / 2)
  })

  it('拖动 360 像素大约转 90 度', () => {
    const before = Math.PI / 2
    const after = rotateByDrag(before, 360)
    expect(after).toBeCloseTo(before - Math.PI / 2, 6)
  })

  it('来回拖同样的距离回到原角度', () => {
    const before = 1.2
    const moved = rotateByDrag(before, 37)
    expect(rotateByDrag(moved, -37)).toBeCloseTo(before, 9)
  })

  it('竖直位移不参与旋转', () => {
    const before = 0.8
    expect(rotateByDrag(before, 0, 0.25)).toBeCloseTo(before, 9)
  })

  it('角度始终落在 0 到 2π', () => {
    let phi = 0
    for (let i = 0; i < 20; i += 1) {
      phi = rotateByDrag(phi, -50)
      expect(phi).toBeGreaterThanOrEqual(0)
      expect(phi).toBeLessThan(Math.PI * 2)
    }
  })
})

describe('screenDirectionOf', () => {
  it('phi 为 0 时指向屏幕右侧', () => {
    const d = screenDirectionOf(0)
    expect(d.x).toBeCloseTo(1, 9)
    expect(d.y).toBeCloseTo(0, 9)
  })

  it('phi 为 90 度时指向屏幕上方', () => {
    const d = screenDirectionOf(Math.PI / 2)
    expect(d.x).toBeCloseTo(0, 9)
    expect(d.y).toBeCloseTo(-1, 9)
  })

  it('与 phiFromScreenVector 互为逆运算', () => {
    for (const phi of [0.3, 1.1, 2.7, 5.9]) {
      const d = screenDirectionOf(phi)
      expect(phiFromScreenVector(d.x, d.y)).toBeCloseTo(phi, 9)
    }
  })
})
