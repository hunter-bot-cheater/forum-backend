// 台面渲染。只负责「把一份球位画到 canvas 上」，不持有状态、不监听事件。
//
// 分层顺序是固定的：木框 → 库边 → 台呢 → 袋口 → 瞄准线 → 球影 → 球。
// 顺序错一层就会出现「球被库边压住」或者「袋口被台呢盖住」这种一眼假的画面，
// 所以整帧一次画完，不做局部重绘——16 颗球的完整重绘在手机上也只有 1ms 左右。
import type { AimGuides } from './aim'
import { ballTexture } from './balls'
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
  type PocketMouth,
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

// 整帧用到的颜色。取值照着参考视频取：外框是近黑的石墨灰而不是棕色木纹，
// 台呢是偏亮的草绿并在中间有一块受光，库边压暗一个档，袋口是纯黑的洞。
const COLORS = {
  rail: '#33383f',
  railEdge: '#0a0c10',
  cloth: '#1f8a58',
  clothLight: '#2aa267',
  // 库边是深灰而不是深绿：参考录屏里那圈是橡皮（黑灰），绿色只属于台呢。
  // 早先用深绿，袋口一切开就分不出「哪块是布、哪块是库边」。
  cushion: '#232a2e',
  pocket: '#04060a',
  // 库边上的菱形瞄准点：视频里那排小白点，没有它外框像一块塑料板。
  diamond: 'rgba(240, 244, 250, 0.92)',
  shadow: 'rgba(0, 0, 0, 0.26)',
  clothLine: 'rgba(255, 255, 255, 0.08)',
}

