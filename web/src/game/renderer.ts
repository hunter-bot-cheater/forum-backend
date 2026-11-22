// 台面渲染。只负责「把一份球位画到 canvas 上」，不持有状态、不监听事件。
//
// 分层顺序是固定的：木框 → 库边 → 台呢 → 袋口 → 瞄准线 → 球影 → 球。
// 顺序错一层就会出现「球被库边压住」或者「袋口被台呢盖住」这种一眼假的画面，
// 所以整帧一次画完，不做局部重绘——16 颗球的完整重绘在手机上也只有 1ms 左右。
import { ballTexture } from './balls'
import { fitTable, metersToPx, worldToScreen, type ViewTransform, type Viewport } from './geometry'
import { BALL_RADIUS, CUSHION_WIDTH, POCKETS, RAIL_WIDTH, TABLE_LENGTH, TABLE_WIDTH } from './table'
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

// 整帧用到的颜色。取值照着参考视频取：外框是近黑的石墨灰而不是棕色木纹，
// 台呢是偏亮的草绿并在中间有一块受光，库边压暗一个档，袋口是纯黑的洞。
const COLORS = {
  rail: '#33383f',
  railEdge: '#0a0c10',
  cloth: '#1f8a58',
  clothLight: '#2aa267',
  cushion: '#0a4a2d',
  pocket: '#04060a',
  // 库边上的菱形瞄准点：视频里那排小白点，没有它外框像一块塑料板。
  diamond: 'rgba(240, 244, 250, 0.92)',
  shadow: 'rgba(0, 0, 0, 0.26)',
  clothLine: 'rgba(255, 255, 255, 0.08)',
}

// 球按物理尺寸再放大一点画。
//
// 手机上球只有十几像素，绿呢又亮，按真实比例画出来像撒了一把彩点；参考视频里
// 球径占台面宽度的 7.2%，而真实球径只占 5.8%，主流台球手游都在 1.2 倍上下。
// 只放大「画」出来的半径，物理坐标与判定一动都不动，所以球挤在一起时会有
// 一点视觉重叠——这正是这类 App 看起来「球大好打」的来源，不必去修。
export const BALL_DRAW_SCALE = 1.2

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
}

// Scene 是一帧要画的全部内容。以后加球杆、击球点提示也放这里，
// 保持「渲染只读输入」这条边界。
export interface Scene {
  balls: ReadonlyArray<BallState>
  aim?: AimLayer | null
  // ghost 是手中球（自由球）的预览位置，世界坐标。只画半透明的虚影，
  // 因为它还没真的落位——落位由服务端确认，之后才作为一颗正常的球进 balls。
  ghost?: { x: number; y: number } | null
}

