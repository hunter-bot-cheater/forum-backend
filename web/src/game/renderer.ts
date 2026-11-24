// 台面渲染。只负责「把一份球位画到 canvas 上」，不持有状态、不监听事件。
//
// 分层顺序是固定的：木框 → 库边 → 台呢 → 袋口 → 瞄准线 → 球影 → 球。
// 顺序错一层就会出现「球被库边压住」或者「袋口被台呢盖住」这种一眼假的画面，
// 所以整帧一次画完，不做局部重绘——16 颗球的完整重绘在手机上也只有 1ms 左右。
import type { AimGuides } from './aim'
import { ballTexture, CUE_DOT_COLOR, CUE_DOT_SCALE } from './balls'
import { fitTable, metersToPx, worldToScreen, type ViewTransform, type Viewport } from './geometry'
import {
  BALL_RADIUS,
  CUE_HEAD_STRING,
  CUSHION_WIDTH,
  POCKET_MOUTHS,
  POCKETS,
  RAIL_WIDTH,
  TABLE_LENGTH,
  TABLE_WIDTH,
  type Pocket,
} from './table'
import type { BallState } from './types'

// 从台面内沿到最外层木框边缘的距离，与 geometry 里算缩放时用的是同一个数。
const MARGIN = CUSHION_WIDTH + RAIL_WIDTH

// 四个角的圆角半径（CSS 像素）。木框有圆角像一张真台子，直角会像色块。
const RAIL_RADIUS = 12

// 台面四周至少留这么多像素。木框的投影会往外扩几像素，留 12 才不会被裁。
export const TABLE_PADDING = 12

// 跳球时影子与球体的变化幅度。2D 里能表达的腾空线索只有这两条，
// 系数都压得比较小：放大过头会像换了一颗大球，而不是球飞起来了。
const SHADOW_SPREAD = 0.4
const SHADOW_FADE = 4
const BALL_GROWTH = 0.6

// 整帧用到的颜色。取值照着标准图（微信小游戏那张截图）取：外框是偏冷的
// 中深灰、台呢是深草绿并在中间有一块受光、袋口是纯黑的洞。
//
// 台呢取 (12,116,72)：标准图台面中央是 (14,110,72)、中上段是 (5,127,78)，
// 两张图放在一起看，台呢的红通道几乎为零、绿通道 110~130。早先提到 (24,122,78)
// 是为了让球更跳，结果整张台子偏青、比标准图亮一档，并排一眼就能看出不是同一张。
const COLORS = {
  rail: '#35383c',
  // 木框框面的受光两端。整块单色是「塑料感」最大来源：这么大一片面积上
  // 没有任何明暗变化，看起来就是一张压平的卡纸。上面这档放在左上，
  // 下面这档放在右下，一条斜向渐变就足够把框面撑起来。
  railLit: '#454a50',
  railDark: '#24272b',
  cloth: '#0c7448',
  clothLight: '#148050',
  // 木框上的颗星：图里那排白色小三角，没有它外框像一块塑料板。
  diamond: 'rgba(240, 244, 250, 0.8)',
  shadow: 'rgba(0, 0, 0, 0.26)',
  clothLine: 'rgba(255, 255, 255, 0.08)',
}

// 库边那圈斜坡的四档颜色。
//
// 标准图里库边的剖面是一条受光的斜坡：贴着台呢那一侧被灯照到，最亮也最绿；
// 往木框方向一路压暗到近黑。四档是按像素取的值（贴布处 (25,140,95)、
// 木框边 (22,43,36)）。
//
// 第一档不能取标准图里那条最亮的 (7,154,100)：那一条只有一两个像素宽，
// 是布折上来时的一道反光，整片照搬会让库边比台呢亮出一大截，围成一圈荧光边。
const CUSHION_FACE = '#128c5f'
const CUSHION_LIT = '#17714f'
const CUSHION_MID = '#1b4634'
const CUSHION_DARK = '#1a2420'

// 球按物理尺寸画，不做视觉放大。
//
// 早先为了让手机上的球大一点，这里乘过 1.2，结果相切的球在画面上互相压掉
// 17%——物理算得再准，玩家看到的都是「球穿在一起了」。现在改成直接把球本身
// 做大（table.ts 的 BALL_RADIUS 是标准球的 1.2 倍），摆球的圆心距跟着变大，
// 屏幕上同样大，但相切关系是对的。
//
// 所以这个系数固定为 1：凡是半径相关的绘制（球、虚影、幽灵球、球杆）
// 都走 ballRadiusPx，谁也不要再乘一个视觉倍数。
export const BALL_DRAW_SCALE = 1

// ballRadiusPx 是球在屏幕上画出来的半径（CSS 像素）。
// 画球、画虚影、画幽灵球、画球杆都必须用它，否则杆头会离球差出一截。
function ballRadiusPx(t: ViewTransform): number {
  return metersToPx(t, BALL_RADIUS) * BALL_DRAW_SCALE
}

// AimLayer 是瞄准时的辅助图形，坐标全部是世界坐标。
// 穿 worldToScreen 之后再画，与球位用同一套变换，窗口缩放时线不会和球错开。
export interface AimLayer {
  // 母球球心
  cue: { x: number; y: number }
  // 母球球心的终点（命中点），kind 决定要不要画幽灵球
  hit: { x: number; y: number; kind: 'ball' | 'cushion' }
  // 击球方向（弧度，与出杆参数里的 phi 同一个）。球杆沿它的反方向伸出去：
  // 杆子朝左后方，球就往右前方走，这是「杆子的反方向」唯一说得通的定义。
  phi: number
  // 0..1 的力度。杆子跟着往后拉，这是力度条在球桌上那一眼。
  power: number
  // locked 表示方向已经定住（点过台面），球杆换成暖色。
  locked: boolean
  // guides 是目标球与母球撞完之后的去向。撞库时为 null，见 aim.ts 的 aimGuides。
  guides?: AimGuides | null
  // forbidden 表示这条线最先碰到的是对手的球（或还没轮到的 8 号）。
  //
  // 打开时不画幻影球与去向引导线——那些线是在告诉玩家「打这里会怎样」，
  // 而这一杆本身就不该打；改成在目标球上盖一个禁止符号，一次说明问题。
  forbidden?: boolean
}

// Scene 是一帧要画的全部内容。以后加球杆、击球点提示也放这里，
// 保持「渲染只读输入」这条边界。
export interface Scene {
  balls: ReadonlyArray<BallState>
  aim?: AimLayer | null
  // peerAim 是对面那一方此刻的瞄准层。画得淡一点、压在自己的瞄准层下面：
  // 两根杆子同时出现时，一眼能分出哪根是自己的。
  peerAim?: AimLayer | null
  // ghost 是手中球（自由球）的预览位置，世界坐标。只画半透明的虚影，
  // 因为它还没真的落位——落位由服务端确认，之后才作为一颗正常的球进 balls。
  ghost?: { x: number; y: number } | null
  // headString 为 true 时在台面上画开球线。只在开球摆球时打开：
  // 那条线是「母球能摆到哪」的边界，平时画着只是干扰。
  headString?: boolean
}

// drawScene 画完整一帧。view 是 CSS 像素尺寸，dpr 只用来取高清球贴图。
//
// 调用方负责先把 ctx 按 dpr 缩放好（PoolTable.vue 里做），
// 这里全部按 CSS 像素算，避免两处都乘 dpr 导致画面放大四倍。
//
// 对局页不用这个函数：它把台面静态层缓存起来，每帧只调 drawActors。
// 留着它是给「一次性画完」的场景（练习页首帧、测试）。
export function drawScene(
  ctx: CanvasRenderingContext2D,
  view: Viewport,
  scene: Scene,
  dpr: number,
): void {
  ctx.clearRect(0, 0, view.width, view.height)
  drawTable(ctx, view)
  drawActors(ctx, view, scene, dpr)
}

// drawTable 只画不动的部分：木框、库边、台呢、六个袋口。
//
// 这一层每帧重画是白费力气：一帧里它一个像素都不会变，
// 而它偏偏是最贵的那部分（台呢两层全屏渐变、六个袋口各自多层填充加裁剪）。
// 拖动瞄准时每帧三十毫秒里有二十多毫秒耗在这里，手感就是「拖杆发涩」。
export function drawTable(ctx: CanvasRenderingContext2D, view: Viewport): void {
  const t = fitTable(view, TABLE_PADDING)
  drawRail(ctx, t)
  drawCushion(ctx, t)
  drawCloth(ctx, t)
  drawPockets(ctx, t)
  // 外框那条亮棱最后画：护角在四角会一直铺到台子外沿，棱要压在它上面，
  // 才是「一圈金属包边绕着整张台子」的样子。顺序反过来，四个角就断了。
  drawRailEdge(ctx, t)
}

// drawActors 画每帧会动的东西：两根杆与瞄准线、球、落袋动画、手中球虚影。
// 必须在 drawTable 之后调用，球要压在这些之上。
export function drawActors(
  ctx: CanvasRenderingContext2D,
  view: Viewport,
  scene: Scene,
  dpr: number,
): void {
  const t = fitTable(view, TABLE_PADDING)
  if (scene.peerAim) {
    // 对面的杆子画得淡一档，并且压在自己那一层下面：
    // 两根杆子同时出现时，一眼分得出哪根是自己的。
    ctx.save()
    ctx.globalAlpha = 0.62
    drawAim(ctx, t, scene.peerAim)
    if (scene.peerAim.guides) {
      drawGuides(ctx, t, scene.peerAim.guides)
    }
    drawCue(ctx, t, scene.peerAim)
    ctx.restore()
  }
  if (scene.aim) {
    drawAim(ctx, t, scene.aim)
    // 不该打的球不给去向引导线：那两条线是在说「打这里会进」，
    // 而这一杆先碰到对手的球就已经犯规了。
    if (scene.aim.guides && !scene.aim.forbidden) {
      drawGuides(ctx, t, scene.aim.guides)
    }
    drawCue(ctx, t, scene.aim)
  }
  // 开球线画在球下面：它是一条地面上的标线，不该压过任何一颗球。
  if (scene.headString) {
    drawHeadString(ctx, t)
  }
  trackPotted(scene.balls)
  drawBalls(ctx, t, scene.balls, dpr)
  // 刚落袋的球再画一小段「滚进袋口」：帧里它直接消失太生硬，
  // 而且看不出是从哪个袋进去的。
  drawSinking(ctx, t, ballRadiusPx(t), dpr)
  // 虚影画在真球之上：拖动时它会压过别的球，被压住就看不出落在哪了。
  if (scene.ghost) {
    drawGhostBall(ctx, t, scene.ghost)
  }
}

// drawHeadString 画开球线：横贯台面的一条白色虚线。
//
// 开球摆母球时它是「能摆到哪」的唯一提示。不画的话玩家只能靠试：
// 摆过线被服务端拒一次，才知道线在哪。用虚线而不是实线，是因为它
// 不是库边那类实体边界，只是一条画在台呢上的标线。
function drawHeadString(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const from = worldToScreen(t, 0, CUE_HEAD_STRING)
  const to = worldToScreen(t, TABLE_WIDTH, CUE_HEAD_STRING)

  ctx.save()
  ctx.setLineDash([6, 6])
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.32)'
  ctx.lineWidth = 1.5
  ctx.beginPath()
  ctx.moveTo(from.x, from.y)
  ctx.lineTo(to.x, to.y)
  ctx.stroke()
  ctx.restore()
}

// drawGhostBall 画手中球的水印（自由球还没落位时跟着指针走的那颗白球）。
//
// 用半透明的白球加一圈虚线，而不是画一颗实心球：实心球看起来已经落定了，
// 玩家会以为不能再挪。虚线是主流台球游戏通用的「这东西还能拖」的暗示。
function drawGhostBall(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  ghost: { x: number; y: number },
): void {
  const center = worldToScreen(t, ghost.x, ghost.y)
  const radiusPx = ballRadiusPx(t)

  ctx.save()

  // 球体：中间偏左上留一块亮斑，和画真球用的是同一个光源方向。
  const body = ctx.createRadialGradient(
    center.x - radiusPx * 0.32,
    center.y - radiusPx * 0.32,
    radiusPx * 0.12,
    center.x,
    center.y,
    radiusPx,
  )
  body.addColorStop(0, 'rgba(255, 255, 255, 0.92)')
  body.addColorStop(0.72, 'rgba(238, 243, 249, 0.5)')
  body.addColorStop(1, 'rgba(206, 216, 228, 0.34)')
  ctx.beginPath()
  ctx.arc(center.x, center.y, radiusPx, 0, Math.PI * 2)
  ctx.fillStyle = body
  ctx.fill()

  // 一圈细实线把球的轮廓勾出来，纯靠透明度在绿呢上容易糊成一团光斑。
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.75)'
  ctx.lineWidth = 1
  ctx.stroke()

  // 外围虚线：半径取 1.55 个球，正好套在球身外面不压住轮廓。
  ctx.beginPath()
  ctx.arc(center.x, center.y, radiusPx * 1.55, 0, Math.PI * 2)
  ctx.setLineDash([4, 4])
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.62)'
  ctx.lineWidth = 1.4
  ctx.stroke()
  ctx.setLineDash([])

  ctx.restore()
}

// Rect 是屏幕上的一个矩形，由两个世界坐标点换算而来。
interface Rect {
  x: number
  y: number
  width: number
  height: number
}

