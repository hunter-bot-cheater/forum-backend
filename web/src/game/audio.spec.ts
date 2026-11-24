// 音效模块的单元测试。
//
// 这里只测「翻译」这一半：一杆的事件流该在什么时刻、以多大声、从哪一侧响。
// 真正发声的那一半（振荡器、包络、滤波）在 jsdom 里根本没有 AudioContext，
// 用假的 context 断言调用顺序意义不大——那只能证明「我按我写的方式调用了」，
// 证明不了好不好听。那部分靠浏览器里试听。
import { describe, expect, it } from 'vitest'
import { buildShotSfx } from './audio'
import type { ShotFrame } from './animator'

// framesOf 造一杆的轨迹：母球 2 m/s 往 +x 走，目标球原地不动。
// 0.5 秒、每秒 60 帧，足够让 speedAt 取到「事件前 20 毫秒」的位置差。
function framesOf(): ShotFrame[] {
  const frames: ShotFrame[] = []
  for (let i = 0; i <= 30; i += 1) {
    const t = i / 60
    frames.push({
      t,
      balls: [
        ['cue', 0.3 + 2 * t, 0.99, 0],
        ['1', 0.6 + 1 * t, 0.99, 0],
      ],
    })
  }
  return frames
}

describe('一杆的音效表', () => {
  it('击球、撞球、撞库、落袋各是一条，顺序跟着事件走', () => {
    const cues = buildShotSfx(
      framesOf(),
      [
        { t: 0, type: 'stick_ball', a: 'cue', b: 'cue' },
        { t: 0.15, type: 'ball_ball', a: 'cue', b: '1' },
        { t: 0.4, type: 'cushion', a: '1', b: '' },
        { t: 0.45, type: 'ball_pocket', a: '1', b: 'p3' },
      ],
      0.7,
    )
    expect(cues.map((cue) => cue.kind)).toEqual(['strike', 'click', 'cushion', 'pocket'])
    expect(cues.map((cue) => cue.t)).toEqual([0, 0.15, 0.4, 0.45])
  })

  it('音量跟着撞击前的球速走，不是整杆一个音量', () => {
    const cues = buildShotSfx(
      framesOf(),
      [
        { t: 0, type: 'stick_ball', a: 'cue', b: 'cue' },
        { t: 0.15, type: 'ball_ball', a: 'cue', b: '1' },
        { t: 0.4, type: 'cushion', a: '1', b: '' },
      ],
      0.7,
    )
    // 击球音用的是力度：玩家拉了多少，杆头就多响。
    expect(cues[0].gain).toBeCloseTo(0.7, 6)
    // 撞球音用的是球速：2 m/s 与 1 m/s 之间，2 m/s 更响（满值按 3.2 折算）。
    expect(cues[1].gain).toBeGreaterThan(0.4)
    // 撞库的是慢下来的那颗球（1 m/s），比撞球轻。
    expect(cues[2].gain).toBeLessThan(cues[1].gain)
    expect(cues[2].gain).toBeGreaterThan(0)
  })

  it('方位跟着球的横向位置：左半台从左边响', () => {
    const cues = buildShotSfx(framesOf(), [{ t: 0.1, type: 'ball_ball', a: 'cue', b: '1' }], 1)
    // 母球在 x≈0.5（台长 2.24），明显偏左。
    expect(cues[0].pan).toBeLessThan(-0.2)
    expect(cues[0].pan).toBeGreaterThan(-0.75)
  })

  it('认不出的类型直接丢掉，不会当成撞球', () => {
    const cues = buildShotSfx(framesOf(), [{ t: 0.1, type: 'cue_magic', a: 'cue', b: '' }], 1)
    expect(cues).toHaveLength(0)
  })

  it('没有帧也没有事件时不会炸', () => {
    expect(buildShotSfx([], [], 1)).toEqual([])
  })
})
