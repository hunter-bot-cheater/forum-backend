// 瞄准计算：从母球沿 phi 发一条射线，找出最先撞到的球或库边。
//
// 只做几何，不碰 canvas。渲染拿这里的结果去画瞄准线与幽灵球，
// 命中判定也复用它，两条路径共用一套数学，不会出现「线画在球上、点下去却没碰」。
//
// phi 的约定沿用 pooltool：速度方向 = (cos phi, sin phi)，phi=0 指向 +x，
// phi=π/2 指向 +y（台面远端）。屏幕的 y 轴朝下，所以由屏幕位移反推 phi 时要取负。
import type { Point } from './geometry'
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

// 引导线的长度上限，单位米。
//
// 0.42 米差不多是七个球直径：再短看不出「球往哪走」，再长会把台面糊成一片虚线，
// 而且越过后面那颗球之后指向就没有意义了。真正生效的长度还要被库边截断，
// 以及按击球厚度打折（见 aimGuides）。
export const OBJECT_GUIDE_LENGTH = 0.42
export const CUE_GUIDE_LENGTH = 0.3

// 太薄的球目标球几乎不动、正碰时母球几乎不动，
// 这两种情况画出来的线段只有几个像素，看着像脏点，不如不画。
const MIN_OBJECT_RATIO = 0.08
const MIN_CUE_RATIO = 0.06

// GuideSegment 是一段引导线，两端都是世界坐标。
export interface GuideSegment {
  from: Point
  to: Point
}

// AimGuides 是瞄准时除了母球路线之外的两段预测线。
//
// object 是目标球被撞之后的去向（沿两球球心连线），
// cue 是母球撞完之后分离的方向。
// 撞库时两者都是 null：库边反弹的入射角等于反射角，但真正的落点还要看旋转，
// 画一条不准的线比不画更容易误导。
export interface AimGuides {
  object: GuideSegment | null
  cue: GuideSegment | null
}

// aimGuides 按一次瞄准结果算出两段引导线。
//
// 两条线的方向与长度都由「打得多厚」决定。设 n 是球心连线方向（幽灵球心
// 指向目标球心）、v 是来球方向，完全弹性、等质量、不带走位时：
//
//   目标球速度 = v·cosθ，方向沿 n
//   母球速度   = v·sinθ，方向沿切向
//
// θ 是 v 与 n 的夹角。正碰（θ=0）时母球停住、目标球拿满速；
// 越薄母球越接近原速、目标球越慢。所以：
//   - 正碰时母球那条线不画（它根本不动），目标球那条最长；
//   - 擦边时反过来，目标球那条很短、母球那条接近原方向。
//
// 以前不管什么厚度都画一根等长的 90 度线，正碰时也画，是纯误导。
export function aimGuides(
  balls: ReadonlyArray<BallState>,
  phi: number,
  hit: AimHit,
): AimGuides {
  if (hit.kind !== 'ball' || !hit.targetId) {
    return { object: null, cue: null }
  }
  const target = findBall(balls, hit.targetId)
  if (!target) {
    return { object: null, cue: null }
  }

  // 球心连线：幽灵球心指向目标球心，就是目标球被撞出去的方向。
  const gapX = target.x - hit.x
  const gapY = target.y - hit.y
  const gap = Math.hypot(gapX, gapY)
  if (gap < 1e-9) {
    return { object: null, cue: null }
  }
  const nx = gapX / gap
  const ny = gapY / gap

  // 来球方向与球心连线的夹角：cos 决定目标球拿多少速度，sin 决定母球剩多少。
  const vx = Math.cos(phi)
  const vy = Math.sin(phi)
  const cosTheta = Math.max(0, vx * nx + vy * ny)
  const sinTheta = Math.abs(vx * ny - vy * nx)

  // 目标球从自己的表面出发，走到库边或长度上限为止；长度按 cosθ 打折。
  let object: GuideSegment | null = null
  if (cosTheta > MIN_OBJECT_RATIO) {
    const objectFrom = { x: target.x + nx * BALL_RADIUS, y: target.y + ny * BALL_RADIUS }
    const objectReach = Math.min(
      OBJECT_GUIDE_LENGTH * cosTheta,
      cushionHit(target.x, target.y, nx, ny).distance,
    )
    object = {
      from: objectFrom,
      to: { x: objectFrom.x + nx * objectReach, y: objectFrom.y + ny * objectReach },
    }
  }

  // 母球撞完沿切线走。切线有两个方向，取与来球同侧的那一支：
  // 反了会把「往左偏」画成「往右偏」，比不画还糟。
  let cue: GuideSegment | null = null
  if (sinTheta > MIN_CUE_RATIO) {
    let px = -ny
    let py = nx
    if (px * vx + py * vy < 0) {
      px = -px
      py = -py
    }
    const cueReach = Math.min(CUE_GUIDE_LENGTH * sinTheta, cushionHit(hit.x, hit.y, px, py).distance)
    cue = {
      from: { x: hit.x + px * BALL_RADIUS, y: hit.y + py * BALL_RADIUS },
      to: { x: hit.x + px * cueReach, y: hit.y + py * cueReach },
    }
  }

  return { object, cue }
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