// worldRect 用两个世界坐标点框出一块屏幕矩形。
//
// 统一走 worldToScreen 再取 min / abs：y 轴是翻转的，直接拿两个点相减
// 会得到负的宽高，后面 fillRect 就什么都不画了。
function worldRect(t: ViewTransform, x1: number, y1: number, x2: number, y2: number): Rect {
  const a = worldToScreen(t, x1, y1)
  const b = worldToScreen(t, x2, y2)
  return {
    x: Math.min(a.x, b.x),
    y: Math.min(a.y, b.y),
    width: Math.abs(b.x - a.x),
    height: Math.abs(b.y - a.y),
  }
}

// railRect 是最外层木框的屏幕矩形，袋口裁剪与木框绘制都用它。
function railRect(t: ViewTransform): Rect {
  return worldRect(
    t,
    -MARGIN,
    -MARGIN,
    TABLE_WIDTH + MARGIN,
    TABLE_LENGTH + MARGIN,
  )
}

// drawRail 画外框的底面与库边上的菱形瞄准点。最外那条亮棱见 drawRailEdge。
function drawRail(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const rect = railRect(t)

  // 底色改成斜向渐变。台灯在正上方偏左，框面的左上角最亮、右下角最暗；
  // 一条 135° 的线性渐变就够，中间那档仍然落在 COLORS.rail 上，
  // 整张台子的明度与之前一致，只是不再一片死灰。
  ctx.save()
  ctx.shadowColor = 'rgba(0, 0, 0, 0.55)'
  ctx.shadowBlur = 18
  ctx.shadowOffsetY = 7
  const base = ctx.createLinearGradient(rect.x, rect.y, rect.x + rect.width, rect.y + rect.height)
  base.addColorStop(0, COLORS.railLit)
  base.addColorStop(0.45, COLORS.rail)
  base.addColorStop(1, COLORS.railDark)
  ctx.fillStyle = base
  roundRectPath(ctx, rect.x, rect.y, rect.width, rect.height, RAIL_RADIUS)
  ctx.fill()
  ctx.restore()

  // 框面靠库边那一圈压暗：库边是立起来的斜面，会把光挡住，
  // 紧挨着它的框面永远是暗的。少了这一圈，框就像贴在布上的一张纸——
  // 亮部只出现在外沿，往里突然变平，就没有「框是抬起来的一层」的感觉。
  //
  // 描边位置取 RAIL_WIDTH：那正是库边的外沿。描边有一半落在库边底下，
  // 后面 drawCushion 会盖掉，框上只剩贴着库边的一窄条暗边。
  ctx.save()
  ctx.strokeStyle = 'rgba(0, 0, 0, 0.34)'
  ctx.lineWidth = Math.max(4, metersToPx(t, RAIL_WIDTH) * 0.5)
  roundRectPath(
    ctx,
    rect.x + metersToPx(t, RAIL_WIDTH),
    rect.y + metersToPx(t, RAIL_WIDTH),
    rect.width - metersToPx(t, RAIL_WIDTH) * 2,
    rect.height - metersToPx(t, RAIL_WIDTH) * 2,
    Math.max(1, RAIL_RADIUS - metersToPx(t, RAIL_WIDTH)),
  )
  ctx.stroke()

  // 框面外沿再压一道很淡的内阴影，让「高出来的一层」有个厚度。
  // 它比上面那道宽、也更淡，两道叠起来才有圆柱倒角的过渡，
  // 只用一道硬边会像画了个黑框。
  ctx.strokeStyle = 'rgba(0, 0, 0, 0.16)'
  ctx.lineWidth = Math.max(2, metersToPx(t, RAIL_WIDTH) * 0.9)
  roundRectPath(ctx, rect.x + 2, rect.y + 2, rect.width - 4, rect.height - 4, Math.max(1, RAIL_RADIUS - 2))
  ctx.stroke()
  ctx.restore()

  drawRailDiamonds(ctx, t)
}

// drawRailEdge 画外框最外面那条亮棱：三遍描边叠出倒角面（宽而淡的一遍当过渡、
// 中间一遍当倒角、最窄的一遍当受光的那条棱）。硬边会像贴了一条白胶带。
//
// 亮棱必须贴着最外沿：标准图里木框的受光只在外面那一两像素上，往里全是暗的。
// 早先把它放进 6px 处，木框里就多出一圈浅灰的描边，像木框套了个内衬。
//
// 它必须在 drawPockets 之后画：四个角的护角会一直铺到台子外沿，
// 棱压在护角上，一圈包边才不断。
function drawRailEdge(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const rect = railRect(t)
  const edge = (inset: number, width: number, color: string) => {
    ctx.strokeStyle = color
    ctx.lineWidth = width
    roundRectPath(
      ctx,
      rect.x + inset,
      rect.y + inset,
      rect.width - inset * 2,
      rect.height - inset * 2,
      Math.max(1, RAIL_RADIUS - inset),
    )
    ctx.stroke()
  }
  edge(3.6, 3.2, 'rgba(186, 192, 199, 0.06)')
  edge(1.8, 1.6, 'rgba(200, 206, 213, 0.14)')
  edge(1.1, 1.1, 'rgba(212, 218, 224, 0.30)')
}

// drawRailDiamonds 画木框上那排白色颗星。
//
// 位置按真实台面的分点来：长库各 6 个（中袋那个位置空着），短库各 3 个。
// 它们除了好看还有一个用处——瞄准时能拿它当角度参考。
//
// 三个细节照着标准图：
//   · 落在木框宽度的中间，不是库边上——库边是一条受光的斜坡，压个白点会被吃掉；
//   · 形状是朝台内的小三角，不是菱形；
//   · 尺寸按木框厚度取，不用 MARGIN 算（那样会大到像贴了一张标签）。
function drawRailDiamonds(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const size = Math.max(2.2, metersToPx(t, RAIL_WIDTH) * 0.17)
  const inset = CUSHION_WIDTH + RAIL_WIDTH / 2

  // 每一项带上「朝台内」的单位方向，三角形的尖就朝这个方向。
  const spots: Array<{ x: number; y: number; dx: number; dy: number }> = []
  for (let i = 1; i <= 7; i += 1) {
    if (i === 4) {
      continue
    }
    const y = (TABLE_LENGTH * i) / 8
    spots.push({ x: -inset, y, dx: 1, dy: 0 })
    spots.push({ x: TABLE_WIDTH + inset, y, dx: -1, dy: 0 })
  }
  for (let i = 1; i <= 3; i += 1) {
    const x = (TABLE_WIDTH * i) / 4
    spots.push({ x, y: -inset, dx: 0, dy: 1 })
    spots.push({ x, y: TABLE_LENGTH + inset, dx: 0, dy: -1 })
  }

  ctx.fillStyle = COLORS.diamond
  for (const spot of spots) {
    const center = worldToScreen(t, spot.x, spot.y)
    // 世界坐标的 y 朝远端，屏幕的 y 朝下，方向要跟着翻一次符号。
    const fx = spot.dx
    const fy = -spot.dy
    // 垂直于朝向的横向单位向量，用来撑开三角形的底边。
    const px = -fy
    const py = fx

    ctx.beginPath()
    ctx.moveTo(center.x + fx * size, center.y + fy * size)
    ctx.lineTo(center.x - fx * size * 0.55 + px * size * 0.62, center.y - fy * size * 0.55 + py * size * 0.62)
    ctx.lineTo(center.x - fx * size * 0.55 - px * size * 0.62, center.y - fy * size * 0.55 - py * size * 0.62)
    ctx.closePath()
    ctx.fill()
  }
}

// drawCushion 画库边，范围是台呢外沿再加一圈 CUSHION_WIDTH。
//
// 四条边各画一条渐变带，不是铺一整块纯色：标准图里库边是一圈受光的斜坡，
// 贴台呢那一侧最亮、往木框方向压暗到近黑。纯色画出来像一根塑料条，
// 而这条斜坡正是「台呢折上来包住橡胶」的那一折。
//
// 整组要裁在「库边外沿 + 六处袋口缺口」这条轮廓里。四条带在四个角上本来会
// 互相盖住，角上留下一块谁也说不清是什么颜色的三角——而角上正是袋口斜边
// 该待的位置。裁掉之后，斜坡在斜边处自然收口，斜边由 drawPockets 的
// 金属护角与暗绿端面接手。
function drawCushion(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const w = CUSHION_WIDTH

  // band 画一条库边带。rect 是世界坐标的矩形，(sx,sy)→(ex,ey) 是渐变轴，
  // 从库边脸指向木框：起点是贴布那头（亮），终点是木框那头（暗）。
  const band = (
    x1: number,
    y1: number,
    x2: number,
    y2: number,
    sx: number,
    sy: number,
    ex: number,
    ey: number,
  ) => {
    const rect = worldRect(t, x1, y1, x2, y2)
    const from = worldToScreen(t, sx, sy)
    const to = worldToScreen(t, ex, ey)
    const ramp = ctx.createLinearGradient(from.x, from.y, to.x, to.y)
    ramp.addColorStop(0, CUSHION_FACE)
    // 受光那一档要占到一半：标准图里库边亮的那半圈有 25mm 宽，
    // 只亮 30% 的话整圈库边看着比标准图细一半。
    ramp.addColorStop(0.48, CUSHION_LIT)
    ramp.addColorStop(0.74, CUSHION_MID)
    ramp.addColorStop(1, CUSHION_DARK)
    ctx.fillStyle = ramp
    ctx.fillRect(rect.x, rect.y, rect.width, rect.height)
  }

  ctx.save()
  ctx.beginPath()
  // 中袋缺口不挖：布铺到洞沿，与真实台桌一致（见 addGreenOutline 的说明）。
  addGreenOutline(ctx, t, true)
  ctx.clip('evenodd')

  // 上、下两条横跨整宽（含两侧的角），左右两条只画中间那截。
  band(-w, -w, TABLE_WIDTH + w, 0, 0, 0, 0, -w)
  band(-w, TABLE_LENGTH, TABLE_WIDTH + w, TABLE_LENGTH + w, 0, TABLE_LENGTH, 0, TABLE_LENGTH + w)
  band(-w, 0, 0, TABLE_LENGTH, 0, 0, -w, 0)
  band(TABLE_WIDTH, 0, TABLE_WIDTH + w, TABLE_LENGTH, TABLE_WIDTH, 0, TABLE_WIDTH + w, 0)

  ctx.restore()
}

// clothGrain 生成台呢的织物颗粒瓦片。
//
// 纯渐变的台呢看着就是一块塑料绿板：真实的布有极细的经纬颗粒，
// 受光时碎成一片细密的亮点。这里用 4×4 的瓦片铺满，颗粒本身只有 1 像素，
// 手机上看起来是「布有质感」而不是「布上画了花纹」。
//
// 用的是固定序列而不是 Math.random：同一张台子每次打开长得一样，
// 不会出现「刷新一下布就换了纹理」的错觉。
let grainPattern: CanvasPattern | null = null

function clothGrain(ctx: CanvasRenderingContext2D): CanvasPattern | null {
  if (grainPattern) {
    return grainPattern
  }
  const size = 4
  const tile = document.createElement('canvas')
  tile.width = size
  tile.height = size
  const pen = tile.getContext('2d')
  if (!pen) {
    return null
  }
  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      const value = ((x * 7 + y * 13 + x * y * 3) % 16) / 16
      if (value > 0.5) {
        pen.fillStyle = 'rgba(255, 255, 255, ' + (0.05 + value * 0.06).toFixed(3) + ')'
      } else {
        pen.fillStyle = 'rgba(0, 0, 0, ' + (0.04 + (0.5 - value) * 0.2).toFixed(3) + ')'
      }
      pen.fillRect(x, y, 1, 1)
    }
  }
  grainPattern = ctx.createPattern(tile, 'repeat')
  return grainPattern
}

