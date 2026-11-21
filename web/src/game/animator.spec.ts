import { describe, expect, it } from 'vitest'
import { applySample, playShot, sampleAt, type Shot, type ShotFrame } from './animator'
import type { BallState } from './types'

// 插值是「看起来顺不顺」的关键，测的是边界与落袋两种容易写错的情况。
// playShot 依赖 rAF，只做「能开始、能结束、能停」的冒烟。

// frame 是构造关键帧的小工具，省得每条用例都写完整的元组。
function frame(t: number, balls: Array<[string, number, number]>): ShotFrame {
  return { t, balls: balls.map(([id, x, y]) => [id, x, y, 0]) }
}

const twoFrames: ShotFrame[] = [frame(0, [['cue', 0, 0], ['1', 1, 0]]), frame(1, [['cue', 0, 1], ['1', 1, 0]])]

describe('sampleAt', () => {
  it('没有帧时返回空，不抛异常', () => {
    expect(sampleAt([], 0)).toEqual([])
  })

  it('只有一帧时任何时刻都返回那一帧', () => {
    const single = [frame(0, [['cue', 0.2, 0.3]])]
    expect(sampleAt(single, 5)).toHaveLength(1)
    expect(sampleAt(single, 5)[0].x).toBeCloseTo(0.2, 9)
  })

  it('两帧中间按时间比例插值', () => {
    const at = sampleAt(twoFrames, 0.25)
    const cue = at.find((b) => b.id === 'cue')
    expect(cue).toBeDefined()
    expect(cue!.y).toBeCloseTo(0.25, 9)
  })

  it('早于首帧取首帧，晚于末帧取末帧', () => {
    expect(sampleAt(twoFrames, -1)[0].y).toBeCloseTo(0, 9)
    expect(sampleAt(twoFrames, 99)[0].y).toBeCloseTo(1, 9)
  })

  it('静止球在多帧里位置不变', () => {
    const at = sampleAt(twoFrames, 0.7)
    const one = at.find((b) => b.id === '1')
    expect(one).toBeDefined()
    expect(one!.x).toBeCloseTo(1, 9)
  })

  it('落袋的球在消失之后不再返回', () => {
    const frames: ShotFrame[] = [
      frame(0, [['cue', 0, 0], ['3', 0.5, 0.5]]),
      frame(0.5, [['cue', 0.1, 0], ['3', 0.5, 0.5]]),
      frame(1, [['cue', 0.2, 0]]),
    ]
    const late = sampleAt(frames, 0.9)
    expect(late.map((b) => b.id)).toEqual(['cue'])
  })

  it('落袋发生在区间内时，这一区间就按已经没它处理', () => {
    const frames: ShotFrame[] = [
      frame(0, [['cue', 0, 0], ['3', 0.5, 0.5]]),
      frame(1, [['cue', 1, 0]]),
    ]
    expect(sampleAt(frames, 0.5).map((b) => b.id)).toEqual(['cue'])
  })

  it('乱序时间也不会死循环（二分依赖有序，越界时走首尾分支）', () => {
    const frames: ShotFrame[] = [frame(0, [['cue', 0, 0]]), frame(2, [['cue', 0, 2]])]
    expect(() => sampleAt(frames, 1)).not.toThrow()
  })
})

describe('applySample', () => {
  // balls 造一份两球球局。
  function balls(): BallState[] {
    return [
      { id: 'cue', x: 0, y: 0, z: 0, on_table: true },
      { id: '1', x: 1, y: 0, z: 0, on_table: true },
    ]
  }

  it('把采样位置写回球对象', () => {
    const list = balls()
    applySample(list, [{ id: 'cue', x: 0.3, y: 0.4, z: 0 }])
    expect(list[0].x).toBeCloseTo(0.3, 9)
    expect(list[0].on_table).toBe(true)
  })

  it('没出现在采样里的球标记为落袋', () => {
    const list = balls()
    applySample(list, [{ id: 'cue', x: 0.3, y: 0.4, z: 0 }])
    expect(list[1].on_table).toBe(false)
  })

  it('采样里出现未知球号时跳过，不新增球', () => {
    const list = balls()
    applySample(list, [{ id: '99', x: 0.3, y: 0.4, z: 0 }])
    expect(list).toHaveLength(2)
    expect(list.every((b) => !b.on_table)).toBe(true)
  })

  it('返回原数组，方便链式调用', () => {
    const list = balls()
    expect(applySample(list, [])).toBe(list)
  })
})

describe('playShot', () => {
  const shot: Shot = { frames: twoFrames, duration: 1 }

  it('从末尾开始播会立刻结束', async () => {
    let done = false
    const handle = playShot(shot, {
      onSample: () => {},
      onDone: () => {
        done = true
      },
      startAt: 1,
    })
    await new Promise((r) => setTimeout(r, 80))
    expect(done).toBe(true)
    handle.stop()
  })

  it('中途 stop 之后不再回调', async () => {
    let count = 0
    const handle = playShot(shot, {
      onSample: () => {
        count += 1
      },
    })
    handle.stop()
    const afterStop = count
    await new Promise((r) => setTimeout(r, 80))
    expect(count).toBe(afterStop)
  })

  it('回调带的时间在 0 到 duration 之间', async () => {
    let last = -1
    const handle = playShot(shot, {
      onSample: (_s, elapsed) => {
        last = elapsed
      },
    })
    await new Promise((r) => setTimeout(r, 60))
    handle.stop()
    expect(last).toBeGreaterThanOrEqual(0)
    expect(last).toBeLessThanOrEqual(1.05)
  })
})
