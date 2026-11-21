// 本地假物理。**M3 接上 pool-sim 之后整个文件删除。**
//
// 存在的唯一理由是：物理服务还没写完之前，前端要能自己打一杆，
// 把瞄准、力度、轨迹播放、球位更新这条链路先跑通。它不追求真实，
// 只要「球会走、会撞、会停、能进袋」就够验收。
//
// 刻意做得粗糙：
//   - 没有旋转、没有摩擦转滚动、没有库边弹性差异，撞库一律按固定恢复系数反弹；
//   - 球球碰撞用等质量弹性碰撞的简化式，不考虑入射角与转动；
//   - 进袋判定只看球心离袋口中心的距离。
// 任何一条拿它当真实物理的用法都是错的，真实结果以服务端为准。
import { BALL_RADIUS, POCKETS, TABLE_LENGTH, TABLE_WIDTH } from './table'
import type { BallState } from './types'
import type { Shot, ShotFrame } from './animator'

// 积分步长。1/120 秒足够让球速 5m/s 时每步只走 4cm，不会穿球。
const STEP = 1 / 120

// 采样频率，与服务端一致。
const FRAME_RATE = 30

// 台呢滚动的减速度，单位 m/s²。真实台球约 0.2 到 0.4，这里给大一点，
// 让一杆在 3 秒内停下，验收时不用盯着看太久。
const FRICTION = 1.1

// 撞库后保留的速度比例。
const CUSHION_BOUNCE = 0.72

// 球球碰撞的恢复系数。
const BALL_BOUNCE = 0.94

// 低于这个速度直接停住，避免球以 0.001m/s 慢慢蹭到天荒地老。
const STOP_SPEED = 0.03

// 一杆最长模拟时间，兜住「球在两个库之间来回弹」这种停不下来的情况。
const MAX_SECONDS = 10

// MovingBall 是模拟过程中每颗球的动态状态。
interface MovingBall {
  id: string
  x: number
  y: number
  vx: number
  vy: number
  onTable: boolean
}

// fakeShot 根据当前球位与击球参数生成一条轨迹。
//
// power 取 0 到 1，映射到初速度 1.2 到 5.6 m/s：轻推能滚一小段，
// 满力能开球。角度沿用 pooltool 的 phi（0 指向 +x，π/2 指向 +y）。
export function fakeShot(balls: ReadonlyArray<BallState>, phi: number, power: number): Shot {
  const moving: MovingBall[] = []
  for (const ball of balls) {
    moving.push({
      id: ball.id,
      x: ball.x,
      y: ball.y,
      vx: 0,
      vy: 0,
      onTable: ball.on_table,
    })
  }

  const cue = findCue(moving)
  const speed = 1.2 + clamp01(power) * 4.4
  if (cue && power > 0.001) {
    cue.vx = Math.cos(phi) * speed
    cue.vy = Math.sin(phi) * speed
  }

  const frames: ShotFrame[] = []
  let elapsed = 0
  let sinceFrame = 0

  // 第 0 帧一定要有，否则播放器第一帧拿到空场景，球会闪一下再出现。
  frames.push(snapshot(moving, 0))

  while (elapsed < MAX_SECONDS) {
    elapsed += STEP
    sinceFrame += STEP

    integrate(moving)
    resolveBallCollisions(moving)
    settle(moving)

    // 全部停下就结束，不用跑满 10 秒。
    if (!anyMoving(moving)) {
      break
    }

    if (sinceFrame >= 1 / FRAME_RATE - 1e-9) {
      sinceFrame = 0
      frames.push(snapshot(moving, elapsed))
    }
  }

  // 收尾再补一帧：最后一帧是「仍在动」的状态，直接停播会让球停在半路。
  frames.push(snapshot(moving, elapsed))

  return { frames, duration: elapsed }
}

// integrate 推进一小步：位置按速度前进，速度按摩擦衰减，越界就反弹或落袋。
function integrate(moving: MovingBall[]): void {
  const decel = FRICTION * STEP

  for (const ball of moving) {
    if (!ball.onTable) {
      continue
    }

    const speed = Math.hypot(ball.vx, ball.vy)
    if (speed <= decel) {
      ball.vx = 0
      ball.vy = 0
      continue
    }
    // 摩擦沿速度反方向，等比缩小，速度方向不变。
    const scale = (speed - decel) / speed
    ball.vx *= scale
    ball.vy *= scale

    ball.x += ball.vx * STEP
    ball.y += ball.vy * STEP

    bounceCushion(ball)

    if (inPocket(ball.x, ball.y)) {
      ball.onTable = false
      ball.vx = 0
      ball.vy = 0
    }
  }
}