// drawCloth 画台呢。矩形范围是库边内侧，也就是世界坐标 0..宽、0..长。
function drawCloth(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const rect = worldRect(t, 0, 0, TABLE_WIDTH, TABLE_LENGTH)
  // 台呢要按 clothPath 裁一遍：六处袋口的缺口里没有布。裁剪必须在填色之外
  // 留一层 save/restore——后面还要画开球线与置球点，它们也落在这块布上。
  ctx.save()
  clothPath(ctx, t)
  ctx.clip('evenodd')

  ctx.fillStyle = COLORS.cloth
  ctx.fillRect(rect.x, rect.y, rect.width, rect.height)

  // 织物颗粒。放在底色之上、光斑之下：亮处能看见细碎的高光，
  // 暗处也不会被下一层的暗角吃掉。
  const grain = clothGrain(ctx)
  if (grain) {
    ctx.fillStyle = grain
    ctx.fillRect(rect.x, rect.y, rect.width, rect.height)
  }

  // 中间偏上打一块亮，边缘压暗。整片纯色会像一块绿色卡纸，
  // 有一点受光才有「这张台子铺了布、灯在上面」的感觉。
  //
  // 灯罩是长条形，打在台面上是一块横向的椭圆而不是正圆：
  // 正圆的亮斑会让台面看起来像个倒扣的碗。
  ctx.save()
  ctx.translate(rect.x + rect.width * 0.5, rect.y + rect.height * 0.46)
  ctx.scale(0.72, 1)
  const light = ctx.createRadialGradient(0, 0, 0, 0, 0, Math.max(rect.width, rect.height) * 0.62)
  light.addColorStop(0, COLORS.clothLight)
  light.addColorStop(0.42, 'rgba(20, 138, 88, 0.34)')
  light.addColorStop(1, 'rgba(20, 138, 88, 0)')
  ctx.fillStyle = light
  ctx.fillRect(-rect.width, -rect.height, rect.width * 2, rect.height * 2)
  ctx.restore()

  // 四周压一圈暗角，球台才有纵深，不会像一张平铺的纸。
  const vignette = ctx.createRadialGradient(
    rect.x + rect.width * 0.5,
    rect.y + rect.height * 0.5,
    Math.min(rect.width, rect.height) * 0.35,
    rect.x + rect.width * 0.5,
    rect.y + rect.height * 0.5,
    Math.max(rect.width, rect.height) * 0.72,
  )
  vignette.addColorStop(0, 'rgba(0, 0, 0, 0)')
  vignette.addColorStop(1, 'rgba(0, 0, 0, 0.18)')
  ctx.fillStyle = vignette
  ctx.fillRect(rect.x, rect.y, rect.width, rect.height)

  // 库边压在台呢上投下的一圈暗影。
  //
  // 标准图里贴着库边那一段明显更暗，而且是「先深一段、再慢慢回来」：
  // 沿左库横向量，洞沿 (0,79,46) → 往里 15mm (1,74,45) → 40mm (8,89,59)
  // → 80mm (8,102,66)，台面中央才到 (14,110,72)。少了这一圈，台呢会像
  // 一张平铺的纸，库边像贴上去的一条。
  //
  // 一条渐变做不出「先平后降」的形，用两条叠：近的一窄条压得深，
  // 远的一宽带慢慢淡出去，叠出来的剖面才和标准图对得上。
  const shadeBand = (
    x1: number,
    y1: number,
    x2: number,
    y2: number,
    sx: number,
    sy: number,
    ex: number,
    ey: number,
    alpha: number,
  ) => {
    const box = worldRect(t, x1, y1, x2, y2)
    const from = worldToScreen(t, sx, sy)
    const to = worldToScreen(t, ex, ey)
    const fade = ctx.createLinearGradient(from.x, from.y, to.x, to.y)
    fade.addColorStop(0, `rgba(0, 0, 0, ${alpha})`)
    fade.addColorStop(1, 'rgba(0, 0, 0, 0)')
    ctx.fillStyle = fade
    ctx.fillRect(box.x, box.y, box.width, box.height)
  }
  // 近暗（0.012m）与远影（0.045m）各四条边，方向都是「库边脸 → 台面中心」。
  const shadeRing = (d: number, alpha: number) => {
    shadeBand(0, 0, TABLE_WIDTH, d, 0, 0, 0, d, alpha)
    shadeBand(0, TABLE_LENGTH - d, TABLE_WIDTH, TABLE_LENGTH, 0, TABLE_LENGTH, 0, TABLE_LENGTH - d, alpha)
    shadeBand(0, 0, d, TABLE_LENGTH, 0, 0, d, 0, alpha)
    shadeBand(TABLE_WIDTH - d, 0, TABLE_WIDTH, TABLE_LENGTH, TABLE_WIDTH, 0, TABLE_WIDTH - d, 0, alpha)
  }
  shadeRing(0.012, 0.24)
  shadeRing(0.045, 0.12)

  // 袋口那一圈暗影：洞沿、斜边都是立起来的面，压在布上会投一圈很短的影。
  //
  // 描边有一半落在缺口里，被上面那道 evenodd 裁剪吃掉，布上只剩内侧一半。
  // 所以线宽取 10mm，布上看到的是 5mm，和标准图里贴着缺口那道暗边差不多。
  // 再宽就会在洞口周围糊出一圈黑，看着像布被烫过。
  ctx.strokeStyle = 'rgba(0, 0, 0, 0.26)'
  ctx.lineWidth = Math.max(3, metersToPx(t, 0.010))
  for (let i = 0; i < POCKETS.length; i += 1) {
    ctx.beginPath()
    addNotch(ctx, t, i)
    ctx.stroke()
  }

  // 开球线与置球点：真实台面上这两条线一直在，缺了会显得台面是空的。
  // 开球线在母球那一侧（世界 y = 1/4 长），置球点在球堆那一侧（3/4 长）。
  //
  // 线宽 1px 的横线要落在半像素上才清晰，否则会被抗锯齿摊成两条浅灰。
  const headLine = worldToScreen(t, 0, TABLE_LENGTH * 0.25)
  ctx.strokeStyle = COLORS.clothLine
  ctx.lineWidth = 1
  ctx.beginPath()
  ctx.moveTo(rect.x, Math.round(headLine.y) + 0.5)
  ctx.lineTo(rect.x + rect.width, Math.round(headLine.y) + 0.5)
  ctx.stroke()

  const spot = worldToScreen(t, TABLE_WIDTH / 2, TABLE_LENGTH * 0.75)
  ctx.beginPath()
  ctx.arc(spot.x, spot.y, Math.max(1.5, metersToPx(t, BALL_RADIUS) * 0.16), 0, Math.PI * 2)
  ctx.fillStyle = COLORS.clothLine
  ctx.fill()
  ctx.restore()
}

// 袋口相关的颜色。
//
// 洞底不是纯黑：靠台外那一圈是皮革内壁（一点暗棕），越往里越黑，到中心几乎无光。
const POCKET_DEEP = '#04060a'

// 洞口画得比判定圆大 0.8mm。台呢按这个圆剪开，剪口正好压在洞沿上；
// 否则中间会漏出一条袋喉色的细缝，远看像洞口套了个环。
const HOLE_PAD = 0.0008

// 护角（袋口的金属包边）配色。标准图里那块金属从洞沿往外是一路压暗的：
// 贴着洞沿的倒角面最亮 (148,147,145)，铸铝主面 (115,115,115)，
// 背光到 (75,69,69)，最外那条接缝 (70,66,63)。
//
// 关键是「亮只亮在洞沿那一圈」：早先整块铺 #a5a29e，四个角变成四块银亮塑料，
// 比台子本身还抢眼。现在主面压到 (113,111,107)，亮的那一档只占渐变前 20%。
const PLATE_LIGHT = '#8b8783'
const PLATE_MID = '#726e6a'

// 斜边端面：贴着两条斜边铺的一条浅青绿压条，颜色由台呢绿压到墨绿黑。
//
// 标准图里从台呢往洞口是「布 → 斜边顶面（受光）→ 侧壁（墨绿黑）→ 皮革边」
// 四段连续过渡，少了这一段，布就直接切在洞沿上，像贴了一张绿纸；给多了
// 又会在袋口糊出一块黑三角，反倒比没有更假。
const JAW_FACE_WIDTH = 0.020

// 袋兜（洞口外面那圈皮革内壁）铺到 1.3 个半径。
// 再往外是斜边折下去的绿面，不是皮革——整块缺口刷成棕的，袋角就成了棕色三角。
const LINER_REACH = 1.3


// outwardScreen 给出袋口朝台外的单位方向（屏幕坐标）。
//
// 角袋朝斜角出去，中袋只朝垂直库边出去。用坐标判断而不是给每个袋口
// 写死方向：以后换成不同规格的台子，袋口位置一变方向就自动跟着对。
function outwardScreen(p: Pocket): { x: number; y: number } {
  let dx = 0
  let dy = 0
  if (p.x < 0) {
    dx = -1
  } else if (p.x > TABLE_WIDTH) {
    dx = 1
  }
  if (p.y < 0) {
    dy = -1
  } else if (p.y > TABLE_LENGTH) {
    dy = 1
  }
  const len = Math.hypot(dx, dy)
  // 世界坐标的 y 朝远端、屏幕的 y 朝下，翻一次符号。
  return { x: dx / len, y: -dy / len }
}

// Point 是世界坐标的一个点，只在袋口几何里用。
interface Point {
  x: number
  y: number
}

// throatPolygon 是角袋挖掉的那块：鼻尖A → 颊板A外端 → 颊板B外端 → 鼻尖B。
//
// 这条折线同时是三样东西：台呢的剪口、库边的收口、护角要让开的区域。
// 三处取自同一条路径，所以「画出来的斜边」永远只有一条，不会有某个袋口对不上。
//
// 落进台面里的那部分只有「两个鼻尖之间那条 45° 斜口」——台呢的内沿正是顺着
// 两段颊板凹进来的；剩下的两条边落在库边上，那是库边橡胶的端面，斜边就是它。
function throatPolygon(i: number): Point[] {
  const m = POCKET_MOUTHS[i]
  return [m.noseA, m.jawA, m.jawB, m.noseB]
}

// addPoly 把一串世界坐标的点接进当前路径（自己不 beginPath）。
//
// 台呢的剪口要和其他子路径凑成一条 evenodd 路径，所以这里不能开新路径；
// 单独用的时候调用方先 beginPath 一下就行。
function addPoly(ctx: CanvasRenderingContext2D, t: ViewTransform, pts: Point[]): void {
  for (let k = 0; k < pts.length; k += 1) {
    const p = worldToScreen(t, pts[k].x, pts[k].y)
    if (k === 0) {
      ctx.moveTo(p.x, p.y)
    } else {
      ctx.lineTo(p.x, p.y)
    }
  }
  ctx.closePath()
}

// addNotch 往当前路径里加一条「这个袋口挖掉的那一块」。
//
//   角袋：整个袋喉四边形。落在库边里的一大半是斜槽的侧壁，落进台呢的那部分
//         正好是「两个鼻尖之间那道 45° 斜口」——台呢的内沿顺着两段斜边凹进来。
//   中袋：「张口梯形 + 洞口探进台呢的那半个圆」。梯形是库边被切掉的那一块
//         （两段 14° 斜边），半个圆是洞压在台呢上的那一块。只挖圆的话，库边
//         的断口是一圈圆齿，看起来像洞被冲压在库边上，而不是两条斜边收进去。
function addNotch(ctx: CanvasRenderingContext2D, t: ViewTransform, i: number): void {
  const p = POCKETS[i]
  if (p.kind === 'side') {
    addSideNotch(ctx, t, i)
    return
  }
  addPoly(ctx, t, throatPolygon(i))
}

// sideNotchGeometry 算出中袋缺口的几个关键点：张口梯形的四个角，
// 以及洞口圆与库边脸的两个交点（缺口边界在两者之间的那段圆弧上）。
//
// 都是世界坐标；屏幕换算留给 addSideNotch，避免同一份点在两处各算一遍。
function sideNotchGeometry(i: number): {
  mouth: Array<{ x: number; y: number }>
  capFrom: { x: number; y: number }
  capTo: { x: number; y: number }
  center: { x: number; y: number }
  radius: number
  inward: { x: number; y: number }
} {
  const p = POCKETS[i]
  const m = POCKET_MOUTHS[i]
  const radius = p.radius + HOLE_PAD
  // 水平库边（左右两袋）：库边脸是竖线，缺口往 x 方向探进台呢；
  // 竖直库边（上下两袋）同理，只是换了轴。
  const horizontal = p.x < 0 || p.x > TABLE_WIDTH
  const face = horizontal ? (p.x < 0 ? 0 : TABLE_WIDTH) : p.y < 0 ? 0 : TABLE_LENGTH
  // 袋心到库边脸的距离，就是圆探进台呢的那一段深度。
  const offset = Math.abs((horizontal ? p.x : p.y) - face)
  // 弦长的一半：圆与库边脸的两个交点，各在袋心上下 halfChord 处。
  const halfChord = Math.sqrt(Math.max(0, radius * radius - offset * offset))
  const inward = horizontal
    ? { x: p.x < 0 ? 1 : -1, y: 0 }
    : { x: 0, y: p.y < 0 ? 1 : -1 }

  // 圆周上落在库边脸之外的那两个点，缺口就是在这两点之间绕半圈。
  const capFrom = horizontal
    ? { x: face, y: p.y - halfChord }
    : { x: p.x - halfChord, y: face }
  const capTo = horizontal
    ? { x: face, y: p.y + halfChord }
    : { x: p.x + halfChord, y: face }

  return {
    mouth: [m.noseA, m.jawA, m.jawB, m.noseB],
    capFrom,
    capTo,
    center: { x: p.x, y: p.y },
    radius,
    inward,
  }
}

// addSideNotch 把中袋缺口加成两条子路径：
//   1) 库边里那块梯形（两个鼻尖 + 两条斜边外端）；
//   2) 台呢里那块弓形（洞口圆被库边脸切下来的那一半 + 弦）。
//
// 早先是一条闭合折线把两者串起来，在「鼻尖 → 库边脸 → 圆弧起点」这几处会
// 出现角度极小的折返。填充本身看不出问题，但裁剪区域在折返点会带出一个
// 几像素的尖刺：袋兜铺上去之后，洞口右两侧各冒出一个小三角，看着像洞的
// 圆边「突出来一块」。拆成两条各自闭合的子路径，弓形就是圆的一段弧加弦，
// 形状上不可能多出尖角。
function addSideNotch(ctx: CanvasRenderingContext2D, t: ViewTransform, i: number): void {
  const g = sideNotchGeometry(i)
  const mouth = g.mouth.map((q) => worldToScreen(t, q.x, q.y))

  // 梯形：鼻尖 → 斜边外端 → 另一侧斜边外端 → 另一侧鼻尖，closePath 沿库边脸封口。
  ctx.moveTo(mouth[0].x, mouth[0].y)
  ctx.lineTo(mouth[1].x, mouth[1].y)
  ctx.lineTo(mouth[2].x, mouth[2].y)
  ctx.lineTo(mouth[3].x, mouth[3].y)
  ctx.closePath()

  // 弓形：以袋心为圆心、洞沿为半径，从库边脸这一侧的交点绕到另一侧。
  // 弦由 closePath 补出来，正好落在库边脸上。
  const from = worldToScreen(t, g.capFrom.x, g.capFrom.y)
  const to = worldToScreen(t, g.capTo.x, g.capTo.y)
  const center = worldToScreen(t, g.center.x, g.center.y)
  const radius = metersToPx(t, g.radius)
  const inward = { x: g.inward.x, y: -g.inward.y }
  ctx.moveTo(from.x, from.y)
  arcThrough(ctx, center, radius, from, to, inward)
  ctx.closePath()
}