// 球按物理尺寸画，不做视觉放大。
//
// 早先放大到 1.2 倍是嫌手机上的球小，代价是球心距正好等于两个半径时，
// 画面上也会叠掉 17%：物理算得再准，玩家看到的都是「球穿在一起了」，
// 只能反过来怀疑碰撞体积。台面上球与球台的尺寸关系是打台球的核心信息，
// 眼睛量到的距离必须等于物理量到的距离，所以这里固定为 1。
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
// 而它偏偏是最贵的那部分（台呢两层全屏渐变、六个袋口各自四层填充加裁剪）。
// 拖动瞄准时每帧三十毫秒里有二十多毫秒耗在这里，手感就是「拖杆发涩」。
export function drawTable(ctx: CanvasRenderingContext2D, view: Viewport): void {
  const t = fitTable(view, TABLE_PADDING)
  drawRail(ctx, t)
  drawCushion(ctx, t)
  drawCloth(ctx, t)
  drawPockets(ctx, t)
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

// 袋兜底色的几档。真实袋口不是一个纯黑的洞：靠观察者这一侧的内壁是
// 深棕色的皮革（参考素材里那圈暗红），越往里越黑，到最深处几乎没有光。
const POCKET_VOID = '#12151a'
const POCKET_DEEP = '#04060a'
const POCKET_LEATHER = '#2c1a15'

// 护角（袋口的金属包角）配色。
//
// 银灰铸铝：靠上的棱受光、朝下的面压暗。三个色阶都压在中灰附近——
// 调到近白会像贴了一块塑料片，压到近黑或深棕又分不出是金属。
// 上一版取 #ccd2d8，画出来的是一圈白塑料，这里整体压掉一档，
// 亮面只留给受光的那条棱（见 drawCollar 里的内棱高光）。
const PLATE_LIGHT = '#a4abb3'
const PLATE_MID = '#7f868e'
const PLATE_DARK = '#565c63'

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

// drawPockets 画六个袋口。
//
// 袋口的形状全部取自 table.ts 的 POCKET_MOUTHS：物理那边用同一份点生成
// 碰撞线段，所以画出来的 45° 斜边就是真的能撞到的斜面。自己再算一套
// 缺口边界，两边差几毫米就会出现「看着擦着袋口过去，球却弹了回来」。
//
// 五层叠出来，少一层就「看不出球从哪边能进」：
//   1) 袋兜   —— 颊板与兜底围出来的 V 形暗面，也就是被切掉的那块库边与台呢；
//   2) 护角   —— 压在洞口与库边交界上的金属压边；
//   3) 斜边   —— 两条 45° 颊板的斜面与顶棱高光，裁在袋兜里画。
//               压在护角之后：袋喉是从金属压边中间穿过去的，先画金属再画喉，
//               压边才不会被斜面盖成一块缺口；
//   4) 圆洞   —— 圆心与半径直接取 POCKETS，与落袋判定是同一个圆。
function drawPockets(ctx: CanvasRenderingContext2D, t: ViewTransform): void {
  const rail = railRect(t)
  const cloth = worldRect(t, 0, 0, TABLE_WIDTH, TABLE_LENGTH)

  ctx.save()
  roundRectPath(ctx, rail.x, rail.y, rail.width, rail.height, RAIL_RADIUS)
  ctx.clip()

  for (let i = 0; i < POCKETS.length; i += 1) {
    const p = POCKETS[i]
    const mouth = POCKET_MOUTHS[i]
    const center = worldToScreen(t, p.x, p.y)
    const radius = metersToPx(t, p.radius)

    drawMouth(ctx, t, mouth, p, center, radius)
    drawCollar(ctx, p, center, radius, rail, cloth)
    drawJawFaces(ctx, t, mouth, center, radius)
    drawJawBevels(ctx, t, mouth, p, radius)
    drawHole(ctx, p, center, radius, p.kind === 'side' ? cloth : null)
  }

  ctx.restore()
}

// mouthPath 铺一条袋口张口的闭合路径（四个顶点直接取 POCKET_MOUTHS）。
//
// 袋兜的暗面、两条斜面、顶棱高光都裁在这条路径里：袋喉只存在于张口内部，
// 出了张口就该是护角的金属面，不然斜面会顺着颊板爬到库边上。
function mouthPath(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  mouth: PocketMouth,
): void {
  const a = worldToScreen(t, mouth.noseA.x, mouth.noseA.y)
  const b = worldToScreen(t, mouth.jawA.x, mouth.jawA.y)
  const c = worldToScreen(t, mouth.jawB.x, mouth.jawB.y)
  const d = worldToScreen(t, mouth.noseB.x, mouth.noseB.y)

  ctx.beginPath()
  ctx.moveTo(a.x, a.y)
  ctx.lineTo(b.x, b.y)
  ctx.lineTo(c.x, c.y)
  ctx.lineTo(d.x, d.y)
  ctx.closePath()
}

// drawMouth 铺 V 形袋兜：颊板、兜底、两个鼻尖围起来的那块暗面。
//
// 这块是「库边被切掉」之后露出来的东西——袋口的内部，比周围任何东西都暗。
// 渐变圆心放在袋心上，远端偏一点皮革色，看起来像有深度的布兜而不是一块黑漆。
function drawMouth(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  mouth: PocketMouth,
  p: Pocket,
  center: { x: number; y: number },
  radius: number,
): void {
  mouthPath(ctx, t, mouth)

  const out = outwardScreen(p)
  const fill = ctx.createRadialGradient(
    center.x - out.x * radius * 0.5,
    center.y - out.y * radius * 0.5,
    radius * 0.2,
    center.x,
    center.y,
    radius * 2.2,
  )
  fill.addColorStop(0, POCKET_VOID)
  fill.addColorStop(0.55, POCKET_DEEP)
  fill.addColorStop(1, POCKET_LEATHER)
  ctx.fillStyle = fill
  ctx.fill()
}

// drawJawFaces 沿两条 45° 颊板铺一条斜面：台呢 → 斜向下的橡胶面 → 黑洞。
//
// 光有 drawMouth 那块 V 形暗面时，台呢是以一条笔直的对角线直接落进纯黑的，
// 看着像桌布被人剪掉一角。真实袋口的颊板是一小块斜着立起来的橡胶：靠上的
// 那条棱被灯照着（drawJawBevels 画的高光），往下滚进袋兜之前还有一段灰度。
// 这一段补的就是那条可见的过渡带，宽度取 0.42r——再宽斜面盖住洞口，
// 球看着像掉在一块灰布上；再窄就只剩一条线，等于没铺。
function drawJawFaces(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  mouth: PocketMouth,
  center: { x: number; y: number },
  radius: number,
): void {
  const width = radius * 0.42
  const pairs = [
    [mouth.noseA, mouth.jawA],
    [mouth.noseB, mouth.jawB],
  ]

  ctx.save()
  mouthPath(ctx, t, mouth)
  ctx.clip()

  for (const pair of pairs) {
    const from = worldToScreen(t, pair[0].x, pair[0].y)
    const to = worldToScreen(t, pair[1].x, pair[1].y)

    // 颊板方向归一化，长度不够就跳过：极端缩放下两端可能落成同一个点，
    // 归一化会得到 NaN，整条路径直接消失，连报错都不会有。
    let dx = to.x - from.x
    let dy = to.y - from.y
    const len = Math.hypot(dx, dy)
    if (len < 0.5) {
      continue
    }
    dx = dx / len
    dy = dy / len

    // 法向取「指向袋心」的那一侧：斜面要铺在洞口里面。
    // 取反就成了在库边外侧糊一条灰边，袋口反而更亮。
    let nx = -dy
    let ny = dx
    const midX = (from.x + to.x) / 2
    const midY = (from.y + to.y) / 2
    if ((center.x - midX) * nx + (center.y - midY) * ny < 0) {
      nx = -nx
      ny = -ny
    }

    ctx.beginPath()
    ctx.moveTo(from.x, from.y)
    ctx.lineTo(to.x, to.y)
    ctx.lineTo(to.x + nx * width, to.y + ny * width)
    ctx.lineTo(from.x + nx * width, from.y + ny * width)
    ctx.closePath()

    const face = ctx.createLinearGradient(midX, midY, midX + nx * width, midY + ny * width)
    face.addColorStop(0, 'rgba(74, 83, 90, 0.96)')
    face.addColorStop(0.4, 'rgba(39, 46, 52, 0.96)')
    face.addColorStop(1, 'rgba(9, 11, 15, 0.96)')
    ctx.fillStyle = face
    ctx.fill()
  }

  ctx.restore()
}

// drawJawBevels 给两条 45° 颊板打高光。
//
// 斜边是一面立起来的小墙，顶棱受光：靠鼻尖那一头亮，退到库边那头顶端被
// 库边挡住，逐渐变暗。只画线不画面就够了——玩家要的是「这里有斜边」这个信号，
// 画成一块灰面反而会和护角的金属混在一起。
function drawJawBevels(
  ctx: CanvasRenderingContext2D,
  t: ViewTransform,
  mouth: PocketMouth,
  p: Pocket,
  radius: number,
): void {
  const out = outwardScreen(p)
  const pairs = [
    [mouth.noseA, mouth.jawA],
    [mouth.noseB, mouth.jawB],
  ]
  ctx.save()
  mouthPath(ctx, t, mouth)
  ctx.clip()
  ctx.lineCap = 'butt'
  ctx.lineWidth = Math.max(1.2, radius * 0.075)

  for (const pair of pairs) {
    const from = worldToScreen(t, pair[0].x, pair[0].y)
    const to = worldToScreen(t, pair[1].x, pair[1].y)
    const bevel = ctx.createLinearGradient(from.x, from.y, to.x, to.y)
    bevel.addColorStop(0, 'rgba(226, 232, 238, 0.62)')
    bevel.addColorStop(0.55, 'rgba(168, 176, 186, 0.42)')
    bevel.addColorStop(1, 'rgba(96, 103, 112, 0.18)')
    ctx.strokeStyle = bevel
    ctx.beginPath()
    ctx.moveTo(from.x + out.x * 0.5, from.y + out.y * 0.5)
    ctx.lineTo(to.x + out.x * 0.5, to.y + out.y * 0.5)
    ctx.stroke()
  }

  ctx.restore()
}

// drawCollar 画护角：一块压在洞口与库边交界上的圆角金属板。
//
// 形状是一块挖了圆洞的圆角方板，裁剪在「库边带」里（台呢以内一律不画）：
// 角袋的板是方的，被台呢的直角切掉内角之后正好剩一个 L 形包角；
// 中袋的板沿库边拉长一点，看起来像压在洞口两侧的条板。
//
// 明暗从左上到右下走三档，外加一道内棱高光和一层柔光——
// 这四项加起来才有「厚实、有体积」的金属感，少一项就退化成一块灰色方块。
function drawCollar(
  ctx: CanvasRenderingContext2D,
  p: Pocket,
  center: { x: number; y: number },
  radius: number,
  rail: Rect,
  cloth: Rect,
): void {
  const out = outwardScreen(p)
  const alongX = Math.abs(out.x) > Math.abs(out.y)
  const corner = p.kind === 'corner'
  // 板的外沿离袋心只有 1.22r（角袋）：护角是套在洞沿上的一圈金属，不是铺满
  // 库边的方块。取到 1.4r 以上时，缩小看六个袋口会变成六块灰斑，整张台子的
  // 轮廓上像长了六个亮疙瘩；1.22r 大致是「洞径 100mm、压边 20mm」的真实比例。
  // 中袋顺着库边拉长，像压在洞口两侧的条板。
  const halfOut = radius * (corner ? 1.22 : 1.26)
  const halfSide = radius * (corner ? 1.22 : 1.7)
  const halfX = alongX ? halfOut : halfSide
  const halfY = alongX ? halfSide : halfOut
  const round = Math.max(2.5, Math.min(halfX, halfY) * 0.32)

  ctx.save()
  // 先把台呢整块挖掉：护角只压在库边与木框上。
  // 角袋的板是方的，被台呢的直角切掉内角之后正好剩一个 L 形包角。
  ctx.beginPath()
  ctx.rect(rail.x, rail.y, rail.width, rail.height)
  ctx.rect(cloth.x, cloth.y, cloth.width, cloth.height)
  ctx.clip('evenodd')

  // 板 + 洞，一条路径用 evenodd 填，得到的就是「挖了洞的板」。
  const plate = () => {
    roundRectPath(ctx, center.x - halfX, center.y - halfY, halfX * 2, halfY * 2, round)
    ctx.moveTo(center.x + radius, center.y)
    ctx.arc(center.x, center.y, radius, 0, Math.PI * 2, true)
  }
  plate()
  const metal = ctx.createLinearGradient(
    center.x - halfX,
    center.y - halfY,
    center.x + halfX,
    center.y + halfY,
  )
  metal.addColorStop(0, PLATE_LIGHT)
  metal.addColorStop(0.45, PLATE_MID)
  metal.addColorStop(1, PLATE_DARK)
  ctx.fillStyle = metal
  ctx.fill('evenodd')

  // 柔光：左上角一块很淡的亮斑。alpha 只给 0.12，再高就变成「发光」，
  // 上一版 0.18 配上更亮的底色，整块板白得像塑料。
  const sheen = ctx.createRadialGradient(
    center.x - halfX * 0.4,
    center.y - halfY * 0.5,
    0,
    center.x - halfX * 0.4,
    center.y - halfY * 0.5,
    Math.max(halfX, halfY),
  )
  sheen.addColorStop(0, 'rgba(255, 255, 255, 0.12)')
  sheen.addColorStop(1, 'rgba(255, 255, 255, 0)')
  plate()
  ctx.fillStyle = sheen
  ctx.fill('evenodd')

  // 外棱：一条深色接缝，让板从库边上「分」出来，不然像刷上去的一层漆。
  ctx.strokeStyle = 'rgba(12, 15, 20, 0.6)'
  ctx.lineWidth = 1
  roundRectPath(ctx, center.x - halfX, center.y - halfY, halfX * 2, halfY * 2, round)
  ctx.stroke()

  // 内棱高光：沿外沿往里收 1.5px 再描一圈淡白，就是倒角面受光的那条棱。
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.26)'
  ctx.lineWidth = Math.max(1, radius * 0.055)
  roundRectPath(
    ctx,
    center.x - halfX + 1.5,
    center.y - halfY + 1.5,
    halfX * 2 - 3,
    halfY * 2 - 3,
    Math.max(2, round - 1.5),
  )
  ctx.stroke()

  // 洞沿的接触阴影：金属压在洞口的边上，有一圈很短的暗影，
  // 没有它，板与洞之间会像贴纸一样没有缝隙。
  ctx.beginPath()
  ctx.arc(center.x, center.y, radius * 1.01, 0, Math.PI * 2)
  ctx.strokeStyle = 'rgba(0, 0, 0, 0.5)'
  ctx.lineWidth = Math.max(1.5, radius * 0.16)
  ctx.stroke()

  ctx.restore()
}

