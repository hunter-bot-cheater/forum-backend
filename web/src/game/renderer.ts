// 台面渲染。只负责「把一份球位画到 canvas 上」，不持有状态、不监听事件。
//
// 分层顺序是固定的：木框 → 库边 → 台呢 → 袋口 → 球影 → 球。
// 顺序错一层就会出现「球被库边压住」或者「袋口被台呢盖住」这种一眼假的画面，
// 所以整帧一次画完，不做局部重绘——16 颗球的完整重绘在手机上也只有 1ms 左右。
import { ballTexture } from './balls'
import { fitTable, metersToPx, worldToScreen, type ViewTransform, type Viewport } from './geometry'
import { BALL_RADIUS, CUSHION_WIDTH, POCKETS, TABLE_LENGTH, TABLE_WIDTH } from './table'
import type { BallState } from './types'

// 木框在库边之外再向外加这么厚（米）。太薄看不出是木头，太厚会把球台挤小。
const RAIL_EXTRA = 0.055

// 四个角的圆角半径（CSS 像素）。木框有圆角像一张真台子，直角会像色块。
const RAIL_RADIUS = 12

// 台面四周至少留这么多像素，给木框的阴影一点空间。
export const TABLE_PADDING = 10

// 整帧用到的颜色。绿色按明度分三档：台呢最亮、库边次之、袋口最暗，
// 这样即使不画轮廓线也能分出层次。
const COLORS = {
  rail: '#4a3222',
  railEdge: '#2c1d12',
  cloth: '#1d6b46',
  cushion: '#14503a',
  pocket: '#0d1117',
  shadow: 'rgba(0, 0, 0, 0.26)',
  clothLine: 'rgba(255, 255, 255, 0.08)',
}

// Scene 是一帧要画的全部内容。以后加瞄准线、幽灵球也放这里，
// 保持「渲染只读输入」这条边界。
export interface Scene {
  balls: ReadonlyArray<BallState>
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
  drawBalls(ctx, t, scene.balls, dpr)
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

// drawRail 画木质外框，范围是库边再向外扩 RAIL_EXTRA。
function drawRail(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const outer = CUSHION_WIDTH + RAIL_EXTRA
  const rect = worldRect(t, -outer, -outer, TABLE_WIDTH + outer, TABLE_LENGTH + outer)

  ctx.save()
  ctx.shadowColor = 'rgba(0, 0, 0, 0.24)'
  ctx.shadowBlur = 14
  ctx.shadowOffsetY = 4
  ctx.fillStyle = COLORS.rail
  roundRectPath(ctx, rect.x, rect.y, rect.width, rect.height, RAIL_RADIUS)
  ctx.fill()
  ctx.restore()

  // 内圈压一条深色线，木框才有厚度感，不至于像一块纯色板。
  ctx.strokeStyle = COLORS.railEdge
  ctx.lineWidth = 2
  roundRectPath(ctx, rect.x + 1, rect.y + 1, rect.width - 2, rect.height - 2, RAIL_RADIUS - 1)
  ctx.stroke()
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

  // 开球线与置球点：真实台面上这两条线一直在，缺了会显得台面是空的。
  // 开球线在母球那一侧（世界 y = 1/4 长），置球点在球堆那一侧（3/4 长）。
  ctx.strokeStyle = COLORS.clothLine
  ctx.lineWidth = 1
  const headLine = worldToScreen(t, 0, TABLE_LENGTH * 0.25)
  ctx.beginPath()
  ctx.moveTo(rect.x, headLine.y)
  ctx.lineTo(rect.x + rect.width, headLine.y)
  ctx.stroke()

  const spot = worldToScreen(t, TABLE_WIDTH / 2, TABLE_LENGTH * 0.75)
  ctx.beginPath()
  ctx.arc(spot.x, spot.y, Math.max(1.5, metersToPx(t, BALL_RADIUS) * 0.16), 0, Math.PI * 2)
  ctx.fillStyle = COLORS.clothLine
  ctx.fill()
}

// drawPockets 画六个袋口。圆本身有一半落在台面外，正好形成开口。
// 由外向内两层：先铺近黑的洞，再叠一圈更暗的收口，袋口才有「深下去」的感觉。
function drawPockets(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  for (const p of POCKETS) {
    const center = worldToScreen(t, p.x, p.y)
    const radius = metersToPx(t, p.radius)

    ctx.beginPath()
    ctx.arc(center.x, center.y, radius, 0, Math.PI * 2)
    ctx.fillStyle = COLORS.pocket
    ctx.fill()

    // 边缘一圈极淡的亮边，模拟袋口倒角。不画会更像一个纯黑贴纸。
    ctx.beginPath()
    ctx.arc(center.x, center.y, radius * 0.86, 0, Math.PI * 2)
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.07)'
    ctx.lineWidth = Math.max(1, radius * 0.08)
    ctx.stroke()
  }
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
  const radiusPx = metersToPx(t, BALL_RADIUS)

  ctx.save()
  ctx.fillStyle = COLORS.shadow
  for (const ball of balls) {
    if (!ball.on_table) {
      continue
    }
    const center = worldToScreen(t, ball.x, ball.y)
    // 球飞起来（z > 0）时影子离球心更远、也更淡，这是跳球唯一的 2D 线索。
    const lift = metersToPx(t, ball.z)
    ctx.globalAlpha = Math.max(0.35, 1 - ball.z * 6)
    ctx.beginPath()
    ctx.ellipse(
      center.x + lift * 0.35 + radiusPx * 0.12,
      center.y + lift * 0.5 + radiusPx * 0.18,
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
    // 跳球时球体略微放大：真实的透视下球离镜头更近，2D 里用尺寸变化代替。
    const drawRadius = radiusPx * (1 + ball.z * 1.6)
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