// arcThrough 从 from 沿圆周画到 to，且必定经过朝 inward 方向的那一侧。
//
// canvas 的两个方向参数只差在「顺时针 / 逆时针」，而这里要保证的是
// 「绕到台呢里那一半」——先把 from→to 的默认方向试一遍，中点方向不对就反向。
function arcThrough(
  ctx: CanvasRenderingContext2D,
  center: { x: number; y: number },
  radius: number,
  from: { x: number; y: number },
  to: { x: number; y: number },
  inward: { x: number; y: number },
): void {
  const a0 = Math.atan2(from.y - center.y, from.x - center.x)
  const a1 = Math.atan2(to.y - center.y, to.x - center.x)
  const mid = Math.atan2(inward.y, inward.x)

  // 判断 mid 是否落在 a0 → a1（角度递增，即 counterclockwise=false 的那一侧）。
  const tau = Math.PI * 2
  const norm = (a: number) => ((a % tau) + tau) % tau
  const s = norm(a0)
  const e = norm(a1)
  const m = norm(mid)
  const forward = s <= e ? m >= s && m <= e : m >= s || m <= e
  ctx.arc(center.x, center.y, radius, a0, a1, !forward)
}

// greenRectScreen 是「台呢 + 库边」那一整块的外沿矩形，也就是有布覆盖的范围。
// 护角只许画在它外面，金属永远到不了台呢上。
function greenRectScreen(t: ViewTransform): Rect {
  const w = CUSHION_WIDTH
  return worldRect(t, -w, -w, TABLE_WIDTH + w, TABLE_LENGTH + w)
}

// addGreenOutline 往当前路径里追加「库边外沿 + 袋口缺口」这条轮廓。
//
// 它是「铺过布的轮廓」：库边的斜坡条要裁在它里面，缺口的角要跟着它的形状走。
// 自己不 beginPath，方便和别的子路径凑成一条 evenodd 路径。
//
// skipSide 为 true 时不挖中袋的缺口。中袋要这么做：它的斜边只在库边里收进去
// 12.5mm，挖出来是一块 128×50mm 的梯形，而洞口圆大部分压在库边上——布一旦
// 被挖走，那块就只剩金属，只能靠皮革内壁去盖，结果就是洞沿两侧各鼓起一块
// 棕三角，看着像圆边「突出来」。真实台桌的中袋，布是一路铺到洞沿的。
// 角袋不跳过：它的斜槽深 105mm，缺口是实打实的凹槽，布本来就该断在那里。
function addGreenOutline(ctx: CanvasRenderingContext2D, t: ViewTransform, skipSide = false): void {
  const outer = greenRectScreen(t)
  ctx.rect(outer.x, outer.y, outer.width, outer.height)
  for (let i = 0; i < POCKETS.length; i += 1) {
    if (skipSide && POCKETS[i].kind === 'side') {
      continue
    }
    addNotch(ctx, t, i)
  }
}

// clothPath 铺台呢的轮廓：一个矩形，六处袋口各挖掉一块。
// 调用方用 evenodd 填充或裁剪，挖掉的那几块就是「没有布的地方」。
function clothPath(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const rect = worldRect(t, 0, 0, TABLE_WIDTH, TABLE_LENGTH)
  ctx.beginPath()
  ctx.rect(rect.x, rect.y, rect.width, rect.height)
  for (let i = 0; i < POCKETS.length; i += 1) {
    addNotch(ctx, t, i)
  }
}

// drawPockets 画六个袋口。五层，顺序不能换：
//   1) 护角 —— 银灰金属，铺满台呢之外（含袋角），最底下一层；
//   2) 绿面 —— 库边的布折进缺口的那一小块，靠近台呢那侧被灯照到；
//   3) 袋兜 —— 皮革内壁，只围着洞口铺一圈，压在绿面上；
//   4) 端面 —— 贴着两条斜边的浅青绿压条，是斜边顶面那一条受光的棱；
//   5) 圆洞 —— 圆心与半径直接取 POCKETS，与落袋判定是同一个圆。
//
// 第 2 层不能省：缺口是一块 84mm 见方的三角，洞只占中间一圈，剩下的部分
// 如果留成袋兜的暗棕，袋角就糊成一大片黑——标准图里那两块是绿的。
function drawPockets(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const rail = railRect(t)

  ctx.save()
  roundRectPath(ctx, rail.x, rail.y, rail.width, rail.height, RAIL_RADIUS)
  ctx.clip()

  for (let i = 0; i < POCKETS.length; i += 1) {
    drawGuard(ctx, t, i, rail)
  }
  for (let i = 0; i < POCKETS.length; i += 1) {
    drawJawBase(ctx, t, i)
  }
  for (let i = 0; i < POCKETS.length; i += 1) {
    drawLiner(ctx, t, i)
  }
  for (let i = 0; i < POCKETS.length; i += 1) {
    drawJawFace(ctx, t, i)
  }
  for (let i = 0; i < POCKETS.length; i += 1) {
    const p = POCKETS[i]
    drawHole(ctx, p, worldToScreen(t, p.x, p.y), metersToPx(t, p.radius + HOLE_PAD))
  }

  ctx.restore()
}

// drawJawBase 画缺口里那块绿面：库边的布一直包到袋口，在缺口处折下去。
//
// 只有角袋有——中袋的缺口就是一个洞，没有可填的面积。
// 渐变从洞沿往外提亮：贴着洞那一圈是折进去的暗面，靠近台呢那一侧才是
// 被灯照到的斜边顶面。
function drawJawBase(ctx: CanvasRenderingContext2D, t: ViewTransform, i: number): void {
  const p = POCKETS[i]
  if (p.kind !== 'corner') {
    return
  }
  const center = worldToScreen(t, p.x, p.y)
  const radius = metersToPx(t, p.radius)
  const green = greenRectScreen(t)

  ctx.save()
  // 只填缺口，而且只填台呢这一侧：缺口伸进库边的那两条角是斜槽的侧壁，
  // 那里该露护角的金属，填绿会变成一块绿三角压在金属上。
  ctx.beginPath()
  addNotch(ctx, t, i)
  ctx.clip()
  ctx.beginPath()
  ctx.rect(green.x, green.y, green.width, green.height)
  ctx.clip()

  const face = ctx.createRadialGradient(
    center.x,
    center.y,
    radius * 0.92,
    center.x,
    center.y,
    radius * 2.1,
  )
  face.addColorStop(0, '#123a2a')
  face.addColorStop(0.5, '#1a6a4b')
  face.addColorStop(1, CUSHION_FACE)
  ctx.fillStyle = face
  ctx.fillRect(green.x, green.y, green.width, green.height)

  ctx.restore()
}

// drawLiner 画袋兜：洞口外面那一圈皮革内壁。
//
// 它只围到 1.3 个半径：把整个缺口都刷成暗棕的话，袋角会出现一大片黑，
// 标准图里那两块是绿的（见 drawJawBase）。这一圈必须暗——填成金属的话，
// 洞口就像镶了一圈银边，中袋两侧还会露出两道灰弧探进绿台面。
//
// 中袋不画。它的缺口在库边里是一块 128×50mm 的梯形，比洞口圆还宽一圈；
// 内壁铺进去之后，圆的两侧各多出一个棕三角，看着就是「洞的圆边突出来一块」。
// 中袋这里布已经铺到洞沿（见 addGreenOutline 的 skipSide），洞沿往外的
// 暗色由 drawHole 渐变的最后一档负责，不需要再垫一层。
function drawLiner(ctx: CanvasRenderingContext2D, t: ViewTransform, i: number): void {
  const p = POCKETS[i]
  if (p.kind === 'side') {
    return
  }
  const center = worldToScreen(t, p.x, p.y)
  const radius = metersToPx(t, p.radius)
  // 内壁只画到库边外沿：再往外是木框，那里该露出护角的金属。
  const green = worldRect(
    t,
    -CUSHION_WIDTH,
    -CUSHION_WIDTH,
    TABLE_WIDTH + CUSHION_WIDTH,
    TABLE_LENGTH + CUSHION_WIDTH,
  )

  ctx.save()
  ctx.beginPath()
  addNotch(ctx, t, i)
  ctx.clip()
  ctx.beginPath()
  ctx.rect(green.x, green.y, green.width, green.height)
  ctx.clip()
  // 再按洞口套一个圆：内壁只是洞口外面那一圈，超出这个圆的部分留给绿面。
  // 不套的话整块缺口都是棕的，袋角就成了一个棕色三角。
  ctx.beginPath()
  ctx.arc(center.x, center.y, radius * LINER_REACH, 0, Math.PI * 2)
  ctx.clip()

  // 贴着洞沿几乎无光，退到剪口那侧才露一点暗棕——皮革内壁的色，不是纯黑。
  // 这两档要与 drawHole 最外两档接得上（洞沿是 #31201a），接不上会在洞口
  // 镶出一圈比洞更黑的环。
  const liner = ctx.createRadialGradient(
    center.x,
    center.y,
    radius * 0.86,
    center.x,
    center.y,
    radius * LINER_REACH,
  )
  liner.addColorStop(0, '#1e1411')
  liner.addColorStop(0.6, '#2b1c17')
  // 最后一档透明：内壁往绿面上淡出去，不留一圈圆的硬边——否则洞口像套了个
  // 棕色甜甜圈。
  liner.addColorStop(1, 'rgba(48, 34, 28, 0)')
  ctx.fillStyle = liner
  ctx.fillRect(green.x, green.y, green.width, green.height)

  ctx.restore()
}

// drawGuard 画护角：袋口周围那块银灰金属板。
//
// 位置有两条硬约束，标准图里都能看出来：
//   1) 不压台呢——按「台呢和库边之外」裁一刀，绿色台面上永远不会出现金属；
//   2) 中袋没有护角板，只留洞口外缘一圈窄边，再大就会在台呢上出环
//      （用户点名过「中袋内侧没有灰色圆弧探进绿色台面」）。
//
// 袋角那一块是让出来的：金属先铺满，袋兜（drawLiner）再在洞口周围压一圈黑。
// 反过来的话，袋兜会把整个袋角刷黑，标准图里那块银灰就没了。
//
// 金属面用一条「从洞沿往外淡到透明」的径向渐变，而不是一块实色方块：
// 标准图里那块金属没有直边，是绕着洞口一圈一圈暗进木框里的。用方块的话
// 四个角会变成四块贴上去的银灰瓷砖，边界比台子本身还硬。
function drawGuard(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  i: number,
  rail: Rect,
): void {
  const p = POCKETS[i]
  const center = worldToScreen(t, p.x, p.y)
  const radius = metersToPx(t, p.radius)

  ctx.save()

  // 木框矩形 + 库边外沿矩形，evenodd 取「台呢和库边之外」那一环。
  // 这里只挖到库边外沿，不再把袋口缺口算进去：缺口的形状（三角形）一直铺到
  // 木框里，算进去的话袋角正中会被挖掉一块，而那正是护角最该亮的地方。
  ctx.beginPath()
  addRoundRect(ctx, rail.x, rail.y, rail.width, rail.height, RAIL_RADIUS)
  const green = greenRectScreen(t)
  ctx.rect(green.x, green.y, green.width, green.height)
  ctx.clip('evenodd')

  // 中袋不再额外套一个圆。早先套过 1.1 个半径，结果金属在洞口左侧只剩
  // 一弯细月牙，看着像洞沿镶了一圈白边；标准图里那块金属是被库边外沿
  // 一刀切平的，边界是直线不是圆弧。
  //
  // 明暗排布照标准图：最亮的一档落在 1.3 个半径上（就是护角绕着洞口那一圈），
  // 往外维持在中灰，最后淡进木框。早先把最亮档放在 0.92 个半径，正好被袋兜
  // 盖住——画了半天金属，能看见的那一圈全是暗的，袋角灰得像块水泥。
  //
  // 中袋只需铺到 2.6 个半径：护角在它四周就是一小块独立的金属板，
  // 铺得太小会变成洞口外面一团灰雾，看不出是块板子。
  const reach = p.kind === 'corner' ? radius * 2.3 : radius * 2.6
  const metal = ctx.createRadialGradient(center.x, center.y, radius * 0.92, center.x, center.y, reach)
  metal.addColorStop(0, PLATE_MID)
  metal.addColorStop(0.22, PLATE_LIGHT)
  metal.addColorStop(0.7, PLATE_MID)
  // 最后一档透明：金属淡进木框本色，不留一条硬边。
  metal.addColorStop(1, 'rgba(98, 94, 90, 0)')
  ctx.fillStyle = metal
  ctx.fillRect(center.x - reach, center.y - reach, reach * 2, reach * 2)

  ctx.restore()
}