// drawScene 画完整一帧。view 是 CSS 像素尺寸，dpr 只用来取高清球贴图。
//
// 调用方负责先把 ctx 按 dpr 缩放好（PoolTable.vue 里做），
// 这里全部按 CSS 像素算，避免两处都乘 dpr 导致画面放大四倍。
export function drawScene(
  ctx: CanvasRenderingContext2D,
  view: Viewport,
  scene: Scene,
  dpr: number,
): void {
  const t = fitTable(view, TABLE_PADDING)

  ctx.clearRect(0, 0, view.width, view.height)
  drawRail(ctx, t)
  drawCushion(ctx, t)
  drawCloth(ctx, t)
  drawPockets(ctx, t)
  if (scene.aim) {
    drawAim(ctx, t, scene.aim)
    drawCue(ctx, t, scene.aim)
  }
  drawBalls(ctx, t, scene.balls, dpr)
  // 虚影画在真球之上：拖动时它会压过别的球，被压住就看不出落在哪了。
  if (scene.ghost) {
    drawGhostBall(ctx, t, scene.ghost)
  }
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

// drawRail 画外框与库边上的菱形瞄准点。
function drawRail(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const rect = railRect(t)

  ctx.save()
  ctx.shadowColor = 'rgba(0, 0, 0, 0.5)'
  ctx.shadowBlur = 16
  ctx.shadowOffsetY = 6
  ctx.fillStyle = COLORS.rail
  roundRectPath(ctx, rect.x, rect.y, rect.width, rect.height, RAIL_RADIUS)
  ctx.fill()
  ctx.restore()

  // 内圈压一条深色线，外框才有厚度感，不至于像一块纯色板。
  ctx.strokeStyle = COLORS.railEdge
  ctx.lineWidth = 2
  roundRectPath(ctx, rect.x + 1, rect.y + 1, rect.width - 2, rect.height - 2, RAIL_RADIUS - 1)
  ctx.stroke()

  drawRailDiamonds(ctx, t)
}

// drawRailDiamonds 画库边上那排白色菱形标记。
//
// 位置按真实台面的分点来：长库各 7 个（中袋那个位置空着），短库各 3 个。
// 它们除了好看还有一个用处——瞄准时能拿它当角度参考。
function drawRailDiamonds(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const size = Math.max(2.4, metersToPx(t, MARGIN) * 0.16)
  const inset = MARGIN / 2

  const spots: Array<{ x: number; y: number }> = []
  for (let i = 1; i <= 7; i += 1) {
    if (i === 4) {
      continue
    }
    const y = (TABLE_LENGTH * i) / 8
    spots.push({ x: -inset, y })
    spots.push({ x: TABLE_WIDTH + inset, y })
  }
  for (let i = 1; i <= 3; i += 1) {
    const x = (TABLE_WIDTH * i) / 4
    spots.push({ x, y: -inset })
    spots.push({ x, y: TABLE_LENGTH + inset })
  }

  ctx.fillStyle = COLORS.diamond
  for (const spot of spots) {
    const center = worldToScreen(t, spot.x, spot.y)
    ctx.beginPath()
    ctx.moveTo(center.x, center.y - size * 0.8)
    ctx.lineTo(center.x + size * 0.75, center.y)
    ctx.lineTo(center.x, center.y + size * 0.8)
    ctx.lineTo(center.x - size * 0.75, center.y)
    ctx.closePath()
    ctx.fill()
  }
}

// drawCushion 画库边，范围是台呢外沿再加一圈 CUSHION_WIDTH。
//
// 用整块矩形而不是按段画折线：折线在袋口处的断点会露出台呢的尖角，
// 看起来像库边被啃掉一块。整块深绿铺底、再用袋口圆去盖，
// 得到的就是「库边连续、袋口是一个洞」这个真实结构。
function drawCushion(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const rect = worldRect(
    t,
    -CUSHION_WIDTH,
    -CUSHION_WIDTH,
    TABLE_WIDTH + CUSHION_WIDTH,
    TABLE_LENGTH + CUSHION_WIDTH,
  )
  ctx.fillStyle = COLORS.cushion
  ctx.fillRect(rect.x, rect.y, rect.width, rect.height)
}

// drawCloth 画台呢。矩形范围是库边内侧，也就是世界坐标 0..宽、0..长。
function drawCloth(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const rect = worldRect(t, 0, 0, TABLE_WIDTH, TABLE_LENGTH)
  ctx.fillStyle = COLORS.cloth
  ctx.fillRect(rect.x, rect.y, rect.width, rect.height)

  // 中间偏上打一块亮，边缘压暗。整片纯色会像一块绿色卡纸，
  // 有一点受光才有「这张台子铺了布、灯在上面」的感觉。
  const light = ctx.createRadialGradient(
    rect.x + rect.width * 0.5,
    rect.y + rect.height * 0.44,
    0,
    rect.x + rect.width * 0.5,
    rect.y + rect.height * 0.44,
    Math.max(rect.width, rect.height) * 0.9,
  )
  light.addColorStop(0, COLORS.clothLight)
  light.addColorStop(0.5, 'rgba(42, 162, 103, 0.55)')
  light.addColorStop(1, 'rgba(42, 162, 103, 0)')
  ctx.fillStyle = light
  ctx.fillRect(rect.x, rect.y, rect.width, rect.height)

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
}

// drawPockets 画六个袋口。
//
// 袋口圆有一半落在台面之外，中袋的圆甚至会伸到木框之外。整帧裁剪到木框范围内，
// 木框的外轮廓才是完整的——不裁的话木框中间会多出两个黑圆，
// 看起来像木头被打了两个洞。
function drawPockets(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const rail = railRect(t)

  ctx.save()
  roundRectPath(ctx, rail.x, rail.y, rail.width, rail.height, RAIL_RADIUS)
  ctx.clip()

  for (const p of POCKETS) {
    const center = worldToScreen(t, p.x, p.y)
    const radius = metersToPx(t, p.radius)

    ctx.beginPath()
    ctx.arc(center.x, center.y, radius, 0, Math.PI * 2)
    ctx.fillStyle = COLORS.pocket
    ctx.fill()

    // 袋口内圈一圈灰边，模拟皮口的倒角。不画只会是一个纯黑贴纸；
    // 参考视频里这一圈很明显，袋口才像「有皮子包着的洞」。
    ctx.beginPath()
    ctx.arc(center.x, center.y, radius * 0.82, 0, Math.PI * 2)
    ctx.strokeStyle = 'rgba(198, 205, 214, 0.22)'
    ctx.lineWidth = Math.max(1, radius * 0.1)
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
  ctx.save()
  ctx.setLineDash([5, 7])
  ctx.lineWidth = 1.6
  ctx.strokeStyle = 'rgba(255, 233, 176, 0.85)'
  ctx.beginPath()
  ctx.moveTo(startX, startY)
  ctx.lineTo(to.x, to.y)
  ctx.stroke()
  ctx.restore()

  // 幽灵球：先铺一层很淡的白，再勾一圈边。
  // 只勾边不看不清，只铺一层又会和真球混起来，两层一起才像「这里会停一颗球」。
  if (aim.hit.kind === 'ball') {
    ctx.beginPath()
    ctx.arc(to.x, to.y, radiusPx, 0, Math.PI * 2)
    ctx.fillStyle = 'rgba(255, 255, 255, 0.14)'
    ctx.fill()
    ctx.strokeStyle = 'rgba(255, 246, 220, 0.9)'
    ctx.lineWidth = 1.6
    ctx.stroke()
  }
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
  const textureSide = Math.round(radiusPx * 2 * dpr)
  for (const ball of balls) {
    if (!ball.on_table) {
      continue
    }
    const center = worldToScreen(t, ball.x, ball.y)
    const lift = metersToPx(t, ball.z)
    // 腾空时球体略放大：真实的透视下球离镜头更近。
    const drawRadius = radiusPx * (1 + ball.z * BALL_GROWTH)
    const texture = ballTexture(ball.id, textureSide)

    ctx.drawImage(
      texture,
      center.x - drawRadius,
      center.y - drawRadius - lift * 0.6,
      drawRadius * 2,
      drawRadius * 2,
    )
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
  const r = Math.min(radius, width / 2, height / 2)
  ctx.beginPath()
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
