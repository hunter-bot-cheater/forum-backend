import { describe, expect, it } from 'vitest'
import { sampleAt } from './animator'
import { fakeShot } from './fake'
import { BALL_RADIUS, RACK_EIGHTBALL, TABLE_LENGTH, TABLE_WIDTH } from './table'
import type { BallState } from './types'

// 假物理的用例只锁「行为不越界」，不锁数值：
// 它本来就不是真实物理，精确断言只会把占位实现固化成规格。
// M3 接上 pool-sim 后这个文件随 fake.ts 一起删。

// rack 造一份开球摆球的球状态。
function rack(): BallState[] {
  return RACK_EIGHTBALL.map((b) => ({ id: b.id, x: b.x, y: b.y, z: 0, on_table: true }))
}

// 母球在开球摆球里的坐标，后面多处用到。
const CUE = RACK_EIGHTBALL.filter((b) => b.id === 'cue')[0]

describe('fakeShot', () => {
  it('没有任何力度时几乎立刻结束', () => {
    const shot = fakeShot(rack(), 0, 0)
    expect(shot.duration).toBeLessThan(0.05)
  })

  it('满力开球会跑出多帧', () => {
    const shot = fakeShot(rack(), Math.PI / 2, 1)
    expect(shot.frames.length).toBeGreaterThan(60)
    expect(shot.duration).toBeGreaterThan(0.5)
  })

  it('模拟时长不会超过上限', () => {
    const shot = fakeShot(rack(), Math.PI / 2, 1)
    expect(shot.duration).toBeLessThanOrEqual(10.05)
  })

  it('首帧包含全部 16 颗球', () => {
    const shot = fakeShot(rack(), 0.3, 0.5)
    expect(shot.frames[0].balls).toHaveLength(16)
  })

  it('球数只会减少，不会凭空多出来', () => {
    const shot = fakeShot(rack(), Math.PI / 2, 1)
    let maxCount = 0
    for (const f of shot.frames) {
      maxCount = Math.max(maxCount, f.balls.length)
    }
    expect(maxCount).toBeLessThanOrEqual(16)
  })

  it('每一帧的球心都在台面范围内（含球半径）', () => {
    const shot = fakeShot(rack(), Math.PI / 3, 0.9)
    for (const f of shot.frames) {
      for (const [, x, y] of f.balls) {
        expect(x).toBeGreaterThanOrEqual(-0.001)
        expect(x).toBeLessThanOrEqual(TABLE_WIDTH + 0.001)
        expect(y).toBeGreaterThanOrEqual(-0.001)
        expect(y).toBeLessThanOrEqual(TABLE_LENGTH + 0.001)
      }
    }
  })

  it('开球后母球离开了原位', () => {
    const shot = fakeShot(rack(), Math.PI / 2, 0.8)
    const last = sampleAt(shot.frames, shot.duration)
    const cue = last.find((b) => b.id === 'cue')
    // 母球可能已经落袋，这时它不在采样里，也算「离开原位」。
    if (cue) {
      expect(Math.hypot(cue.x - CUE.x, cue.y - CUE.y)).toBeGreaterThan(0.1)
    }
  })

  it('时间戳严格递增，播放器才能二分', () => {
    const shot = fakeShot(rack(), 0.1, 0.6)
    for (let i = 1; i < shot.frames.length; i += 1) {
      expect(shot.frames[i].t).toBeGreaterThan(shot.frames[i - 1].t)
    }
  })

  it('朝右轻推，母球先往 +x 方向走', () => {
    const shot = fakeShot(rack(), 0, 0.3)
    // 只看前 0.1 秒：轻推也会撞右库再弹回来，拿终局位置判断方向是错的。
    const early = sampleAt(shot.frames, 0.1)
    const cue = early.find((b) => b.id === 'cue')
    expect(cue).toBeDefined()
    expect(cue!.x).toBeGreaterThan(CUE.x)
  })

  it('球号字符串原样保留，没有变成数字', () => {
    const shot = fakeShot(rack(), 0, 0.2)
    for (const [, ...rest] of shot.frames[0].balls) {
      expect(rest).toHaveLength(3)
    }
    expect(typeof shot.frames[0].balls[0][0]).toBe('string')
  })

  it('母球贴着库边也不会被推出去', () => {
    const balls = rack()
    const cue = balls.filter((b) => b.id === 'cue')[0]
    cue.x = TABLE_WIDTH - BALL_RADIUS
    cue.y = 1.0
    const shot = fakeShot(balls, 0, 0.5)
    for (const f of shot.frames) {
      for (const [, x] of f.balls) {
        expect(x).toBeLessThanOrEqual(TABLE_WIDTH + 0.001)
      }
    }
  })
})