// drawJawFace 画角袋的斜边端面：两段颊板各铺一条短坡。
//
// 两条坡都从台呢剪口往里 20mm 收暗，压在袋兜与绿面之上。这是「斜边有厚度」
// 唯一的视觉线索：少了它，布就是一刀切在水平面上；有了它，放大看能看出库边
// 是折下来、在袋口有个立着的端面——球撞斜边时弹的就是这条边。
//
// 曾经还有第三条「正面坡」连在两个鼻尖之间。它在画面上就是一根横在洞口的
// 横杠：两个鼻尖本来就隔着 119mm，铺 20mm 宽的亮绿上去，比洞还抢眼。
// 真实的袋口正面并不是一块立面，而是一段圆弧（洞本身），所以删掉。
//
// 中袋不画：它的两条颊板整段落在洞口圆里，画了也会被洞盖掉；中袋的端面在
// 洞沿上，由台呢剪口那道暗边代表。
function drawJawFace(ctx: CanvasRenderingContext2D, t: ViewTransform, i: number): void {
  const p = POCKETS[i]
  if (p.kind !== 'corner') {
    return
  }
  const m = POCKET_MOUTHS[i]
  const center = worldToScreen(t, p.x, p.y)
  const width = metersToPx(t, JAW_FACE_WIDTH)

  ctx.save()
  // 只许铺在缺口里：缺口之外是台呢，压上去就是布上多一条绿边。
  ctx.beginPath()
  addNotch(ctx, t, i)
  ctx.clip()

  // 两条坡的内法向都指向袋心，所以同一个参考点够用。
  jawFace(ctx, t, m.jawA, m.noseA, center, width)
  jawFace(ctx, t, m.noseB, m.jawB, center, width)

  ctx.restore()
}

// jawFace 沿一条边往袋口里铺一条宽 width 的实色短坡。
//
// 渐变轴垂直于这条边、指向袋心：贴边是台呢那一档绿，往外压到墨绿黑。
// 三档都是实色，不用半透明——它铺在银灰金属上，半透明会混成脏绿，
// 看起来像金属生了锈。
function jawFace(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  from: Point,
  to: Point,
  center: { x: number; y: number },
  width: number,
): void {
  const a = worldToScreen(t, from.x, from.y)
  const b = worldToScreen(t, to.x, to.y)
  let dx = b.x - a.x
  let dy = b.y - a.y
  const len = Math.hypot(dx, dy)
  if (len < 0.5) {
    return
  }
  dx /= len
  dy /= len

  // 内法向：指向袋心那一侧。两个候选里选和「起点 → 袋心」同向的那个。
  let nx = -dy
  let ny = dx
  if ((center.x - a.x) * nx + (center.y - a.y) * ny < 0) {
    nx = -nx
    ny = -ny
  }

  ctx.beginPath()
  ctx.moveTo(a.x, a.y)
  ctx.lineTo(b.x, b.y)
  ctx.lineTo(b.x + nx * width, b.y + ny * width)
  ctx.lineTo(a.x + nx * width, a.y + ny * width)
  ctx.closePath()

  const ramp = ctx.createLinearGradient(a.x, a.y, a.x + nx * width, a.y + ny * width)
  // 贴边那一档要亮：那是斜边顶面受光的一条棱，比缺口里那块折下去的绿面亮一档。
  // 三档都是实色，不用半透明——半透明会跟底下的绿面混成一块，棱就没了。
  ramp.addColorStop(0, '#1d8a5e')
  ramp.addColorStop(0.5, '#12503a')
  ramp.addColorStop(1, '#0a1c14')
  ctx.fillStyle = ramp
  ctx.fill()
}

// drawHole 画圆洞：圆心与半径就是落袋判定用的那个圆（只大 0.8mm，与台呢剪口重合）。
//
// 不画任何外圈光环。早先洞外压过一圈 1.42r 的暗影，中袋那半个圆压在台呢上时，
// 那圈暗影就成了「绿色台面上套了一个灰环」——标准图里没有这个环，台呢是直接
// 剪到洞沿的，洞沿只剩一圈皮革内壁的暗棕。
function drawHole(
  ctx: CanvasRenderingContext2D,
  p: Pocket,
  center: { x: number; y: number },
  radius: number,
): void {
  const out = outwardScreen(p)
  const hole = ctx.createRadialGradient(
    center.x - out.x * radius * 0.32,
    center.y - out.y * radius * 0.32,
    radius * 0.05,
    center.x,
    center.y,
    radius,
  )
  hole.addColorStop(0, POCKET_DEEP)
  hole.addColorStop(0.38, '#07080c')
  // 洞底不是一整块黑：标准图里只有靠里的一半是黑的，往外 0.6 个半径就开始
  // 转暖，到洞沿已经是一圈暗棕的皮革。做成纯黑的话，洞像一块贴上去的圆片。
  hole.addColorStop(0.66, '#100c0a')
  hole.addColorStop(0.86, '#221410')
  hole.addColorStop(1, '#31201a')

  ctx.beginPath()
  ctx.arc(center.x, center.y, radius, 0, Math.PI * 2)
  ctx.fillStyle = hole
  ctx.fill()
}

// drawGuides 画目标球与母球的去向引导线。
//
// 两种颜色区分「这是哪颗球的线」：目标球用暖白（与瞄准线同色系），
// 母球用偏冷的浅蓝。线都短，只用来提示方向，不做精确预测。
function drawGuides(ctx: CanvasRenderingContext2D, t: ViewTransform, guides: AimGuides): void {
  ctx.save()
  ctx.setLineDash([4, 5])
  ctx.lineWidth = 1.5

  if (guides.object) {
    const from = worldToScreen(t, guides.object.from.x, guides.object.from.y)
    const to = worldToScreen(t, guides.object.to.x, guides.object.to.y)
    ctx.strokeStyle = 'rgba(255, 246, 220, 0.62)'
    ctx.beginPath()
    ctx.moveTo(from.x, from.y)
    ctx.lineTo(to.x, to.y)
    ctx.stroke()
  }

  if (guides.cue) {
    const from = worldToScreen(t, guides.cue.from.x, guides.cue.from.y)
    const to = worldToScreen(t, guides.cue.to.x, guides.cue.to.y)
    ctx.strokeStyle = 'rgba(150, 205, 255, 0.5)'
    ctx.beginPath()
    ctx.moveTo(from.x, from.y)
    ctx.lineTo(to.x, to.y)
    ctx.stroke()
  }

  ctx.restore()
}

// drawAim 画瞄准虚线与幽灵球。
//
// 线从母球表面开始而不是球心：从球心画会有一条穿过母球的白线，
// 看起来像球被划开了。幽灵球只在会撞到球时出现——撞库时画个圆反而误导。
function drawAim(ctx: CanvasRenderingContext2D, t: ViewTransform, aim: AimLayer): void {
  const from = worldToScreen(t, aim.cue.x, aim.cue.y)
  const to = worldToScreen(t, aim.hit.x, aim.hit.y)
  const radiusPx = ballRadiusPx(t)

  // 沿方向退一个球半径，得到线在母球边缘的起点。
  const dx = to.x - from.x
  const dy = to.y - from.y
  const len = Math.hypot(dx, dy)
  if (len < 1e-6) {
    return
  }
  const startX = from.x + (dx / len) * radiusPx
  const startY = from.y + (dy / len) * radiusPx

  // 暖白色的细虚线。参考视频里瞄准线就是一条又细又亮的暖色线，
  // 台呢是绿的，用白色或青色都会被台面吃掉。
  // 撞到对手的球时换成警示红：这条线本身还在（方向要看得见），
  // 但它指向的那颗球不能打，颜色先给一次提示，具体原因由禁止符号说明。
  ctx.save()
  ctx.setLineDash([5, 7])
  ctx.lineWidth = 1.6
  ctx.strokeStyle = aim.forbidden ? 'rgba(255, 122, 122, 0.9)' : 'rgba(255, 233, 176, 0.85)'
  ctx.beginPath()
  ctx.moveTo(startX, startY)
  ctx.lineTo(to.x, to.y)
  ctx.stroke()
  ctx.restore()

  // 幽灵球：先铺一层很淡的白，再勾一圈边。
  // 只勾边看不清，只铺一层又会和真球混起来，两层一起才像「这里会停一颗球」。
  // 不该打的球不给幽灵球：那一圈白圈会被读成「打这里就对了」。
  if (aim.hit.kind === 'ball' && !aim.forbidden) {
    ctx.beginPath()
    ctx.arc(to.x, to.y, radiusPx, 0, Math.PI * 2)
    ctx.fillStyle = 'rgba(255, 255, 255, 0.14)'
    ctx.fill()
    ctx.strokeStyle = 'rgba(255, 246, 220, 0.9)'
    ctx.lineWidth = 1.6
    ctx.stroke()
  }

  if (aim.forbidden && aim.hit.kind === 'ball') {
    drawProhibition(ctx, t, aim)
  }
}

// drawProhibition 在「不该打的那颗球」上盖一个禁止符号（红圈加一斜杠）。
//
// 圆心取目标球球心：幽灵球心沿击球方向再走一个直径就是它。符号比球大一圈，
// 小了会被号码盖住，大了会压到旁边的球，看起来就不知道在指谁。
function drawProhibition(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  aim: AimLayer,
): void {
  const hit = worldToScreen(t, aim.hit.x, aim.hit.y)
  // 屏幕 y 朝下、phi 的 y 朝远端，方向换算与瞄准线共用同一条规则。
  const dirX = Math.cos(aim.phi)
  const dirY = -Math.sin(aim.phi)
  const radiusPx = ballRadiusPx(t)
  const cx = hit.x + dirX * radiusPx * 2
  const cy = hit.y + dirY * radiusPx * 2
  const r = radiusPx * 1.5

  ctx.save()
  // 一圈很淡的暗影把符号从花球的花纹上托起来，别让红色直接贴着彩色。
  ctx.shadowColor = 'rgba(0, 0, 0, 0.55)'
  ctx.shadowBlur = 4
  ctx.strokeStyle = 'rgba(255, 92, 92, 0.95)'
  ctx.lineWidth = Math.max(2, radiusPx * 0.18)
  ctx.beginPath()
  ctx.arc(cx, cy, r, 0, Math.PI * 2)
  ctx.stroke()

  // 斜杠从右上到左下，和交通标志上的禁止符号同向。
  const k = r * 0.7
  ctx.beginPath()
  ctx.moveTo(cx + k, cy - k)
  ctx.lineTo(cx - k, cy + k)
  ctx.stroke()
  ctx.restore()
}

// drawCue 画那根球杆。
//
// 球杆沿击球方向的反方向伸出去：杆头贴在母球后面，杆尾指向「球会飞出去的
// 那一侧的对侧」。玩家看到杆子从哪个方向顶过来，就知道球会往反方向走，
// 这是台球桌上最不需要解释的一条。
//
// 力度体现在「后拉」上：力度条往上推，杆子往后缩，松手才前进击球。
// 所以这一笔同时是瞄准指示与力度反馈，不用再画第二条力量条在台面上。
function drawCue(ctx: CanvasRenderingContext2D, t: ViewTransform, aim: AimLayer): void {
  const center = worldToScreen(t, aim.cue.x, aim.cue.y)
  const radiusPx = ballRadiusPx(t)

  // 屏幕方向与 phi 的关系和瞄准线共用同一条换算：y 轴朝下所以要取负。
  const dirX = Math.cos(aim.phi)
  const dirY = -Math.sin(aim.phi)

  // 杆头离球心的距离 = 一个球半径 + 一点缝隙 + 后拉量。
  // 后拉量按球半径缩放，换屏幕尺寸时杆子的相对长度不变。
  const pullback = radiusPx * (0.35 + aim.power * 3.2)
  const tipDistance = radiusPx + radiusPx * 0.18 + pullback

  const tipX = center.x - dirX * tipDistance
  const tipY = center.y - dirY * tipDistance

  // 杆长 34 倍球半径。真实球杆约 1.45 米、球径 5.7 厘米，也就是 25 倍直径、
  // 50 倍半径；但画布只比球台大一圈，按真实长度画，杆尾会甩出画布被切掉。
  // 34 倍是这条约束下的折中：看得出是一根完整的长杆，又基本都在画布里。
  const length = radiusPx * 34
  // 粗细：皮头 0.14 个球半径、杆尾 0.40。按真实杆子（皮头 13mm、杆尾 30mm）
  // 是 0.11 / 0.26，但杆身太细在绿呢上像一条划痕，加粗一点才看得出是根杆。
  const tipHalf = Math.max(2, radiusPx * 0.14)
  const buttHalf = Math.max(4, radiusPx * 0.4)

  // 本地坐标的 +x 指向杆尾（也就是击球方向的反方向）。
  const angle = Math.atan2(-dirY, -dirX)

  ctx.save()
  ctx.translate(tipX, tipY)
  ctx.rotate(angle)

  // 投影只有一点点：杆子贴着台面，影子重了会像浮在空中。
  ctx.shadowColor = 'rgba(0, 0, 0, 0.35)'
  ctx.shadowBlur = 5
  ctx.shadowOffsetY = 2

  const gradient = ctx.createLinearGradient(0, 0, length, 0)
  if (aim.locked) {
    // 锁定之后整套颜色偏暖，一眼能看出「方向已经定住了」。
    gradient.addColorStop(0, '#f4e3bb')
    gradient.addColorStop(0.55, '#d8b072')
    gradient.addColorStop(1, '#8a5a22')
  } else {
    gradient.addColorStop(0, '#e8d8b6')
    gradient.addColorStop(0.55, '#c9a878')
    gradient.addColorStop(1, '#7d5330')
  }

  // 一根两头粗细不同的杆：用四边形而不是 lineWidth，
  // 否则整根一样粗，看上去像一根棍子。
  ctx.beginPath()
  ctx.moveTo(0, -tipHalf)
  ctx.lineTo(length * 0.09, -tipHalf * 1.5)
  ctx.lineTo(length, -buttHalf)
  ctx.lineTo(length, buttHalf)
  ctx.lineTo(length * 0.09, tipHalf * 1.5)
  ctx.lineTo(0, tipHalf)
  ctx.closePath()
  ctx.fillStyle = gradient
  ctx.fill()

  // 皮头：杆头最前面那一小截深色。没有它，杆子顶在球上分不清哪头是头。
  ctx.shadowColor = 'transparent'
  ctx.fillStyle = '#243b57'
  ctx.fillRect(0, -tipHalf, Math.max(3, length * 0.03), tipHalf * 2)

  // 先角（白色那一圈）把皮头与木身分开，真杆都有。
  ctx.fillStyle = 'rgba(250, 248, 240, 0.92)'
  ctx.fillRect(Math.max(3, length * 0.03), -tipHalf * 1.15, Math.max(2, length * 0.02), tipHalf * 2.3)

  ctx.restore()
}

