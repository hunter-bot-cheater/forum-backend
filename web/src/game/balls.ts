// 球的外观与号码贴图。
//
// 号码不用图片：16 张 PNG 加起来几十 KB，还要处理不同 DPR 下的模糊。
// 用离屏 canvas 现画一次再缓存，整页零外部资源，首屏不受图片加载影响。
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

// paintBall 把一颗球画进正方形画布。分四层：底色、花色带、球面明暗、号码。
function paintBall(ctx: CanvasRenderingContext2D, side: number, look: BallLook): void {
  const c = side / 2
  const r = side / 2 - Math.max(1, side * 0.01)

  // 先按圆形裁剪，后面所有填充都不会溢出到球外。
  ctx.save()
  ctx.beginPath()
  ctx.arc(c, c, r, 0, Math.PI * 2)
  ctx.clip()

  if (look.striped) {
    // 花色球：白底 + 中间色带。色带高度取 0.62 倍直径，
    // 与真实球上那道色带的比例接近，留出足够的白色给号码。
    ctx.fillStyle = '#fbfaf6'
    ctx.fillRect(0, 0, side, side)
    ctx.fillStyle = look.color
    ctx.fillRect(0, c - r * 0.62, side, r * 1.24)
  } else {
    ctx.fillStyle = look.color
    ctx.fillRect(0, 0, side, side)
  }

  // 球面明暗：左上一团高光、右下一圈变暗，让平面圆看起来是球。
  // 用径向渐变而不是描边，渐变能同时表达高光和球体边缘的收暗。
  const shade = ctx.createRadialGradient(c - r * 0.35, c - r * 0.4, r * 0.08, c, c, r)
  shade.addColorStop(0, 'rgba(255, 255, 255, 0.62)')
  shade.addColorStop(0.45, 'rgba(255, 255, 255, 0.06)')
  shade.addColorStop(1, 'rgba(0, 0, 0, 0.34)')
  ctx.fillStyle = shade
  ctx.fillRect(0, 0, side, side)

  // 一小块死白的高光点。只有它才让球看起来是抛光树脂；
  // 光靠上面那圈渐变，球会像一颗哑光塑料珠子。
  ctx.beginPath()
  ctx.ellipse(c - r * 0.38, c - r * 0.44, r * 0.2, r * 0.13, -0.5, 0, Math.PI * 2)
  ctx.fillStyle = 'rgba(255, 255, 255, 0.88)'
  ctx.fill()

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

  ctx.restore()
}