// bounceCushion 处理台面四边的反弹。台面内沿减去一个球半径是球心的活动范围。
function bounceCushion(ball: MovingBall): void {
  const minX = BALL_RADIUS
  const maxX = TABLE_WIDTH - BALL_RADIUS
  const minY = BALL_RADIUS
  const maxY = TABLE_LENGTH - BALL_RADIUS

  if (ball.x < minX) {
    ball.x = minX
    ball.vx = Math.abs(ball.vx) * CUSHION_BOUNCE
  } else if (ball.x > maxX) {
    ball.x = maxX
    ball.vx = -Math.abs(ball.vx) * CUSHION_BOUNCE
  }
  if (ball.y < minY) {
    ball.y = minY
    ball.vy = Math.abs(ball.vy) * CUSHION_BOUNCE
  } else if (ball.y > maxY) {
    ball.y = maxY
    ball.vy = -Math.abs(ball.vy) * CUSHION_BOUNCE
  }
}

// inPocket 判断球心是否落进某个袋口。
function inPocket(x: number, y: number): boolean {
  for (const p of POCKETS) {
    if (Math.hypot(x - p.x, y - p.y) <= p.radius) {
      return true
    }
  }
  return false
}

// resolveBallCollisions 处理球与球的碰撞。
//
// 等质量、速度沿连心线交换法向分量，再乘恢复系数。位置要同时推开，
// 否则两球会卡在一起，下一帧继续判定为重叠，越抖越远。
function resolveBallCollisions(moving: MovingBall[]): void {
  const minDist = BALL_RADIUS * 2

  for (let i = 0; i < moving.length; i += 1) {
    const a = moving[i]
    if (!a.onTable) {
      continue
    }
    for (let j = i + 1; j < moving.length; j += 1) {
      const b = moving[j]
      if (!b.onTable) {
        continue
      }
      const dx = b.x - a.x
      const dy = b.y - a.y
      const dist = Math.hypot(dx, dy)
      if (dist === 0 || dist >= minDist) {
        continue
      }

      // 法线方向（从 a 指向 b）。
      const nx = dx / dist
      const ny = dy / dist
      const overlap = (minDist - dist) / 2
      a.x -= nx * overlap
      a.y -= ny * overlap
      b.x += nx * overlap
      b.y += ny * overlap

      // 沿法线的相对速度，接近时才交换（分开时再去推一把会让球互相吸回来）。
      const relative = (b.vx - a.vx) * nx + (b.vy - a.vy) * ny
      if (relative >= 0) {
        continue
      }
      const impulse = (-(1 + BALL_BOUNCE) * relative) / 2
      a.vx -= impulse * nx
      a.vy -= impulse * ny
      b.vx += impulse * nx
      b.vy += impulse * ny
    }
  }
}

// settle 把低速球彻底停住，避免无限小的速度让模拟跑满 10 秒。
function settle(moving: MovingBall[]): void {
  for (const ball of moving) {
    if (!ball.onTable) {
      continue
    }
    if (Math.hypot(ball.vx, ball.vy) < STOP_SPEED) {
      ball.vx = 0
      ball.vy = 0
    }
  }
}

// anyMoving 判断还有没有球在动。
function anyMoving(moving: MovingBall[]): boolean {
  for (const ball of moving) {
    if (ball.onTable && (ball.vx !== 0 || ball.vy !== 0)) {
      return true
    }
  }
  return false
}

// snapshot 把当前状态压成一帧。只放还在台面上的球，与服务端协议一致。
function snapshot(moving: MovingBall[], t: number): ShotFrame {
  const balls: ShotFrame['balls'] = []
  for (const ball of moving) {
    if (!ball.onTable) {
      continue
    }
    balls.push([ball.id, round4(ball.x), round4(ball.y), 0])
  }
  return { t: round4(t), balls }
}

// round4 保留四位小数，0.1mm 精度远超绘制需要，也让帧数据小一半。
function round4(n: number): number {
  return Math.round(n * 10000) / 10000
}

// findCue 找母球。
function findCue(moving: MovingBall[]): MovingBall | null {
  for (const ball of moving) {
    if (ball.id === 'cue') {
      return ball
    }
  }
  return null
}

// clamp01 把力度收进 0 到 1。
function clamp01(n: number): number {
  if (n < 0) {
    return 0
  }
  if (n > 1) {
    return 1
  }
  return n
}
