// 瞄准计算：从母球沿 phi 发一条射线，找出最先撞到的球或库边。
//
// 只做几何，不碰 canvas。渲染拿这里的结果去画瞄准线与幽灵球，
// 命中判定也复用它，两条路径共用一套数学，不会出现「线画在球上、点下去却没碰」。
//
// phi 的约定沿用 pooltool：速度方向 = (cos phi, sin phi)，phi=0 指向 +x，
// phi=π/2 指向 +y（台面远端）。屏幕的 y 轴朝下，所以由屏幕位移反推 phi 时要取负。
import { BALL_RADIUS, TABLE_LENGTH, TABLE_WIDTH } from './table'
import type { BallState } from './types'

// 两球接触时球心的距离，等于一个球直径。
const CONTACT_DISTANCE = BALL_RADIUS * 2

// AimHit 是一次瞄准的第一个障碍。
export interface AimHit {
  // ball 表示先撞到球，cushion 表示先到库边。
  kind: 'ball' | 'cushion'
  // 母球球心在接触瞬间的位置，也就是幽灵球的圆心。
  x: number
  y: number
  // 母球球心走过的距离，单位米。
  distance: number
  // kind 为 ball 时是被撞的球号，否则为 null。
  targetId: string | null
}

// phiFromScreenVector 把屏幕上的位移方向换成 pooltool 的 phi。
//
// 屏幕 y 向下、台面 y 向远端，两者反向，所以第二个参数要取负。
// 传全 0 时返回 0：没有方向就当成朝右，调用方本来也不该在没拖动时算角度。
export function phiFromScreenVector(dx: number, dy: number): number {
  if (dx === 0 && dy === 0) {
    return 0
  }
  return normalizePhi(Math.atan2(-dy, dx))
}

// normalizePhi 把角度收进 [0, 2π)。
// 不做归一化的话，微调累加几十次之后 phi 会漂到几百度，
// 传回服务端虽然物理结果一样，但日志与回放里看不懂。
export function normalizePhi(phi: number): number {
  const twoPi = Math.PI * 2
  const wrapped = phi % twoPi
  return wrapped < 0 ? wrapped + twoPi : wrapped
}

// adjustPhi 按度数微调方位角，返回归一化后的新值。
// 底部微调尺每次 ±0.1°，正负两个方向都要能走。
export function adjustPhi(phi: number, deltaDeg: number): number {
  return normalizePhi(phi + (deltaDeg * Math.PI) / 180)
}

// 拖动一像素转多少度。取 0.25 是想让手机上一整屏宽（约 360px）刚好转 90°：
// 太灵敏手指抖一下就偏，太迟钝要拖好几次才能绕一圈。
export const AIM_DEG_PER_PX = 0.25

// rotateByDrag 把一次水平拖动换算成新的 phi。
//
// 只吃水平位移。斜着拖时竖直分量不参与，否则手指往下滑一点，角度会莫名其妙地变，
// 而竖屏手机上往下滑是很自然的动作（想把手指移到更好发力的位置）。
//
// 方向：往右拖，瞄准线顺时针转，也就是屏幕上的角度变小；
// 而 phi 增大对应屏幕逆时针，所以这里是减号。这和「用手推转盘」的直觉一致。
export function rotateByDrag(phi: number, dxPx: number, degPerPx = AIM_DEG_PER_PX): number {
  return normalizePhi(phi - (dxPx * degPerPx * Math.PI) / 180)
}

// screenDirectionOf 给出 phi 对应的屏幕单位向量，供画球杆与瞄准线使用。
//
// 复用这里的换算而不是让渲染自己算 cos/sin：屏幕 y 向下、phi 的 y 向远端，
// 两处各写一遍必然有一处漏掉取负，表现为「线画在左边、球却往右边走」。
export function screenDirectionOf(phi: number): { x: number; y: number } {
  return { x: Math.cos(phi), y: -Math.sin(phi) }
}

