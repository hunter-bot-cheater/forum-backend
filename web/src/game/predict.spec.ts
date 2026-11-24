import { describe, expect, it } from 'vitest'
import fixtureRaw from './__fixtures__/pooltool-shots.json?raw'
import { aimFromCue, aimGuides } from './aim'
import { fitTable, worldToScreen } from './geometry'
import { predictShot, speedOfPower, spinThrowDeg } from './predict'
import { TABLE_PADDING } from './renderer'
import { BALL_RADIUS, TABLE_LENGTH } from './table'
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
// powerForSpeed 反查「要多少力度才有这个出速」。
//
// 这些用例关心的是物理（甩球角、低杆回拉、引导线精度），不该被力度曲线
// 牵着走：曲线从线性改成平方之后，同一个力度对应的球速掉了一大半，
// 直接写 0.55 的用例会莫名其妙地失败——而它想说的其实一直是「中速的一杆」。
// 所以这里按 m/s 反查力度：以后无论曲线怎么改，用例说的还是同一件事。
function powerForSpeed(target: number): number {
  let lo = 0
  let hi = 1
  for (let i = 0; i < 40; i += 1) {
    const mid = (lo + hi) / 2
    if (speedOfPower(mid) < target) {
      lo = mid
    } else {
      hi = mid
    }
  }
  return (lo + hi) / 2
}

