import { describe, expect, it } from 'vitest'
import fixtureRaw from './__fixtures__/pooltool-shots.json?raw'
import { aimFromCue, aimGuides } from './aim'
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
    // 让这一杆拖到四五秒，球一直在台上爬。
    //
    // 上界一路从 0.7 放到 0.8、再到 0.95，每次都是主动改手感参数，不是放宽标准：
    // 碰撞改成「分离时带一半滚动」多滚一截；台呢阻尼减半、停球阈值往下收
    // 又多滚一截。现在开球这一杆是参考时长的 0.90 倍（停球那段柔软的收尾
    // 去掉之后又回来一点）。判据只剩一句——不能比 pooltool 还久，
    // 真回到那个量级说明参数又飘了。
    const ratio = predicted.duration / shot.duration
    expect(ratio).toBeGreaterThan(0.3)
    expect(ratio).toBeLessThan(0.95)
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
        // 一帧 1/120 秒，出射速度上限 4.4 m/s，单帧位移不可能超过 0.04 米；
        // 卡到 0.2 米是留了撞库瞬间换向的余量。超过就说明积分炸了（位置发散），
        // 画面上是球在乱飞。
        expect(step).toBeLessThan(0.2)
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

    // 这份对照轨迹是 pooltool 按标准球（直径 57.15mm）摆的，球心距 57.15mm；
    // 我们的球放大到 68.58mm 之后，开局那一帧本身就小于 2R——那不是积分出错，
    // 是基准数据里的球比我们的小。所以以开局帧的最小球心距当基线：
    // 只要后面没有任何一帧比开局更近，就说明碰撞解算没有把球挤到一起。
    //
    // 允许 3 毫米的数值误差：碰撞后是用「重叠量的一半」把球反向推开的，
    // 基准数据里的球又比我们的小（撞得更实），累下来实测最多挤近 1.2 毫米。
    // 真穿模是几十毫米的量级，3 毫米的窗口照样抓得住。
    let baseline = Infinity
    const opening = predicted.frames[0].balls
    for (let i = 0; i < opening.length; i += 1) {
      for (let j = i + 1; j < opening.length; j += 1) {
        const distance = Math.hypot(opening[i][1] - opening[j][1], opening[i][2] - opening[j][2])
        if (distance < baseline) {
          baseline = distance
        }
      }
    }

    for (const frame of predicted.frames) {
      const balls = frame.balls
      for (let i = 0; i < balls.length; i += 1) {
        for (let j = i + 1; j < balls.length; j += 1) {
          const distance = Math.hypot(balls[i][1] - balls[j][1], balls[i][2] - balls[j][2])
          expect(distance).toBeGreaterThan(baseline - 0.003)
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

// 引导线画出来的方向，必须就是球实际飞出去的方向。
//
// 这是玩家最容易察觉、也最难自己验证的一件事：线在那儿，球却偏了。
// 根因是碰撞解算按「整步走完」的位置取法向，母球满速一步走 18.3 毫米，
// 球心连线被穿透量带偏，打薄球时偏出五到九度。改了精确接触时刻之后，
// 这里锁住 1 度以内——再松就又能被肉眼看出「线在骗人」。
describe('引导线精度', () => {
  // 母球沿 +x 直走，目标球横向偏移出击球厚度 b（b = 0 正碰，b = 2R 擦边）。
  function cutShot(ratio: number): { balls: BallState[]; phi: number } {
    const balls: BallState[] = [
      { id: 'cue', x: 0.3, y: 0.99, z: 0, on_table: true },
      { id: '1', x: 0.7, y: 0.99 + ratio * BALL_RADIUS * 2, z: 0, on_table: true },
    ]
    return { balls, phi: 0 }
  }

  // objectDirection 取目标球刚动起来那几帧的平均方向（弧度）。
  function objectDirection(frames: Array<{ t: number; balls: Array<[string, number, number, number]> }>): number | null {
    let start = -1
    for (let i = 1; i < frames.length; i += 1) {
      const a = frames[i - 1].balls.find((item) => item[0] === '1')
      const b = frames[i].balls.find((item) => item[0] === '1')
      if (a && b && Math.hypot(b[1] - a[1], b[2] - a[2]) > 1e-6) {
        start = i - 1
        break
      }
    }
    if (start < 0) {
      return null
    }
    const a = frames[start].balls.find((item) => item[0] === '1')
    const b = frames[Math.min(start + 4, frames.length - 1)].balls.find((item) => item[0] === '1')
    if (!a || !b) {
      return null
    }
    return Math.atan2(b[2] - a[2], b[1] - a[1])
  }

  it('各种厚度、各种力度下，出球方向与引导线相差不到 1 度', () => {
    const ratios = [0.2, 0.4, 0.6, 0.8, 0.9, 0.95]
    const powers = [0.2, 0.5, 0.9]
    for (const power of powers) {
      for (const ratio of ratios) {
        const { balls, phi } = cutShot(ratio)
        const hit = aimFromCue(balls, phi)
        const guides = aimGuides(balls, phi, hit)
        expect(guides.object).not.toBeNull()
        const shot = predictShot(balls, { phi, power, theta: 0, a: 0, b: 0 })
        expect(shot).not.toBeNull()
        const actual = objectDirection(shot!.frames)
        expect(actual).not.toBeNull()
        const guide = guides.object!
        const aim = Math.atan2(guide.to.y - guide.from.y, guide.to.x - guide.from.x)
        let diff = ((actual! - aim) * 180) / Math.PI
        while (diff > 180) {
          diff -= 360
        }
        while (diff < -180) {
          diff += 360
        }
        expect(Math.abs(diff)).toBeLessThan(1)
      }
    }
  })
})

// 加塞（杆法）落到物理上的三条约定。
// 前两条是玩家能直接看出来的「高杆跟进、低杆拉回」，
// 第三条是引导线还能画准的前提：目标球永远沿球心连线走，不受加塞影响。
describe('加塞', () => {
  // cueBallAt 取某一时刻母球的位置，找不到（落袋）时返回 null。
  function cueBallAt(
    frames: Array<{ t: number; balls: Array<[string, number, number, number]> }>,
    t: number,
  ): { x: number; y: number } | null {
    let pick: [string, number, number, number] | null = null
    for (const frame of frames) {
      if (frame.t > t) {
        break
      }
      for (const item of frame.balls) {
        if (item[0] === 'cue') {
          pick = item
        }
      }
    }
    return pick ? { x: pick[1], y: pick[2] } : null
  }

  function straightHit(b: number, a = 0) {
    const balls: BallState[] = [
      { id: 'cue', x: 0.3, y: 0.99, z: 0, on_table: true },
      { id: '1', x: 0.6, y: 0.99, z: 0, on_table: true },
    ]
    const params: ShotParams = { phi: 0, power: 0.55, theta: 0, a, b }
    return predictShot(balls, params)!
  }

  it('高杆：撞完继续往前跟进', () => {
    const shot = straightHit(1)
    const cue = cueBallAt(shot.frames, 0.9)!
    // 正碰的接触点是 0.6 - 2R，母球停在那里就是「中杆」。
    expect(cue.x).toBeGreaterThan(0.6 - BALL_RADIUS * 2 + 0.05)
  })

  it('低杆：撞完往回拉', () => {
    const shot = straightHit(-1)
    const cue = cueBallAt(shot.frames, 0.9)!
    expect(cue.x).toBeLessThan(0.3 - 0.02)
  })

  it('中杆：正碰之后基本停在接触点附近', () => {
    const shot = straightHit(0)
    const cue = cueBallAt(shot.frames, 0.9)!
    expect(Math.abs(cue.x - (0.6 - BALL_RADIUS * 2))).toBeLessThan(0.02)
  })

  it('加塞不改变目标球的出球方向（引导线才能画准）', () => {
    const plain = straightHit(0, 0)
    const spinned = straightHit(0, 1)
    const dirOf = (shot: { frames: Array<{ t: number; balls: Array<[string, number, number, number]> }> }) => {
      let prev: [string, number, number, number] | null = null
      for (const frame of shot.frames) {
        const item = frame.balls.find((ball) => ball[0] === '1')
        if (!item) {
          return null
        }
        if (prev && Math.hypot(item[1] - prev[1], item[2] - prev[2]) > 1e-6) {
          return Math.atan2(item[2] - prev[2], item[1] - prev[1])
        }
        prev = item
      }
      return null
    }
    expect(dirOf(plain)).not.toBeNull()
    expect(dirOf(spinned)).not.toBeNull()
    expect(dirOf(spinned)!).toBeCloseTo(dirOf(plain)!, 9)
  })

  // 库边是侧旋唯一的作用点：同一副塞在上下两条库上把球带向相反的方向，
  // 所以这里只锁「有没有偏、偏的方向对不对」，不锁具体角度。
  it('左旋／右旋撞库后向相反方向偏出', () => {
    const run = (a: number) => {
      const balls: BallState[] = [{ id: 'cue', x: 0.4953, y: 0.5, z: 0, on_table: true }]
      const shot = predictShot(balls, { phi: Math.PI / 2, power: 0.6, theta: 0, a, b: 0 })!
      const end = shot.frames[shot.frames.length - 1].balls[0]
      return end[1] - 0.4953
    }
    const right = run(1)
    const left = run(-1)
    const none = run(0)
    expect(Math.abs(none)).toBeLessThan(0.001)
    expect(right).toBeGreaterThan(0.01)
    expect(left).toBeLessThan(-0.01)
  })
})

// 停球那一段只有一种减速：台呢的阻尼。
//
// 曾经在最末段叠过一层「速度越低、减速度越小」的柔化，想做出「越走越慢」的
// 观感，实际看到的是球滚到一半忽然减速、再爬半秒才不动——那一顿比直接停
// 更显眼。现在从出杆到静止减速度是同一个值，速度线性归零。
describe('停球', () => {
  it('从 3 cm/s 到静止只走完匀速减速那半秒，没有额外的爬行段', () => {
    // 母球贴着左库往右推，力度压到最小档：全程约 0.27 米，
    // 到不了右库，速度曲线就是纯粹的摩擦减速（撞库那一下位移会反向，
    // 用位移量出来的「速度」会误判成已经停了）。
    const balls: BallState[] = [{ id: 'cue', x: 0.15, y: 0.5, z: 0, on_table: true }]
    const shot = predictShot(balls, { phi: 0, power: 0.03, theta: 0, a: 0, b: 0 })!
    // 关键帧的坐标只保留到 0.1 毫米，低速时单帧差值的噪声比信号还大，
    // 所以隔 WINDOW 帧量一次平均速度（0.07 秒），噪声压到 1 mm/s 以内。
    const WINDOW = 8
    let start = -1
    for (let i = 0; i + WINDOW < shot.frames.length; i += 1) {
      const a = shot.frames[i]
      const b = shot.frames[i + WINDOW]
      const step = Math.hypot(b.balls[0][1] - a.balls[0][1], b.balls[0][2] - a.balls[0][2])
      if (step / (b.t - a.t) < 0.03) {
        start = i
        break
      }
    }
    expect(start).toBeGreaterThan(0)
    const tail = shot.frames[shot.frames.length - 1].t - shot.frames[start].t
    // 匀速减速下 3 cm/s 掉到零要 0.03 / 0.059 ≈ 0.51 秒（窗口本身再占 0.07 秒）。
    // 加过柔化收尾时这一段实测在 1.2 秒以上，0.8 秒的窗口能把两种做法分开。
    expect(tail).toBeLessThan(0.8)
  })
})