// drawBalls 先画所有球影、再画所有球。
//
// 分两轮而不是逐颗「影 + 球」：逐颗画时后一颗球的影子会盖在前一颗球身上，
// 球挤在一起就会出现半透明的黑斑。先统一铺影，球体永远是干净的。
function drawBalls(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  balls: ReadonlyArray<BallState>,
  dpr: number,
): void {
  const radiusPx = ballRadiusPx(t)

  ctx.save()
  ctx.fillStyle = COLORS.shadow
  for (const ball of balls) {
    if (!ball.on_table) {
      continue
    }
    const center = worldToScreen(t, ball.x, ball.y)
    // z 是球底离台面的高度，不是球心高度。腾空时影子变远变淡，
    // 这是 2D 里唯一能表达「球飞起来了」的线索。
    const lift = metersToPx(t, ball.z)
    ctx.globalAlpha = Math.max(0.3, 1 - ball.z * SHADOW_FADE)
    ctx.beginPath()
    ctx.ellipse(
      center.x + lift * SHADOW_SPREAD + radiusPx * 0.12,
      center.y + lift * SHADOW_SPREAD + radiusPx * 0.18,
      radiusPx * 0.92,
      radiusPx * 0.7,
      0,
      0,
      Math.PI * 2,
    )
    ctx.fill()
  }
  ctx.restore()

  // 贴图按物理像素取，2x 屏上号码边缘才不发虚。
  // 上限 48 像素：球在屏幕上只有三十来像素，再往上是白算——
  // 而每多一倍边长，球面重采样的像素数就翻四倍。
  const textureSide = Math.min(SPRITE_MAX_SIDE, Math.round(radiusPx * 2 * dpr))

  const onTable: BallState[] = []
  for (const ball of balls) {
    if (ball.on_table) {
      onTable.push(ball)
    }
  }

  // 这一帧谁先拿到重采样预算。击球瞬间十六颗球同时滚，每颗都重采样一遍
  // 会占住主线程十几毫秒，rAF 一停球就像瞬移；分成两帧算完，肉眼分不出
  // 谁晚了一帧。起点每帧往后挪一批，保证没有哪几颗永远排在预算之外
  // （固定从第一颗开始的话，排在后面的球号码会一直不转）。
  const total = onTable.length
  const startIndex = total > 0 ? (spriteTurn * SPRITE_FRAME_BUDGET) % total : 0
  const rankOf = new Map<string, number>()
  for (let k = 0; k < total; k += 1) {
    rankOf.set(onTable[(startIndex + k) % total].id, k)
  }
  spriteTurn += 1

  // 先算这一帧谁和谁贴着。球与球的边界只靠各自的边缘暗撑不住，
  // 十六颗球挤在三角里会糊成一片色块；朝邻居那一侧压一道暗，
  // 球才一颗颗分得开（真实的台球也是靠这道接触阴影分开的）。
  const contacts = contactMap(onTable)

  for (const ball of onTable) {
    const rank = rankOf.get(ball.id)
    const touching = contacts.get(ball.id)
    drawBall(ctx, t, ball, radiusPx, textureSide, rank === undefined ? 0 : rank, touching)
  }
}

// Contact 是一颗球朝着某个邻居的方向（单位向量，屏幕坐标系）。
interface Contact {
  dx: number
  dy: number
}

// 两颗球中心的距离小于这个值就算贴着。摆球时相邻两颗正好差一个直径，
// 留 10% 余量兜住滚动中的轻微穿透。
const CONTACT_REACH = BALL_RADIUS * 2.1

// contactMap 扫一遍所有球对，返回「球号 → 朝各邻居的方向」。
//
// 只有十几颗球，两两比较 120 次远比在渲染循环里维护一份空间索引便宜；
// 一颗球同时贴住三颗以上的情况很少，超过三颗只取最近的几颗，
// 免得球堆中心那颗被四道暗斑压成黑的。
const MAX_CONTACTS = 3

function contactMap(balls: ReadonlyArray<BallState>): Map<string, Contact[]> {
  const out = new Map<string, Contact[]>()
  for (let i = 0; i < balls.length; i += 1) {
    for (let j = i + 1; j < balls.length; j += 1) {
      const a = balls[i]
      const b = balls[j]
      // 一上一下的两颗球不是贴着，是叠着（跳球），不能压接触阴影。
      if (Math.abs(a.z - b.z) > BALL_RADIUS * 0.5) {
        continue
      }
      const dx = b.x - a.x
      const dy = b.y - a.y
      const distance = Math.hypot(dx, dy)
      if (distance > CONTACT_REACH || distance < 1e-6) {
        continue
      }
      const unit: Contact = { dx: dx / distance, dy: dy / distance }
      pushContact(out, a.id, unit)
      pushContact(out, b.id, { dx: -unit.dx, dy: -unit.dy })
    }
  }
  return out
}

// pushContact 往某颗球的邻居列表里加一条，满了就不再收。
//
// 这里不比较远近：要「丢掉最远的那条」就得先量完所有邻居，而球堆里
// 一颗球最多也就贴住三四颗，多出来的那几条压在边缘上，少画一道看不出来。
function pushContact(out: Map<string, Contact[]>, id: string, contact: Contact): void {
  const list = out.get(id)
  if (!list) {
    out.set(id, [contact])
    return
  }
  if (list.length >= MAX_CONTACTS) {
    return
  }
  list.push(contact)
}

// ===== 球的三维朝向 =====
//
// 台面上的球是滚过去的，不是原地打转。要画出这一点，每颗球都得记住自己的
// 三维朝向：纯滚动时它绕「垂直于运动方向的水平轴」转，转过的角度等于
// 走过的弧长除以半径。只按屏幕法线转贴图，看起来永远是同一面在转圈，
// 号码不会转到侧面去，而且往左滚和往右滚一个样。
//
// 渲染层本来「只读输入」，这是唯一的例外：一颗球转了多少只跟它走过的路径有关，
// 光看当前位置推不出来。
interface RollState {
  x: number
  y: number
  // 球体局部坐标到世界坐标的 3×3 旋转矩阵，行优先，长度 9。
  // 用数组而不是对象：每帧只是九次乘加，没必要为可读性多一层属性访问。
  m: number[]
}

const rollState = new Map<string, RollState>()

// 一帧里位移超过这个距离就当成「瞬移」而不是滚动：
// 换局、重连补齐、自由球落位都会让球直接出现在别处，那时转半圈很假。
const TELEPORT_DISTANCE = BALL_RADIUS * 6

// IDENTITY 是单位旋转，球还没动过时用这个。
const IDENTITY = [1, 0, 0, 0, 1, 0, 0, 0, 1]

// multiply3 算两个行优先 3×3 矩阵的乘积 a·b。
function multiply3(a: number[], b: number[]): number[] {
  const out = new Array<number>(9)
  for (let row = 0; row < 3; row += 1) {
    for (let col = 0; col < 3; col += 1) {
      out[row * 3 + col] =
        a[row * 3] * b[col] + a[row * 3 + 1] * b[3 + col] + a[row * 3 + 2] * b[6 + col]
    }
  }
  return out
}

// rollMatrixOf 返回这颗球当前该用哪个朝向，就地把这一帧的位移滚进去。
//
// 没动的时候返回**同一个数组引用**：球面位图的缓存靠引用比较判断能不能复用，
// 每帧新建一个值相同的数组会让缓存永远不命中，白白重画十六颗球。
function rollMatrixOf(ball: BallState): number[] {
  const prev = rollState.get(ball.id)
  if (!prev) {
    rollState.set(ball.id, { x: ball.x, y: ball.y, m: IDENTITY })
    return IDENTITY
  }

  const dx = ball.x - prev.x
  // 位移换到「观察坐标」（x 向右、y 向下、z 朝观察者），与屏幕方向一致。
  // 世界坐标的 y 朝远端，所以这里取负：不换的话竖直方向的滚动会反着转。
  const dy = -(ball.y - prev.y)
  prev.x = ball.x
  prev.y = ball.y
  const distance = Math.hypot(dx, dy)
  // 瞬移不推进朝向：那不是滚出来的位移。
  if (distance < 1e-6 || distance > TELEPORT_DISTANCE) {
    return prev.m
  }

  // 滚动轴在水平面内，垂直于运动方向。取哪一侧由「球顶往运动方向倒」定：
  // 往 +x 滚时球顶要朝 +x 倒，对应绕 +y 转正角，所以轴取 (-dy, dx)。
  const ax = -dy / distance
  const ay = dx / distance
  const angle = distance / BALL_RADIUS
  const cos = Math.cos(angle)
  const sin = Math.sin(angle)
  const t = 1 - cos
  // 罗德里格斯公式，轴在 xy 平面上所以 az 为 0。
  const rot = [
    t * ax * ax + cos,
    t * ax * ay,
    sin * ay,
    t * ax * ay,
    t * ay * ay + cos,
    -sin * ax,
    -sin * ay,
    sin * ax,
    cos,
  ]
  // 世界端左乘：新朝向 = 这一小段旋转 · 原来的朝向。
  prev.m = multiply3(rot, prev.m)
  return prev.m
}

// ===== 球面采样 =====

// context2d 取 2d 上下文。jsdom 没实现 canvas，getContext 会直接抛，
// 测试环境走到这里返回 null，渲染退回「不滚动的贴图」也不会崩。
function context2d(canvas: HTMLCanvasElement): CanvasRenderingContext2D | null {
  try {
    return canvas.getContext('2d')
  } catch {
    return null
  }
}

// TextureEntry 是一颗球反照率贴图的像素副本，按尺寸缓存。
interface TextureEntry {
  side: number
  data: ImageData
}

const textureDataCache = new Map<string, TextureEntry>()

// textureImage 取一颗球贴图的像素，供球面反投影采样用。
//
// 每换一次尺寸都要重新读：贴图本身按尺寸缓存，这里跟着它的节奏走。
function textureImage(id: string, side: number): ImageData | null {
  const cached = textureDataCache.get(id)
  if (cached && cached.side === side) {
    return cached.data
  }
  const ctx = context2d(ballTexture(id, side))
  if (!ctx) {
    return null
  }
  const data = ctx.getImageData(0, 0, side, side)
  textureDataCache.set(id, { side, data })
  return data
}

// SpriteEntry 缓存一颗球某个朝向的位图。m 用引用比较：
// 球没动时 rollMatrixOf 返回的就是同一个数组，直接复用不用重算。
//
// canvas 与 image 都是复用的：球滚动时每一帧都要重采样，如果每帧新建一张
// canvas，16 颗球一秒就是近千个画布对象，GC 一抖主线程就卡一下，
// 而 rAF 被卡住之后插值直接跳到后面几帧——看起来就是「球瞬移了」。
interface SpriteEntry {
  side: number
  m: number[]
  canvas: HTMLCanvasElement
  image: ImageData
}

const spriteCache = new Map<string, SpriteEntry>()

// 球面位图的边长上限（物理像素）。屏幕上球直径三十来 CSS 像素，超采样之后
// 到一百二三十物理像素，留一档余量；再往上是白算——每多一倍边长，
// 球面重采样的像素数就翻四倍。
const SPRITE_MAX_SIDE = 128

// 一帧最多重采样几颗球。超出的先用上一帧的位图，下一帧补上。
const SPRITE_FRAME_BUDGET = 8

// 预算轮转游标。每帧往后挪一批，避免固定那几颗球一直排在预算之外。
let spriteTurn = 0

// 明暗层缓存。左上一团亮、右下一圈收暗，再加一个死白高光点——
// 这一层只跟绘制半径有关，同一帧里所有球一模一样，没必要每颗各建一次
// 径向渐变再裁一次圆（十六颗球就是十六次裁剪填充，纯浪费）。
let shadeSide = 0
let shadeLayer: HTMLCanvasElement | null = null

