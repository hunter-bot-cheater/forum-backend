import { describe, expect, it } from 'vitest'
import fixtureRaw from './__fixtures__/pooltool-shots.json?raw'
import { predictShot } from './predict'
import { BALL_RADIUS } from './table'
import type { BallState, ShotParams } from './types'

// 对照数据由 pool-sim 真实跑出来的轨迹（见测试末尾的生成方式说明）。
// 用真实物理而不是手工数字：预测模型的意义就是「贴住服务端」，
// 拿自己算的数当参考答案，等于什么都没验。
interface FixtureShot {
  name: string
  params: ShotParams
  balls: Array<[string, number, number]>
  frames: Array<{ t: number; balls: Array<[string, number, number, number]> }>
  duration: number
}

const fixture: { shots: FixtureShot[] } = JSON.parse(fixtureRaw)

// toBalls 把 fixture 的 [id, x, y] 转成对局页手里的球状态。
function toBalls(shot: FixtureShot): BallState[] {
  const result: BallState[] = []
  for (const [id, x, y] of shot.balls) {
    result.push({ id, x, y, z: 0, on_table: true })
  }
  return result
}

// positionAt 从一杆的帧里取某颗球在 t 时刻的位置，线性插值。
// 时间超出尾部时返回 null——那颗球可能已经落袋。
function positionAt(
  frames: Array<{ t: number; balls: Array<[string, number, number, number]> }>,
  id: string,
  t: number,
): { x: number; y: number } | null {
  let prev: { t: number; x: number; y: number } | null = null
  for (const frame of frames) {
    let found: [string, number, number, number] | null = null
    for (const item of frame.balls) {
      if (item[0] === id) {
        found = item
        break
      }
    }
    if (!found) {
      return null
    }
    if (frame.t >= t) {
      if (!prev || frame.t === prev.t) {
        return { x: found[1], y: found[2] }
      }
      const k = (t - prev.t) / (frame.t - prev.t)
      return { x: prev.x + (found[1] - prev.x) * k, y: prev.y + (found[2] - prev.y) * k }
    }
    prev = { t: frame.t, x: found[1], y: found[2] }
  }
  return prev
}

describe('本地预测', () => {
  it('开球前 0.15 秒：母球出发方向与 pooltool 一致', () => {
    const shot = fixture.shots.find((item) => item.name === 'break')!
    const predicted = predictShot(toBalls(shot), shot.params)!
    const start = positionAt(predicted.frames, 'cue', 0)!
    const truthStart = positionAt(shot.frames, 'cue', 0)!

    // 绝对距离不再和 pooltool 对得上了，这是刻意的：出射速度上限与摩擦系数
    // 都按手感调过（见 predict.ts 的 SPEED_MAX / ROLL_FRICTION），同一个力度
    // 现在走得更慢、停得更早。该对得上的是方向——它只由 phi 决定，
    // 偏一点就说明瞄准几何或者积分出了问题。
    for (const t of [0.05, 0.1, 0.15]) {
      const mine = positionAt(predicted.frames, 'cue', t)!
      const truth = positionAt(shot.frames, 'cue', t)!
      const mineAngle = Math.atan2(mine.y - start.y, mine.x - start.x)
      const truthAngle = Math.atan2(truth.y - truthStart.y, truth.x - truthStart.x)
      expect(Math.abs(mineAngle - truthAngle)).toBeLessThan(0.005)
    }
  })

  it('开球这一杆的时长明显短于 pooltool：球要停得下来', () => {
    const shot = fixture.shots.find((item) => item.name === 'break')!
    const predicted = predictShot(toBalls(shot), shot.params)!

    // 这一条盯的是「像在玻璃上打球」那个毛病：摩擦调小、速度上限调高都会
    // 让这一杆拖到四五秒，球一直在台上爬。区间取在 0.3~0.7 倍是给以后
    // 微调手感留的余地，超出就说明参数被改回了原来的量级。
    const ratio = predicted.duration / shot.duration
    expect(ratio).toBeGreaterThan(0.3)
    expect(ratio).toBeLessThan(0.7)
  })

  it('轻推：母球一路减速，最后一帧已经停下', () => {
    const shot = fixture.shots.find((item) => item.name === 'soft')!
    const predicted = predictShot(toBalls(shot), shot.params)!
    let previous: { x: number; y: number } | null = null
    let lastStep = 0

    for (const frame of predicted.frames) {
      const cue = frame.balls.find((item) => item[0] === 'cue')
      expect(cue).toBeDefined()
      if (!cue) {
        return
      }
      expect(Number.isFinite(cue[1])).toBe(true)
      expect(Number.isFinite(cue[2])).toBe(true)
      if (previous) {
        const step = Math.hypot(cue[1] - previous.x, cue[2] - previous.y)
        // 一帧 1/30 秒，出射速度上限 4.8 m/s，单帧位移不可能超过 0.25 米。
        // 超过就说明积分炸了（位置发散），画面上是球在乱飞。
        expect(step).toBeLessThan(0.25)
        lastStep = step
      }
      previous = { x: cue[1], y: cue[2] }
    }

    // 收尾是「全停才结束」，所以最后一帧的位移应该小到看不见。
    expect(lastStep).toBeLessThan(0.01)
  })

  it('预测的每一帧都不会出现两颗球叠在一起', () => {
    const shot = fixture.shots.find((item) => item.name === 'break')!
    const predicted = predictShot(toBalls(shot), shot.params)!

    // 玩家会把「画面上球穿在一起」直接当成碰撞体积算错，
    // 所以这条对预测同样成立：允许 1 毫米的数值误差，再多就是真叠了。
    for (const frame of predicted.frames) {
      const balls = frame.balls
      for (let i = 0; i < balls.length; i += 1) {
        for (let j = i + 1; j < balls.length; j += 1) {
          const distance = Math.hypot(balls[i][1] - balls[j][1], balls[i][2] - balls[j][2])
          expect(distance).toBeGreaterThan(BALL_RADIUS * 2 - 0.001)
        }
      }
    }
  })

  it('球全部停在台面内：不会有人滚到木框外面去', () => {
    const shot = fixture.shots.find((item) => item.name === 'soft')!
    const predicted = predictShot(toBalls(shot), shot.params)!
    const last = predicted.frames[predicted.frames.length - 1]
    for (const item of last.balls) {
      expect(item[1]).toBeGreaterThan(-0.08)
      expect(item[1]).toBeLessThan(1.07)
      expect(item[2]).toBeGreaterThan(-0.08)
      expect(item[2]).toBeLessThan(2.06)
    }
  })

  it('跳球不做预测：二维模型表达不了腾空', () => {
    const shot = fixture.shots.find((item) => item.name === 'soft')!
    const params: ShotParams = { ...shot.params, theta: 0.4 }
    expect(predictShot(toBalls(shot), params)).toBeNull()
  })
})