// MEDIUM_SPEED 是这批用例默认的「中速一杆」：2.44 m/s，
// 也就是改曲线之前 0.55 那个力度对应的速度。
const MEDIUM_SPEED = 2.44

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
    // 慢、中、快三档，按速度给。低于 0.8 m/s 时 0.4 米的距离根本打不到目标球，
    // 而这组用例要的是「撞上之后方向准不准」。
    const powers = [powerForSpeed(0.9), powerForSpeed(MEDIUM_SPEED), powerForSpeed(3.8)]
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
    const params: ShotParams = { phi: 0, power: powerForSpeed(MEDIUM_SPEED), theta: 0, a, b }
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

  // dirOf 取目标球被撞之后那一段的方向（弧度）。
  //
  // 起点是「它开始动的那一帧」，终点隔 10 帧（约 0.08 秒，十来厘米）：
  // 只隔一帧的话位移只有几毫米，而关键帧坐标只保留 0.1 毫米，
  // 侧向那一点点位移会被量化噪声盖住（实测能把 1° 量成 1.7°）。
  // 10 帧之内它还没走到库边，方向仍是碰撞刚给的那个。
  function dirOf(shot: {
    frames: Array<{ t: number; balls: Array<[string, number, number, number]> }>
  }): number | null {
    for (let i = 1; i < shot.frames.length; i += 1) {
      const prev = shot.frames[i - 1].balls.find((ball) => ball[0] === '1')
      const cur = shot.frames[i].balls.find((ball) => ball[0] === '1')
      if (!prev || !cur) {
        return null
      }
      if (Math.hypot(cur[1] - prev[1], cur[2] - prev[2]) <= 1e-6) {
        continue
      }
      const end = shot.frames[Math.min(i + 10, shot.frames.length - 1)].balls.find(
        (ball) => ball[0] === '1',
      )
      if (!end) {
        return null
      }
      return Math.atan2(end[2] - cur[2], end[1] - cur[1])
    }
    return null
  }

  // 加塞撞正碰的球：目标球会被甩离球心连线，右塞一边、左塞另一边。
  //
  // 这是真实台球的「甩球」（throw）。以前的模型让目标球永远走球心连线，
  // 满塞打出去和空杆一模一样，玩家会发现塞在撞球时毫无作用。
  it('加塞把目标球甩偏，左右塞方向相反', () => {
    const plain = (dirOf(straightHit(0, 0))! * 180) / Math.PI
    const right = (dirOf(straightHit(0, 1))! * 180) / Math.PI
    const left = (dirOf(straightHit(0, -1))! * 180) / Math.PI

    // 不加塞时就是球心连线。
    expect(plain).toBeCloseTo(0, 6)
    // 满塞在 2.4 m/s 的球速下应当偏出零点几度：太小看不见，超过 5° 就不真实了。
    expect(Math.abs(right)).toBeGreaterThan(0.3)
    expect(Math.abs(right)).toBeLessThan(5)
    // 左右镜像。
    expect(right).toBeCloseTo(-left, 6)
  })

  // 甩球曲线本身：资料里那几个特征点必须成立。
  //
  // 最大甩球出现在「慢速、中杆、约一半塞」，不是满塞；高杆低杆各压掉四分之三；
  // 球速越快甩得越少。这三条是 Dr. Dave 的实测结论，也是玩家能感觉到的差别。
  it('甩球角：半颗塞最大、满塞更小、高杆低杆压掉大半', () => {
    const half = Math.abs(spinThrowDeg(0.5, 1, 0))
    const full = Math.abs(spinThrowDeg(1, 1, 0))
    const quarter = Math.abs(spinThrowDeg(0.25, 1, 0))
    expect(half).toBeGreaterThan(quarter)
    expect(half).toBeGreaterThan(full)

    // 高低杆把甩球压到四分之一上下。
    const withFollow = Math.abs(spinThrowDeg(0.5, 1, 1))
    expect(withFollow).toBeLessThan(half * 0.3)

    // 快速球几乎不甩。
    const fast = Math.abs(spinThrowDeg(0.5, 4, 0))
    expect(fast).toBeLessThan(half * 0.25)

    // 中杆零塞一定不甩，方向随塞号反过来。
    expect(spinThrowDeg(0, 1, 0)).toBe(0)
    expect(spinThrowDeg(0.5, 1, 0)).toBe(-spinThrowDeg(-0.5, 1, 0))
  })

  // 引导线要跟着甩球角一起偏，否则「线指着袋口、球擦着袋角走」。
  it('引导线的目标球段与预测的甩出方向一致', () => {
    const balls: BallState[] = [
      { id: 'cue', x: 0.3, y: 0.99, z: 0, on_table: true },
      { id: '1', x: 0.6, y: 0.99, z: 0, on_table: true },
    ]
    const hit = aimFromCue(balls, 0)
    const guides = aimGuides(balls, 0, hit, {
      a: 1,
      b: 0,
      speed: speedOfPower(powerForSpeed(MEDIUM_SPEED)),
    })
    expect(guides.object).not.toBeNull()

    const from = guides.object!
    const guideDeg =
      (Math.atan2(from.to.y - from.from.y, from.to.x - from.from.x) * 180) / Math.PI
    const realDeg = (dirOf(straightHit(0, 1))! * 180) / Math.PI
    // 引导线用的是出杆初速，实际撞上时球已经掉了几 % 速度，差 0.2° 以内算对上。
    expect(Math.abs(guideDeg - realDeg)).toBeLessThan(0.2)
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

// 停球那一段：球要停在「肉眼能看见它一点点慢下来」的那个过程里。
//
// 收尾试过三种做法，观感差别很大：
//   纯指数（只有 u_r 与粘性项）——最后三秒只走一两厘米，屏幕上不到 2 个像素，
//     看着像「滚到某处忽然定住」；
//   阻尼随速度增大（v 越小刹得越狠）——末段位移是有了，但全堆在最后半秒：
//     前 1 秒几乎匀速，最后 0.4 秒从 57 px/s 塌到 6 px/s，观感是「匀速滚完
//     啪一下刹住」；
//   现在（predict.ts 的 tailDecel）——进末段后在 TAIL_RAMP 这段速度区间里
//     平滑升到恒定减速度，速度因此线性趋零，每 0.1 秒的位移稳定地少一点点。
//
// 判据也跟着换成两个：最后 0.6 秒还得看得见在动，且这段位移要均匀递减——
// 不能出现「前一段 6 像素、后一段 0.5 像素」的塌陷，那正是「突然停下」的来源。
describe('停球', () => {
  const view = fitTable({ width: 390, height: 844 }, TABLE_PADDING)

  // tailSteps 把「停球前 seconds 秒」按 0.1 秒切片，返回每段母球在屏幕上走过
  // 的像素数，最后一项是结尾那 0.1 秒。
  //
  // 用 50% 力度、单球沿长边滚：这一段没有别的球、也没有库边折返，
  // 位移就是纯粹的摩擦减速，量出来是什么就是什么。
  function tailSteps(seconds: number): number[] {
    const balls: BallState[] = [{ id: 'cue', x: 0.4953, y: 0.3, z: 0, on_table: true }]
    const shot = predictShot(balls, { phi: Math.PI / 2, power: 0.5, theta: 0, a: 0, b: 0 })!
    const frames = shot.frames
    const end = frames[frames.length - 1].t
    // 关键帧固定 120Hz，按时间反查下标就够了，不必线性扫。
    const at = (time: number) => {
      const i = Math.round(time * 120)
      if (i < 0) {
        return frames[0]
      }
      if (i > frames.length - 1) {
        return frames[frames.length - 1]
      }
      return frames[i]
    }
    const steps: number[] = []
    const count = Math.round(seconds / 0.1)
    for (let k = count; k >= 1; k -= 1) {
      const from = at(end - k * 0.1)
      const to = at(end - (k - 1) * 0.1)
      const a = worldToScreen(view, from.balls[0][1], from.balls[0][2])
      const b = worldToScreen(view, to.balls[0][1], to.balls[0][2])
      steps.push(Math.hypot(b.x - a.x, b.y - a.y))
    }
    return steps
  }

  it('最后 0.6 秒仍然看得出在滚，不是一下子定住', () => {
    let px = 0
    for (const step of tailSteps(0.6)) {
      px += step
    }
    // 390×844 下 1 米约 304 像素，球直径才 12 像素。低于 8 像素就是
    // 「看着已经停了」；高过 40 像素说明末段还在猛冲，收尾会显得被掐断。
    expect(px).toBeGreaterThan(8)
    expect(px).toBeLessThan(40)
  })

  it('越到后面走得越少，没有哪一段骤降', () => {
    const steps = tailSteps(1.2)
    for (let i = 1; i < steps.length; i += 1) {
      const prev = steps[i - 1]
      const cur = steps[i]
      // 后一段不能比前一段还大（留 0.2 像素给坐标量化噪声）。
      expect(cur).toBeLessThan(prev + 0.2)
      // 也不能突然塌掉。旧实现最后几段是 5.0 → 4.2 → 3.2 → 2.1 → 0.9 → 0.2，
      // 相邻比值最低到 0.2，看着就是「啪」；现在的最低比值在 0.4 以上。
      expect(cur).toBeGreaterThan(prev * 0.3)
    }
    // 最后 0.4 秒合起来还得有位移，否则「一点点停」只是纸面上的说法。
    let last = 0
    for (let i = steps.length - 4; i < steps.length; i += 1) {
      last += steps[i]
    }
    expect(last).toBeGreaterThan(2)
  })
})

// 力度到球速的曲线。
//
// 这条曲线决定「拉一半是多大力」，是手感里最容易被忽略、又最影响成绩的一层。
// 它必须按**距离**来验，不能按 m/s：玩家心里的力度就是球能跑多远，
// 而距离与初速几乎成正比（阻尼里那份跟速度成正比的项占主导），
// 所以「线性球速」恰好等于「线性距离」。曾经改成平方去「照顾低端精度」，
// 结果一半力度只滚 3.2 米、下半条等于没有，实机被否掉。
describe('力度曲线', () => {
  // rollDistance 量一颗母球沿长边自由滚的总路程。
  //
  // 母球放在长边中线上，phi 指向长边，只算这一颗球：
  // 把 120Hz 关键帧之间的位移累加起来，库边反弹的损耗也在里面。
  // 起点避开中袋口，免得一杆打进去。
  function rollDistance(power: number): number {
    const balls: BallState[] = [{ id: 'cue', x: 0.4953, y: 0.3, z: 0, on_table: true }]
    const shot = predictShot(balls, { phi: Math.PI / 2, power, theta: 0, a: 0, b: 0 })
    if (shot === null) {
      return 0
    }
    let total = 0
    let prev: [number, number] | null = null
    for (const frame of shot.frames) {
      const cue = frame.balls.find((b) => b[0] === 'cue')
      if (cue === undefined) {
        break
      }
      if (prev !== null) {
        total += Math.hypot(cue[1] - prev[0], cue[2] - prev[1])
      }
      prev = [cue[1], cue[2]]
    }
    return total
  }

  it('两端是 0.05 与 4.4，中点是半速', () => {
    // 满力必须还是 4.4：开球靠它。
    expect(speedOfPower(1)).toBeCloseTo(4.4, 6)
    expect(speedOfPower(0)).toBeCloseTo(0.05, 6)
    // 一半力度就是一半速度，开根号或平方都不对。
    expect(speedOfPower(0.5)).toBeCloseTo(2.225, 2)
  })

  it('单调：加力一定更快', () => {
    let prev = -1
    for (let i = 0; i <= 20; i += 1) {
      const next = speedOfPower(i / 20)
      expect(next).toBeGreaterThan(prev)
      prev = next
    }
  })

  it('距离与力度近乎成正比：一半力度能滚一半路程', () => {
    const half = rollDistance(0.5)
    const full = rollDistance(1)
    // 实测 6.23 / 12.0 = 0.52。放宽到 0.45~0.6 只挡住「拐得太狠」的曲线，
    // 不锁死摩擦参数——u_r 与 ROLL_VISCOUS 调整会让这个比小幅漂移。
    expect(half / full).toBeGreaterThan(0.45)
    expect(half / full).toBeLessThan(0.6)
    // 一半力度至少要能滚过两张台长（3.96 米），否则中距离球全得靠满力。
    expect(half).toBeGreaterThan(2 * TABLE_LENGTH)
  })

  it('下半条不是死区：两成力度就能滚过大半张台', () => {
    // 平方曲线时这里是 0.33 米，台面都过不去，玩家反馈「一半以下没意义」。
    // 现在 0.2 是 1.84 米、0.3 是 3.22 米。
    // 注意 0.1 力度只有 0.4 米（纯指数收尾时是 1.10 米）：那 1.1 米里有七成是
    // 「最后三秒只走一两厘米」的爬行，收尾一压缩就没了。轻推本来就只用来
    // 贴球、解斯诺克，0.4 米仍在可用范围里；真要更远就推到 0.15 以上。
    expect(rollDistance(0.2)).toBeGreaterThan(1.7)
    expect(rollDistance(0.3)).toBeGreaterThan(1.55 * TABLE_LENGTH)
  })
})