// ballShade 取（必要时生成）一张边长为 side 的球面明暗层。
//
// 明暗按参考录屏的球重新标过：顶灯压在球的中上部，亮部是一大片柔和的亮区
// （最亮处偏白，约占球面三成），暗部集中在底部与右下，轮廓一圈压到本色五成
// 左右——不再是从前那块「左上死白、右下近黑」的塑料球。
//
// 高光单独用一层径向渐变画，不再用硬边椭圆：硬边椭圆在三十来像素的球上
// 只剩一个白点，看起来像贴片；渐变收边才是抛光树脂被灯照出来的那条亮带。
function ballShade(side: number): HTMLCanvasElement | null {
  if (shadeLayer && shadeSide === side) {
    return shadeLayer
  }
  const canvas = document.createElement('canvas')
  canvas.width = side
  canvas.height = side
  const ctx = context2d(canvas)
  if (!ctx) {
    return null
  }
  const c = side / 2

  // 裁成圆形再填：渐变在圆外会一直用最后一个色标，不裁的话四个角
  // 会带上最外圈的黑，贴到台上就是一个方块——这正是「进球时变成方框」的由来。
  ctx.beginPath()
  ctx.arc(c, c, c, 0, Math.PI * 2)
  ctx.clip()

  // 主体光照。亮心在球的正中偏上，而不是偏左上：录屏里十六颗球的高光
  // 都压在球顶（台灯是顶光），偏得太靠左会让球看起来在往左滚。
  //
  // 亮度分布是「中间略亮、边上明显暗」：球面法线越接近轮廓越背离光源，
  // 真实台球的轮廓一圈本来就暗，全靠这一圈才有体积。峰值只给 0.24 白，
  // 再多球色就被冲淡了——一号球会变成奶黄球、八号球发灰。
  const shade = ctx.createRadialGradient(
    c - c * 0.16,
    c - c * 0.34,
    c * 0.05,
    c,
    c * 1.0,
    c * 1.02,
  )
  shade.addColorStop(0, 'rgba(255, 255, 255, 0.24)')
  shade.addColorStop(0.26, 'rgba(255, 255, 255, 0.11)')
  shade.addColorStop(0.52, 'rgba(255, 255, 255, 0)')
  shade.addColorStop(0.72, 'rgba(0, 0, 0, 0.1)')
  shade.addColorStop(0.9, 'rgba(0, 0, 0, 0.32)')
  shade.addColorStop(1, 'rgba(0, 0, 0, 0.52)')
  ctx.fillStyle = shade
  ctx.fillRect(0, 0, side, side)

  // 高光带：球顶偏左的一条横亮带。参考录屏里十六颗球的高光都是横着铺开的
  // 一道弧，而不是一个圆点——球面把顶灯的圆形光斑横向拉长了。
  //
  // 做法是把坐标系竖直压扁到 0.72 再画圆形渐变：clip 的圆是在变换之前设的，
  // 不受影响。半径压到 0.3c：再大就和号码盘连成一片白，号码先糊掉。
  ctx.save()
  ctx.translate(c - c * 0.16, c - c * 0.42)
  ctx.scale(1.3, 0.72)
  const spec = ctx.createRadialGradient(0, 0, 0, 0, 0, c * 0.3)
  spec.addColorStop(0, 'rgba(255, 255, 255, 0.58)')
  spec.addColorStop(0.45, 'rgba(255, 255, 255, 0.2)')
  spec.addColorStop(1, 'rgba(255, 255, 255, 0)')
  ctx.fillStyle = spec
  ctx.fillRect(-side, -side, side * 2, side * 2)
  ctx.restore()

  // 底下贴台的那一条暗弧。球不是浮在台呢上的，接触点附近要压一点，
  // 否则球像一块圆形贴纸。用竖直渐变而不是整圈压暗：整圈会让球看起来
  // 套了个黑箍，而台面反光其实只挡得住下半边的光。
  const contact = ctx.createLinearGradient(0, c * 0.6, 0, side)
  contact.addColorStop(0, 'rgba(0, 0, 0, 0)')
  contact.addColorStop(1, 'rgba(0, 0, 0, 0.15)')
  ctx.fillStyle = contact
  ctx.fillRect(0, 0, side, side)

  shadeLayer = canvas
  shadeSide = side
  return canvas
}

// ballSprite 按当前朝向画一颗球的反照率位图。
//
// 球贴图是「从正上方看」的正交投影，所以反投影非常直接：
// 屏幕圆上某点 (nx, ny) 对应球面法线 n = (nx, ny, √(1-nx²-ny²))
// （观察坐标：x 向右、y 向下、z 朝观察者）。
// 换到球体局部 m = Mᵀn，贴图坐标就是 (m.x, m.y) —— 正交投影把球面点
// 直接落到贴图的同名位置，不用三角函数。
//
// m.z 小于 0 的点（球体局部的另一侧）同样用 (m.x, m.y)：
// 那个位置对应贴图上的镜像点，等于「另一极也印着号码」。
// 这里**不能**改成对跖点 (-m.x, -m.y)：那样在 m.z = 0 那一圈上
// 两侧坐标突然反号，画出来是一圈竖着的黑色锯齿（踩过）。
function ballSprite(ball: BallState, side: number, m: number[], rank: number): HTMLCanvasElement | null {
  const cached = spriteCache.get(ball.id)
  if (cached && cached.side === side && cached.m === m) {
    return cached.canvas
  }
  // 这一帧的预算轮不到它：沿用上一帧的朝向位图，最多晚一帧更新。
  // 宁可让号码慢一帧转，也不要为了它把整帧拖长。
  if (cached && cached.side === side && rank >= SPRITE_FRAME_BUDGET) {
    return cached.canvas
  }
  const source = textureImage(ball.id, side)
  if (!source) {
    return null
  }

  // 尺寸没变就沿用上一帧的画布与像素缓冲。圆外的像素永远不会被写，
  // 建的时候是全 0 就一直是全 0，不需要每帧清空。
  let canvas = cached && cached.side === side ? cached.canvas : null
  let image = cached && cached.side === side ? cached.image : null
  if (!canvas || !image) {
    canvas = document.createElement('canvas')
    canvas.width = side
    canvas.height = side
    const fresh = context2d(canvas)
    if (!fresh) {
      return null
    }
    image = fresh.createImageData(side, side)
  }
  const ctx = context2d(canvas)
  if (!ctx) {
    return null
  }
  const out = image.data
  const src = source.data
  const half = side / 2
  const last = side - 1

  for (let py = 0; py < side; py += 1) {
    const ny = (py + 0.5 - half) / half
    for (let px = 0; px < side; px += 1) {
      const nx = (px + 0.5 - half) / half
      const radiusSquared = nx * nx + ny * ny
      // 圆外的像素保持全透明：贴到画布上时会被 drawImage 直接丢掉，
      // 不会盖住旁边的台呢。
      if (radiusSquared > 1) {
        continue
      }
      const nz = Math.sqrt(1 - radiusSquared)
      // m = Mᵀ · n，行优先矩阵的转置就是「按列取」。
      const mx = m[0] * nx + m[3] * ny + m[6] * nz
      const my = m[1] * nx + m[4] * ny + m[7] * nz

      // 贴图半径对应 u = 1（球面赤道），先归一化再乘到像素上。
      const fx = (0.5 + mx * 0.5) * last
      const fy = (0.5 + my * 0.5) * last

      // 双线性采样。最近邻在球的边缘会出摩尔纹：
      // 那里的球面被压得很扁，相邻屏幕像素可能跨好几个贴图像素，
      // 号码盘的边缘就会碎成锯齿。四个像素加权只多几次乘加。
      const x0 = fx | 0
      const y0 = fy | 0
      const x1 = x0 < last ? x0 + 1 : last
      const y1 = y0 < last ? y0 + 1 : last
      const tx = fx - x0
      const ty = fy - y0
      const w00 = (1 - tx) * (1 - ty)
      const w10 = tx * (1 - ty)
      const w01 = (1 - tx) * ty
      const w11 = tx * ty
      const i00 = (y0 * side + x0) * 4
      const i10 = (y0 * side + x1) * 4
      const i01 = (y1 * side + x0) * 4
      const i11 = (y1 * side + x1) * 4
      const to = (py * side + px) * 4
      out[to] = src[i00] * w00 + src[i10] * w10 + src[i01] * w01 + src[i11] * w11
      out[to + 1] =
        src[i00 + 1] * w00 + src[i10 + 1] * w10 + src[i01 + 1] * w01 + src[i11 + 1] * w11
      out[to + 2] =
        src[i00 + 2] * w00 + src[i10 + 2] * w10 + src[i01 + 2] * w01 + src[i11 + 2] * w11
      out[to + 3] = 255
    }
  }

  ctx.putImageData(image, 0, 0)
  spriteCache.set(ball.id, { side, m, canvas, image })
  return canvas
}

// drawBall 画一颗球：贴一张按当前三维朝向重采样出来的球面，再叠一层
// 不随球转的光影，最后按邻居方向压接触阴影。
//
// 号码与花色必须跟着球滚，球才像在滚而不是一张贴纸在图面上平移。
// 但光影不能跟着转——台灯固定在台面上方，把高光烘焙进贴图再整体旋转，
// 看起来会像有人举着灯绕台子走。所以贴图只存反照率（见 balls.ts），
// 明暗与高光每次按屏幕方向补。
function drawBall(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  ball: BallState,
  radiusPx: number,
  textureSide: number,
  rank: number,
  contacts?: Contact[],
): void {
  const center = worldToScreen(t, ball.x, ball.y)
  const lift = metersToPx(t, ball.z)
  // 腾空时球体略放大：真实的透视下球离镜头更近。
  const drawRadius = radiusPx * (1 + ball.z * BALL_GROWTH)
  const cy = center.y - lift * 0.6

  // 朝向按这一帧的位移推进，再用它重采样球面。
  const m = rollMatrixOf(ball)
  const sprite = ballSprite(ball, textureSide, m, rank)
  // 贴图是按物理像素生成的，多数时候是 1:1 贴上去；只有球刚换尺寸、
  // 或者超采样倍率变了才会缩放。缩放时用高质量插值，球缘才不会出现
  // 一圈硬台阶——默认的 'low' 会在号码球的白边上一眼看出锯齿。
  ctx.imageSmoothingQuality = 'high'
  if (sprite) {
    ctx.drawImage(sprite, center.x - drawRadius, cy - drawRadius, drawRadius * 2, drawRadius * 2)
  } else {
    // 拿不到球面位图（测试环境没有 canvas 实现）：退回一张不动的贴图。
    // 位置仍然对，只是看不出滚动，比整帧空掉好。
    ctx.drawImage(
      ballTexture(ball.id, textureSide),
      center.x - drawRadius,
      cy - drawRadius,
      drawRadius * 2,
      drawRadius * 2,
    )
  }

  // 母球的红点画在光影之前：这样它们和球面一样会被压暗，不会像贴纸浮在上面。
  if (ball.id === 'cue') {
    drawCueDots(ctx, m, center.x, cy, drawRadius)
  }

  // 明暗层贴着球体尺寸取，圆外是透明的，贴上去不会盖住台呢。
  const shade = ballShade(Math.max(8, Math.round(textureSide * (drawRadius / radiusPx))))
  if (shade) {
    ctx.drawImage(shade, center.x - drawRadius, cy - drawRadius, drawRadius * 2, drawRadius * 2)
  }

  if (!contacts || contacts.length === 0) {
    return
  }
  const blob = contactShadowSprite()
  if (!blob) {
    return
  }
  // 暗斑必须裁在球内：球外的部分是台呢，压暗了会看到一圈脏边。
  ctx.save()
  ctx.beginPath()
  ctx.arc(center.x, cy, drawRadius, 0, Math.PI * 2)
  ctx.clip()
  const reach = drawRadius * 0.56
  for (const contact of contacts) {
    // 暗斑中心放在球缘上：完全放在轮廓外时，一半的暗落在圆内被裁掉，
    // 剩下那半只剩一条硬边；放在轮廓上，压出来的就是一条贴着球的窄暗缝。
    const px = center.x + contact.dx * drawRadius * 0.98
    const py = cy + contact.dy * drawRadius * 0.98
    ctx.drawImage(blob, px - reach, py - reach, reach * 2, reach * 2)
  }
  ctx.restore()
}

// 母球红点所在的球面方向：三根正交轴的六个极点，也就是上下左右前后。
//
// 只有六个，不取立方体的八个角：真实的练习白球就是六个点，八角看着像另一颗球。
//
// 这六个方向相对屏幕是「转过的」：球躺在台面上时朝向本来就是任意的，朝哪边
// 全看上一次怎么滚过来。这里固定取一个立方体对角线的朝向，是为了让它停在
// 开球点时能看见三颗点而不是一颗——若局部坐标轴与视线重合，四颗赤道点正好
// 落在轮廓上、一颗在背面，画面上只剩正中一个点，看着像掉了四个。
// 三根轴与视线的夹角都是 54.7°（nz = 0.577），三颗点落在离圆心 0.82R 处。
const CUE_DOTS: ReadonlyArray<readonly [number, number, number]> = [
  [0.8165, 0, 0.5774],
  [-0.8165, 0, -0.5774],
  [-0.4082, 0.7071, 0.5774],
  [0.4082, -0.7071, -0.5774],
  [-0.4082, -0.7071, 0.5774],
  [0.4082, 0.7071, -0.5774],
]

