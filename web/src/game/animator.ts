// 一杆轨迹的插值与播放。
//
// 关键帧由本地预测直接生成，现在按 120Hz 采样（原来 30Hz，后来 60Hz，
// 撞库折点与慢速滚动都能看出一顿一顿）。播放仍按时间插值：
// 60Hz 屏上下隔一帧取样，120Hz 屏一帧一个采样点，掉帧时也不会把球停在两帧之间。
//
// 帧格式与服务端协议一致：t 是秒，balls 是 [球号, x, y, z] 的数组。
// **每一帧都是那一刻整张台面的快照**（pool-sim 把仍在台上的每颗球都写进去），
// 所以单独一帧就足以还原当时的局面；落袋的球从它消失的那一帧起不再出现，
// 调用方据此把它从画面上去掉。
import type { BallState } from './types'

// ShotFrame 是一帧关键帧。
export interface ShotFrame {
  t: number
  balls: Array<[string, number, number, number]>
}

// Shot 是一杆的完整轨迹。
export interface Shot {
  frames: ShotFrame[]
  duration: number
}

// SampledBall 是插值后某一时刻一颗球的位置。
export interface SampledBall {
  id: string
  x: number
  y: number
  z: number
}

// sampleAt 取 t 时刻所有还在台面上的球。
//
// 落在两帧之间时线性插值；t 早于第一帧取第一帧，晚于最后一帧取最后一帧，
// 这样播放器首尾两帧不会闪一下空场景。
// 进袋的球在它消失的那一帧之后不再返回，调用方据此把它从画面上去掉。
export function sampleAt(frames: ReadonlyArray<ShotFrame>, t: number): SampledBall[] {
  if (frames.length === 0) {
    return []
  }
  if (frames.length === 1) {
    return toSampled(frames[0])
  }

  // 帧按时间递增，可以用二分找区间。一杆 200 帧左右，线性扫也够快，
  // 但播放时每帧都要调一次，二分省下的是一帧里的固定开销。
  let lo = 0
  let hi = frames.length - 1
  if (t <= frames[0].t) {
    return toSampled(frames[0])
  }
  if (t >= frames[hi].t) {
    return toSampled(frames[hi])
  }
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1
    if (frames[mid].t <= t) {
      lo = mid
    } else {
      hi = mid
    }
  }

  const a = frames[lo]
  const b = frames[hi]
  const span = b.t - a.t
  const k = span > 0 ? (t - a.t) / span : 0

  // 以「后一帧」的球集合为准：这一帧还在的球才需要继续画。
  // 前一帧有、后一帧没有，说明它在这个区间里落袋了，直接不返回。
  const before = indexById(a)
  const result: SampledBall[] = []
  for (const ball of b.balls) {
    const [id, bx, by, bz] = ball
    const prev = before.get(id)
    if (!prev) {
      // 区间中途才出现的球（正常不会有），按当前位置画，不插值。
      result.push({ id, x: bx, y: by, z: bz })
      continue
    }
    result.push({
      id,
      x: prev[0] + (bx - prev[0]) * k,
      y: prev[1] + (by - prev[1]) * k,
      z: prev[2] + (bz - prev[2]) * k,
    })
  }
  return result
}

// toSampled 把一帧原样转成采样结果。
function toSampled(frame: ShotFrame): SampledBall[] {
  const result: SampledBall[] = []
  for (const [id, x, y, z] of frame.balls) {
    result.push({ id, x, y, z })
  }
  return result
}

// indexById 建一份「球号 → [x, y, z]」的索引，用于插值时查上一帧。
function indexById(frame: ShotFrame): Map<string, [number, number, number]> {
  const map = new Map<string, [number, number, number]>()
  for (const [id, x, y, z] of frame.balls) {
    map.set(id, [x, y, z])
  }
  return map
}

// applySample 把采样结果写回球数组。
//
// 不在采样结果里的球标记为已落袋，而不是从数组里删掉：
// 球的原始顺序与对象要在整局里保持稳定，删掉会让后续按 id 找球的逻辑失效。
// 返回值是原数组，方便调用方链式使用。
export function applySample(balls: BallState[], sample: ReadonlyArray<SampledBall>): BallState[] {
  const byId = new Map<string, BallState>()
  for (const ball of balls) {
    ball.on_table = false
    byId.set(ball.id, ball)
  }
  for (const item of sample) {
    const ball = byId.get(item.id)
    if (!ball) {
      // 采样里出现了球局没有的球号，说明协议不一致。跳过而不是补一颗，
      // 补出来的球没有初始位置，下一帧就不知道往哪画了。
      continue
    }
    ball.on_table = true
    ball.x = item.x
    ball.y = item.y
    ball.z = item.z
  }
  return balls
}

// PlayOptions 是播放一杆的参数。
export interface PlayOptions {
  // onSample 每帧回调一次，参数是该时刻的球位与相对这一杆开始的秒数。
  onSample: (sample: SampledBall[], elapsed: number) => void
  // onDone 播完调一次。
  onDone?: () => void
  // startAt 从第几秒开始播，断线重连补播时用。
  startAt?: number
  // speed 播放倍速，默认 1。
  speed?: number
}

// PlayHandle 用于中途停止播放（对手退出、自己离开页面）。
export interface PlayHandle {
  stop: () => void
}

// playShot 按真实时间播放一杆。
//
// 用 requestAnimationFrame 而不是 setInterval：rAF 与屏幕刷新同步，
// 掉帧时自动跳过中间帧，不会越播越慢；标签页切到后台时它会暂停，
// 回来时按真实时间戳算进度，球的位置仍然是对的。
export function playShot(shot: Shot, options: PlayOptions): PlayHandle {
  const speed = options.speed && options.speed > 0 ? options.speed : 1
  const startedAt = performance.now() - (options.startAt ?? 0) * 1000
  let handle = 0
  let stopped = false

  const step = () => {
    if (stopped) {
      return
    }
    const elapsed = ((performance.now() - startedAt) / 1000) * speed
    if (elapsed >= shot.duration) {
      options.onSample(sampleAt(shot.frames, shot.duration), shot.duration)
      options.onDone?.()
      return
    }
    options.onSample(sampleAt(shot.frames, elapsed), elapsed)
    handle = requestAnimationFrame(step)
  }

  handle = requestAnimationFrame(step)

  return {
    stop() {
      stopped = true
      if (handle) {
        cancelAnimationFrame(handle)
      }
    },
  }
}