// drawHole 画圆洞：圆心与半径就是落袋判定用的那个圆，不做任何二次换算。
//
// 渐变中心偏到朝台面那一侧，远端就落在朝台外那半圈上，正好是皮革内壁
// 露出来的位置——一个纯黑的圆看着像贴纸，有一点内壁的颜色才有深度。
//
// 中袋要额外裁掉台呢以内的那半个圆。中袋的袋心在库边厚度中线上，半径比这个
// 偏移大一倍多，整个圆会往台面里探进二十几毫米——不裁的话，绿色台呢上会盖着
// 一个黑圆盖，看起来像台布中间打了个洞。真实球桌的洞沿就落在库边脸上，洞身
// 全在库边背后，所以这里按台呢框切一刀，洞沿与库边正好对齐。
// 角袋不裁：角袋的洞探进台面的那部分落在 drawMouth 已经啃掉的三角里，
// 再切一刀反而会在洞里露出一小块绿的。
function drawHole(
  ctx: CanvasRenderingContext2D,
  p: Pocket,
  center: { x: number; y: number },
  radius: number,
  cloth: Rect | null,
): void {
  const out = outwardScreen(p)
  const hole = ctx.createRadialGradient(
    center.x - out.x * radius * 0.34,
    center.y - out.y * radius * 0.34,
    radius * 0.06,
    center.x,
    center.y,
    radius,
  )
  hole.addColorStop(0, POCKET_DEEP)
  hole.addColorStop(0.52, '#0a0c10')
  hole.addColorStop(0.86, '#1b1210')
  hole.addColorStop(1, '#33201a')
  ctx.save()
  if (cloth) {
    const box = radius * 2.4
    ctx.beginPath()
    ctx.rect(center.x - box, center.y - box, box * 2, box * 2)
    ctx.rect(cloth.x, cloth.y, cloth.width, cloth.height)
    ctx.clip('evenodd')
  }
  ctx.beginPath()
  ctx.arc(center.x, center.y, radius, 0, Math.PI * 2)
  ctx.fillStyle = hole
  ctx.fill()
  ctx.restore()
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

  for (const ball of onTable) {
    const rank = rankOf.get(ball.id)
    drawBall(ctx, t, ball, radiusPx, textureSide, rank === undefined ? 0 : rank)
  }
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

// 球面位图的边长上限（物理像素）。屏幕上球直径约三十 CSS 像素，
// 两张图就够清楚；再往上翻倍只会让每帧的采样量涨四倍。
const SPRITE_MAX_SIDE = 48

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
  // 会带上 42% 的黑，贴到台上就是一个方块——这正是「进球时变成方框」的由来。
  ctx.beginPath()
  ctx.arc(c, c, c, 0, Math.PI * 2)
  ctx.clip()

  const shade = ctx.createRadialGradient(
    c - c * 0.4,
    c - c * 0.44,
    c * 0.1,
    c,
    c,
    c * 1.02,
  )
  shade.addColorStop(0, 'rgba(255, 255, 255, 0.5)')
  shade.addColorStop(0.42, 'rgba(255, 255, 255, 0.04)')
  shade.addColorStop(0.78, 'rgba(0, 0, 0, 0.1)')
  shade.addColorStop(1, 'rgba(0, 0, 0, 0.42)')
  ctx.fillStyle = shade
  ctx.fillRect(0, 0, side, side)

  // 一小块死白的高光点。只有它才让球看起来是抛光树脂。
  ctx.beginPath()
  ctx.ellipse(c - c * 0.36, c - c * 0.42, c * 0.24, c * 0.15, -0.5, 0, Math.PI * 2)
  ctx.fillStyle = 'rgba(255, 255, 255, 0.9)'
  ctx.fill()

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
// 不随球转的光影。
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
): void {
  const center = worldToScreen(t, ball.x, ball.y)
  const lift = metersToPx(t, ball.z)
  // 腾空时球体略放大：真实的透视下球离镜头更近。
  const drawRadius = radiusPx * (1 + ball.z * BALL_GROWTH)
  const cy = center.y - lift * 0.6

  // 朝向按这一帧的位移推进，再用它重采样球面。
  const sprite = ballSprite(ball, textureSide, rollMatrixOf(ball), rank)
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

  // 明暗层贴着球体尺寸取，圆外是透明的，贴上去不会盖住台呢。
  const shade = ballShade(Math.max(8, Math.round(textureSide * (drawRadius / radiusPx))))
  if (shade) {
    ctx.drawImage(shade, center.x - drawRadius, cy - drawRadius, drawRadius * 2, drawRadius * 2)
  }
}

// 落袋动画的时长与状态。
//
// 帧里球是一瞬间消失的，直接照抄会看到球「咔」地不见。这里让它按帧里最后
// 的位置往最近的袋口滚进去，同时缩小变淡，和真实落袋的观感接近。
const SINK_MS = 340

interface SinkState {
  fromX: number
  fromY: number
  toX: number
  toY: number
  at: number
}

const sinking = new Map<string, SinkState>()
// 上一帧还在台面上的球号。用它判断这一帧谁刚掉袋。
let visibleIds = new Set<string>()

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
    sinking.set(id, { fromX: ball.x, fromY: ball.y, toX: pocket.x, toY: pocket.y, at: now })
  }
  visibleIds = current

  // 播完的一次性清掉，别让这张表跟着一局局对局长下去。
  for (const [id, state] of sinking) {
    if (now - state.at > SINK_MS) {
      sinking.delete(id)
    }
  }
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
// 进度用平方曲线：一开始慢、越靠近袋口越快，球的「被吸进去」的感觉就出来了。
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
    const progress = Math.min(1, (now - state.at) / SINK_MS)
    const eased = progress * progress
    const x = state.fromX + (state.toX - state.fromX) * eased
    const y = state.fromY + (state.toY - state.fromY) * eased
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
