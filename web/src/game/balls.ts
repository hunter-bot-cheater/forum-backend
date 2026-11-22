// 球的外观与号码贴图。
//
// 号码不用图片：16 张 PNG 加起来几十 KB，还要处理不同 DPR 下的模糊。
// 用离屏 canvas 现画一次再缓存，整页零外部资源，首屏不受图片加载影响。
//
// 这里画的是**反照率**（底色、花色带、号码），不含明暗与高光。
// 明暗必须在绘制时按屏幕方向补，理由见 renderer.ts 的 drawBall：
// 球滚动时贴图要跟着转，而光源方向是固定的，烘焙进贴图的光斑会跟着转，
// 看起来像台灯绕着球跑。
//
// 缓存键带像素尺寸：窗口放大或换到高分屏时球的直径会变，
// 沿用旧尺寸的贴图会被拉伸成糊的号码。尺寸变了就重建一份，旧的自然被丢弃。

// 1 到 7 号球的本色，8 号单独给黑，9 到 15 号沿用 1 到 7 的颜色但画成花色。
// 色值取自常见的台球色卡，饱和度压过一档以免和白色号码的对比不足。
const SOLID_COLORS: Record<number, string> = {
  1: '#f0c02f',
  2: '#2f66d0',
  3: '#d13b2c',
  4: '#7a4fbf',
  5: '#e07b2a',
  6: '#2f8b52',
  7: '#8a3b2e',
}

const EIGHT_COLOR = '#22262e'
const CUE_COLOR = '#f5f3ee'

// BallLook 是一颗球的静态外观，绘制时需要的信息全在这里。
export interface BallLook {
  color: string
  // striped 为 true 时球面是「白底 + 中间一道色带」，9 到 15 号都是这样。
  striped: boolean
  // number 为 0 表示不画号码（母球）。
  number: number
}

// ballLookOf 把球号换成外观。非法球号按母球处理：
// 宁可画一颗白球，也不要在渲染循环里抛异常把整帧画面搞黑。
export function ballLookOf(ballId: string): BallLook {
  if (ballId === 'cue') {
    return { color: CUE_COLOR, striped: false, number: 0 }
  }
  const n = Number(ballId)
  if (!Number.isInteger(n) || n < 1 || n > 15) {
    return { color: CUE_COLOR, striped: false, number: 0 }
  }
  if (n === 8) {
    return { color: EIGHT_COLOR, striped: false, number: 8 }
  }
  const key = n > 8 ? n - 8 : n
  return { color: SOLID_COLORS[key], striped: n > 8, number: n }
}

// 纹理缓存。只缓存「当前尺寸」这一套，尺寸一变就整批丢掉重建。
//
// 不按尺寸分桶：同一时刻画面里只有一种球直径，分桶只会让每次拖动窗口都
// 多留一份 16 张位图，一局下来几十张，且再也没有机会被访问到。
let cachedSide = 0
let textureCache = new Map<string, HTMLCanvasElement>()

// ballTexture 取（必要时生成）一颗球的位图，边长以物理像素为单位。
//
// 传入的 size 应当已经乘过 devicePixelRatio：贴图按物理像素生成，
// 绘制时再缩到 CSS 尺寸，这样在 2x 屏上号码也是清晰的。
export function ballTexture(ballId: string, size: number): HTMLCanvasElement {
  // 最小 8px：再小球台上的球只剩一个点，号码画上去也是糊的。
  const side = Math.max(8, Math.round(size))
  if (side !== cachedSide) {
    cachedSide = side
    textureCache = new Map()
  }

  const cached = textureCache.get(ballId)
  if (cached) {
    return cached
  }

  const canvas = document.createElement('canvas')
  canvas.width = side
  canvas.height = side
  const ctx = canvas.getContext('2d')
  if (ctx) {
    paintBall(ctx, side, ballLookOf(ballId))
  }

  textureCache.set(ballId, canvas)
  return canvas
}

// paintBall 把一颗球的反照率画进正方形画布：底色、花色带、号码。
//
// 视角是**从极点看**，不是常见的侧视图：画布中心就是球的一个极点，
// 到中心的距离等于 sinθ（θ 是离极点的球心角），所以赤道落在画布边缘。
// 这个约定是渲染侧做球面反投影的前提——那边拿屏幕圆上的 (nx, ny) 直接取
// 贴图的同名位置，只有极投影能保证「球转过多少、图案就跟着挪多少」。
//
// 所以色带不能画成一条横向长条（那是侧视图的画法）：要画成一圈环带，
// 内沿是色带的纬度边界，外沿一直铺到画布之外，连正方形的四个角也填上色。
// 四角必须填：球面赤道附近会采到那里，留白就在球上显出一圈黑边
// （透明像素的 RGB 是 0，双线性一加权整圈都暗下去）。
//
// 明暗与高光不在这里画（见文件头注释），调用方在球旋转之后补上去。
function paintBall(ctx: CanvasRenderingContext2D, side: number, look: BallLook): void {
  const c = side / 2
  const r = side / 2 - Math.max(1, side * 0.01)

  // 底色一律先铺白：条纹球的两个极帽是白的，实心球随后整张盖掉。
  ctx.fillStyle = '#fbfaf6'
  ctx.fillRect(0, 0, side, side)

  if (look.striped) {
    // 赤道色带：从离极点 38°（sin = 0.62）到赤道的那一圈。
    // 外圆取 1.6r 是为了盖住四角（角到中心约 1.41r），否则四角还是白的。
    ctx.beginPath()
    ctx.arc(c, c, r * 1.6, 0, Math.PI * 2)
    ctx.arc(c, c, r * 0.62, 0, Math.PI * 2, true)
    ctx.fillStyle = look.color
    ctx.fill()
  } else {
    ctx.fillStyle = look.color
    ctx.fillRect(0, 0, side, side)
  }

  // 号码：白圆垫底再写黑字。母球（number 为 0）跳过这一层。
  if (look.number > 0) {
    const badge = r * 0.46
    ctx.beginPath()
    ctx.arc(c, c, badge, 0, Math.PI * 2)
    ctx.fillStyle = '#fbfaf6'
    ctx.fill()

    ctx.fillStyle = '#1d2129'
    ctx.font = 'bold ' + String(Math.round(badge * 1.45)) + 'px "Helvetica Neue", Arial, sans-serif'
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText(String(look.number), c, c + badge * 0.04)
  }
}