// 红点从哪个角度开始变淡、淡到没有。nz 是这颗点与视线夹角的余弦。
//
// 0.30 对应「离球心 0.95 个半径」：再往外这颗圆点就会跨过轮廓，被裁成一个
// 挂在球缘上的半圆——那正是玩家说的「白球四周一圈红」，只是以前是长弧、
// 现在换成半圆，一样难看。静止时三颗点都在 0.82R（nz 0.577），不受影响。
const CUE_DOT_FADE_START = 0.3
const CUE_DOT_FADE_SPAN = 0.18

// 点越靠近球缘画得越小：球面上的圆点投到屏幕上本来就是越靠边越扁，
// 这里用「缩小的正圆」近似那点透视，顺带让它不会顶到轮廓上。
const CUE_DOT_NEAR_SCALE = 0.6

// drawCueDots 在母球表面画那六颗红点。
//
// 以前红点是烘焙在贴图里的，跟着球面重采样一起滚。问题是贴图坐标等于球面
// 法线（u = sinθ）：一颗落在赤道附近的圆点，投到屏幕上会被径向压扁、贴着
// 球缘摊成一道弧，四颗赤道点连起来就是「母球四周镶了一圈红边」。
//
// 现在按球面朝向算出六颗点各自的屏幕位置，一颗一颗画正圆：
//   - n = M · d 把这颗点在球体坐标里的方向转到屏幕坐标；
//   - nz 太小的点在球缘那一圈，画出来是半圆，直接不收（见上面的阈值）；
//   - 越靠边越小，越靠边越淡。
// 红点仍然是滚动的参照物，但不会再糊成一条边。
function drawCueDots(
  ctx: CanvasRenderingContext2D,
  m: readonly number[],
  cx: number,
  cy: number,
  radius: number,
): void {
  const dotRadius = radius * CUE_DOT_SCALE
  ctx.save()
  // 裁在球内：贴边的点会被轮廓切掉一块，看起来才像画在球面上。
  ctx.beginPath()
  ctx.arc(cx, cy, radius, 0, Math.PI * 2)
  ctx.clip()
  ctx.fillStyle = CUE_DOT_COLOR
  for (const dot of CUE_DOTS) {
    const nx = m[0] * dot[0] + m[1] * dot[1] + m[2] * dot[2]
    const ny = m[3] * dot[0] + m[4] * dot[1] + m[5] * dot[2]
    const nz = m[6] * dot[0] + m[7] * dot[1] + m[8] * dot[2]
    if (nz <= CUE_DOT_FADE_START) {
      continue
    }
    ctx.globalAlpha = Math.min(1, (nz - CUE_DOT_FADE_START) / CUE_DOT_FADE_SPAN)
    const shrink = CUE_DOT_NEAR_SCALE + (1 - CUE_DOT_NEAR_SCALE) * nz
    ctx.beginPath()
    ctx.arc(cx + nx * radius, cy + ny * radius, dotRadius * shrink, 0, Math.PI * 2)
    ctx.fill()
  }
  ctx.restore()
}

// 接触阴影用的暗斑。做成一张 64 像素的固定位图反复缩放，
// 而不是每颗球每个邻居都 createRadialGradient：一帧最多三四十次，
// 建渐变比贴一张图贵一个量级，手机上能省出半毫秒。
let contactBlob: HTMLCanvasElement | null = null

function contactShadowSprite(): HTMLCanvasElement | null {
  if (contactBlob) {
    return contactBlob
  }
  const side = 64
  const canvas = document.createElement('canvas')
  canvas.width = side
  canvas.height = side
  const ctx = context2d(canvas)
  if (!ctx) {
    return null
  }
  const c = side / 2
  const g = ctx.createRadialGradient(c, c, 0, c, c, c)
  g.addColorStop(0, 'rgba(0, 0, 0, 0.42)')
  g.addColorStop(0.5, 'rgba(0, 0, 0, 0.18)')
  g.addColorStop(1, 'rgba(0, 0, 0, 0)')
  ctx.fillStyle = g
  ctx.fillRect(0, 0, side, side)
  contactBlob = canvas
  return canvas
}

// 落袋动画的时长与状态。
//
// 帧里球是一瞬间消失的，直接照抄会看到球「咔」地不见。这里让它按帧里最后
// 的位置往最近的袋口滚进去，同时缩小变淡，和真实落袋的观感接近。
//
// 时长不再是一个固定值：球进袋那一刻还在以多快跑，就用多快走完最后这一段
// （见 trackPotted 里的估算）。固定 340ms 的问题是把快球硬拖慢——玩家看到的
// 就是「球到袋口突然被吸进去」。
const SINK_MS_MAX = 320
const SINK_MS_MIN = 90

interface SinkState {
  fromX: number
  fromY: number
  toX: number
  toY: number
  at: number
  // duration 是按「球消失前的速度」算出来的这一段该走多久。
  duration: number
}

const sinking = new Map<string, SinkState>()
// 上一帧还在台面上的球号。用它判断这一帧谁刚掉袋。
let visibleIds = new Set<string>()
// 上一帧每颗球的位置与时刻，用来估「消失前还在以多快跑」。
const lastSeen = new Map<string, { x: number; y: number; at: number }>()

// nearestPocket 返回离给定点最近的袋口中心（世界坐标）。
function nearestPocket(x: number, y: number): { x: number; y: number } {
  let best = POCKETS[0]
  let bestDistance = Number.POSITIVE_INFINITY
  for (const pocket of POCKETS) {
    const d = (pocket.x - x) * (pocket.x - x) + (pocket.y - y) * (pocket.y - y)
    if (d < bestDistance) {
      bestDistance = d
      best = pocket
    }
  }
  return { x: best.x, y: best.y }
}

// trackPotted 在每次绘制前比对「谁不在了」，给刚落袋的球起一段动画。
//
// 球不在采样结果里时 applySample 只会把 on_table 置 false，坐标留着最后
// 一帧的位置，正好拿来当动画起点。
function trackPotted(balls: ReadonlyArray<BallState>): void {
  const now = performance.now()
  const current = new Set<string>()
  for (const ball of balls) {
    if (ball.on_table) {
      current.add(ball.id)
    }
  }

  for (const id of visibleIds) {
    if (current.has(id)) {
      continue
    }
    const ball = findBallById(balls, id)
    if (!ball) {
      continue
    }
    const pocket = nearestPocket(ball.x, ball.y)
    sinking.set(id, {
      fromX: ball.x,
      fromY: ball.y,
      toX: pocket.x,
      toY: pocket.y,
      at: now,
      duration: sinkDuration(ball, pocket, lastSeen.get(id), now),
    })
  }
  visibleIds = current

  // 记下这一帧每颗球的位置，下一帧有人消失时才有速度可用。
  // 只记还在台面上的：球消失之后它停在原地，留着会把速度算成 0。
  for (const ball of balls) {
    if (ball.on_table) {
      lastSeen.set(ball.id, { x: ball.x, y: ball.y, at: now })
    } else {
      lastSeen.delete(ball.id)
    }
  }

  // 播完的一次性清掉，别让这张表跟着一局局对局长下去。
  for (const [id, state] of sinking) {
    if (now - state.at > state.duration) {
      sinking.delete(id)
    }
  }
}

// sinkDuration 按球消失前的速度算落袋动画该走多久。
//
// 距离是「最后一帧位置 → 袋口中心」，速度取上一帧到这一帧的位移除以间隔。
// 两者一除就是「以原来的速度滚完最后这一段」所需的时间：快球几十毫秒就没了，
// 慢球才在袋口磨一下。估不出速度（只有一帧数据、或者球本来就几乎不动）时
// 退回一个中速的固定值。
function sinkDuration(
  ball: BallState,
  pocket: { x: number; y: number },
  prev: { x: number; y: number; at: number } | undefined,
  now: number,
): number {
  const distance = Math.hypot(pocket.x - ball.x, pocket.y - ball.y)
  if (!prev || now <= prev.at) {
    return SINK_MS_MAX
  }
  const seconds = (now - prev.at) / 1000
  const moved = Math.hypot(ball.x - prev.x, ball.y - prev.y)
  if (seconds <= 0 || moved < 0.002) {
    return SINK_MS_MAX
  }
  const speed = moved / seconds
  const wanted = (distance / speed) * 1000
  if (wanted < SINK_MS_MIN) {
    return SINK_MS_MIN
  }
  if (wanted > SINK_MS_MAX) {
    return SINK_MS_MAX
  }
  return wanted
}

// findBallById 不过在台面上与否，按球号找一份状态。
function findBallById(balls: ReadonlyArray<BallState>, id: string): BallState | null {
  for (const ball of balls) {
    if (ball.id === id) {
      return ball
    }
  }
  return null
}

// resetBallMotion 清空滚动角度与落袋动画的缓存。
//
// 这两份状态是模块级的（球的转动只跟路径有关，从当前位置推不出来），
// 同一次会话里必须跨帧累积才对。代价是测试之间会互相影响：
// 上一帧的残留会让「空球局」也画出两颗正在往袋里滚的球，所以用例要先调它。
export function resetBallMotion(): void {
  rollState.clear()
  sinking.clear()
  visibleIds = new Set<string>()
  lastSeen.clear()
  // 球面位图是按「球号 + 朝向」缓存的，朝向表清了它也就没意义了；
  // 不清的话下一局的球会沿用上一局的朝向重新起算，看起来像被谁转过一圈。
  spriteCache.clear()
}

// sinkingActive 表示「还有球在往袋里滚」。
//
// 调用方（PoolTable）要靠它决定要不要继续要下一帧：落袋动画是唯一
// 不依赖外部输入、只随时间推进的动画，没有这个信号就没人再调度绘制，
// 画面停在被判落袋的那一帧上——那颗球会一直挂在袋口不消失。
export function sinkingActive(): boolean {
  return sinking.size > 0
}

// drawSinking 画还在往袋口滚的球。
//
// 进度是线性的，不再用平方曲线。平方曲线就是「越靠近袋口越快」，
// 也就是玩家说的「像被吸进去」：球明明已经慢下来了，最后那一下却突然加速。
// 时长既然按消失前的速度算好了，匀速走完这一段才是「沿着原来的方向滑进去」。
function drawSinking(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  radiusPx: number,
  dpr: number,
): void {
  if (sinking.size === 0) {
    return
  }
  const now = performance.now()
  const textureSide = Math.min(SPRITE_MAX_SIDE, Math.round(radiusPx * 2 * dpr))

  for (const [id, state] of sinking) {
    const progress = Math.min(1, (now - state.at) / state.duration)
    // 位移线性；缩小与淡出保留一点加速度：球是往下掉进洞里的，
    // 观感上最后一段确实该「沉」下去，但那靠的是缩放，不是让它横着飞。
    const eased = progress * progress
    const x = state.fromX + (state.toX - state.fromX) * progress
    const y = state.fromY + (state.toY - state.fromY) * progress
    const center = worldToScreen(t, x, y)
    const radius = radiusPx * (1 - 0.55 * eased)

    // 用球面位图而不是原始贴图：贴图是正方形的一张反照率，直接 drawImage
    // 出去就是一个方块——球一落袋就变成方框，正是这么来的。
    const surface: BallState = { id, x, y, z: 0, on_table: true }
    const roll = rollState.get(id)
    const sprite = ballSprite(surface, textureSide, roll ? roll.m : IDENTITY, 0)
    if (!sprite) {
      continue
    }

    ctx.save()
    ctx.globalAlpha = 1 - 0.8 * progress
    ctx.drawImage(sprite, center.x - radius, center.y - radius, radius * 2, radius * 2)
    const shade = ballShade(Math.max(8, Math.round(textureSide * (radius / radiusPx))))
    if (shade) {
      ctx.drawImage(shade, center.x - radius, center.y - radius, radius * 2, radius * 2)
    }
    ctx.restore()
  }
}

// roundRectPath 手写圆角矩形路径。
// 不用 ctx.roundRect：它在 Safari 15 之前不存在，而本项目没上 polyfill，
// 直接调用会让整个对局页在旧机型上白屏。
function roundRectPath(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  width: number,
  height: number,
  radius: number,
): void {
  ctx.beginPath()
  addRoundRect(ctx, x, y, width, height, radius)
}

// addRoundRect 往当前路径里追加一个圆角矩形，自己不开新路径。
//
// 护角的裁剪要把「木框 + 库边轮廓」凑成一条 evenodd 路径，用 roundRectPath
// 会把前面的子路径冲掉，所以这里单独留一个只追加的版本。
function addRoundRect(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  width: number,
  height: number,
  radius: number,
): void {
  const r = Math.min(radius, width / 2, height / 2)
  ctx.moveTo(x + r, y)
  ctx.lineTo(x + width - r, y)
  ctx.arcTo(x + width, y, x + width, y + r, r)
  ctx.lineTo(x + width, y + height - r)
  ctx.arcTo(x + width, y + height, x + width - r, y + height, r)
  ctx.lineTo(x + r, y + height)
  ctx.arcTo(x, y + height, x, y + height - r, r)
  ctx.lineTo(x, y + r)
  ctx.arcTo(x, y, x + r, y, r)
  ctx.closePath()
}