// aimFromCue 算母球沿 phi 前进时先撞到什么。
//
// balls 里应当包含母球自己，函数按 id === 'cue' 找它；
// 找不到就返回一条到库边的直线，避免调用方先判空再算。
export function aimFromCue(
  balls: ReadonlyArray<BallState>,
  phi: number,
  cueId = 'cue',
): AimHit {
  const cue = findBall(balls, cueId)
  const dx = Math.cos(phi)
  const dy = Math.sin(phi)

  if (!cue) {
    return { kind: 'cushion', x: 0, y: 0, distance: 0, targetId: null }
  }

  // 先算到库边的距离，作为「至少能走这么远」的基准。
  let best = cushionHit(cue.x, cue.y, dx, dy)

  // 再逐颗球求交，取更近的那个。已经落袋的球不参与，否则会瞄准一颗看不见的球。
  for (const ball of balls) {
    if (ball.id === cueId || !ball.on_table) {
      continue
    }
    const t = rayCircleDistance(cue.x, cue.y, dx, dy, ball.x, ball.y, CONTACT_DISTANCE)
    if (t !== null && t < best.distance) {
      best = {
        kind: 'ball',
        x: cue.x + dx * t,
        y: cue.y + dy * t,
        distance: t,
        targetId: ball.id,
      }
    }
  }

  return best
}

// findBall 按 id 找一颗还在台面上的球。
function findBall(balls: ReadonlyArray<BallState>, id: string): BallState | null {
  for (const ball of balls) {
    if (ball.id === id) {
      return ball.on_table ? ball : null
    }
  }
  return null
}

// rayCircleDistance 求射线与圆的首个交点距离。
//
// 射线从圆心内部出发（母球和别的球不该重叠，但快照错位时可能发生）没有正根，
// 这时返回 null 而不是负数，调用方会自然地退回到库边。
function rayCircleDistance(
  ox: number,
  oy: number,
  dx: number,
  dy: number,
  cx: number,
  cy: number,
  radius: number,
): number | null {
  const fx = ox - cx
  const fy = oy - cy
  const b = 2 * (fx * dx + fy * dy)
  const c = fx * fx + fy * fy - radius * radius
  const disc = b * b - 4 * c
  if (disc < 0) {
    return null
  }
  const root = Math.sqrt(disc)
  // 两个根里先到的那个：(-b - root) / 2。
  const near = (-b - root) / 2
  if (near > 1e-9) {
    return near
  }
  const far = (-b + root) / 2
  if (far > 1e-9) {
    return far
  }
  return null
}

// cushionHit 求母球中心撞到台面内沿（已内缩一个球半径）的距离。
//
// 四个方向各解一次，取最小的正 t。母球贴库时要打向台面外，
// 这时该方向解出的 t 接近 0，瞄准线长度也就是 0，是正确表现。
function cushionHit(ox: number, oy: number, dx: number, dy: number): AimHit {
  const minX = BALL_RADIUS
  const maxX = TABLE_WIDTH - BALL_RADIUS
  const minY = BALL_RADIUS
  const maxY = TABLE_LENGTH - BALL_RADIUS

  let best = Number.POSITIVE_INFINITY

  if (dx > 1e-9) {
    best = Math.min(best, (maxX - ox) / dx)
  } else if (dx < -1e-9) {
    best = Math.min(best, (minX - ox) / dx)
  }
  if (dy > 1e-9) {
    best = Math.min(best, (maxY - oy) / dy)
  } else if (dy < -1e-9) {
    best = Math.min(best, (minY - oy) / dy)
  }

  // 朝正 x / 正 y 时上面算的是正值；朝负方向时分子为负、分母为负，结果仍是正的。
  // 全都为负说明母球已经在库边外面（数据异常），按 0 处理，别把负距离传出去。
  const distance = Number.isFinite(best) && best > 0 ? best : 0
  return {
    kind: 'cushion',
    x: ox + dx * distance,
    y: oy + dy * distance,
    distance,
    targetId: null,
  }
}
