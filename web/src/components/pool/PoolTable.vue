<template>
  <div
    ref="host"
    class="pt"
    @pointerdown="onPointerDown"
    @pointermove="onPointerMove"
    @pointerup="onPointerUp"
    @pointercancel="onPointerUp"
  >
    <canvas ref="canvas" class="pt__canvas"></canvas>
  </div>
</template>

<script setup lang="ts">
// 对局与练习共用的画布容器。只做三件事：跟住容器尺寸、按 DPR 配好位图、
// 内容变了就重画一帧。几何与绘制都在 @/game 里，这里不掺业务逻辑。
//
// 指针拖动也只负责「换算成相对起点的位移」再抛给父级，
// 具体是旋转瞄准还是拖动母球由页面决定：同一个画布以后还要支持手中球落位。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { screenToWorld, fitTable, type Point, type Viewport } from '@/game/geometry'
import type { AimLayer } from '@/game/renderer'
import { drawActors, drawTable, sinkingActive, TABLE_PADDING } from '@/game/renderer'
import type { BallState, PoolDrag } from '@/game/types'

const props = defineProps<{
  balls: BallState[]
  aim?: AimLayer | null
  // peerAim 是对面那一方此刻的瞄准层，对局页与观战页都靠它看到「对面在转杆」。
  peerAim?: AimLayer | null
  // ghost 是手中球还没落位时跟着指针走的虚影位置（世界坐标）。
  ghost?: { x: number; y: number } | null
  // headString 为 true 时画开球线。开球摆母球时由对局页打开：它标出母球
  // 能摆到哪，也解释了为什么往球堆那边摆会被夹住。
  headString?: boolean
}>()

const emit = defineEmits<{
  drag: [payload: PoolDrag]
  // tap 是「按下又抬起、几乎没移动」的一次点按，坐标已换成台面世界坐标（米）。
  // 手中球落位用它，瞄准走 drag，两者用同一个 scale，落点不会偏。
  tap: [point: Point]
  // hover 是鼠标在台面上移动（没按键）时的位置。桌面端「指哪打哪」靠它：
  // 手指没有悬停，所以只在鼠标上发，触摸屏仍然用拖动转角度。
  hover: [point: Point]
}>()

const host = ref<HTMLDivElement | null>(null)
const canvas = ref<HTMLCanvasElement | null>(null)

// ResizeObserver 只在浏览器里有，组件卸载时必须断开，否则容器被移除后回调仍会触发。
let observer: ResizeObserver | null = null
// DPR 查询。把窗口从笔记本屏拖到外接屏时容器尺寸不变，ResizeObserver 不触发，
// 但位图会停在旧倍率上变糊。matchMedia 能捕获这种变化。
let dprQuery: MediaQueryList | null = null
// 合帧句柄。拖动时球位与瞄准线一帧内可能改多次，直接重画会把工作重复做几遍。
let frame = 0

// 按下时的坐标与按下状态。pointermove 在鼠标没按下时也会触发，
// 不判这个标志会把「鼠标划过」当成拖动，瞄准线会跟着光标乱转。
let pressing = false
let originX = 0
let originY = 0

// 位图倍率的两个约束：设备像素比是下限、像素预算是上限。
//
// 下限：一个 CSS 像素至少要落到整数倍的物理像素上，否则整张台子都会被
// 插值糊掉——1x 屏上球缘的锯齿、3x 屏上细线的灰边都是这么来的。
//
// 上限：位图按「CSS 面积 × 倍率²」膨胀，超了就是每帧多拷几兆像素。
// 4.8M 像素差不多是这颗星球上主流手机还能稳 60fps 的一帧上限，
// 台子在全屏竖屏时最接近它。
const PIXEL_BUDGET = 4800000

// 超采样系数：在设备像素比之上再画大一点，让浏览器缩回来。
//
// 为什么要超：球缘、袋口圆弧、瞄准线与球杆都是矢量绘制的，画布自己的抗锯齿
// 只有一像素，缩回来之后就多了一像素的过渡余地，边缘看起来才是「圆滑」而不是
// 「台阶」。低倍率屏幕上收益最大——1x 显示器不超采样时，球缘的锯齿一眼可见。
//
// 高倍率屏幕上只加两成：3 倍设备像素已经用掉了大半预算，再加就要么超预算、
// 要么把每帧的静态层拷贝拖慢。
function renderScale(cssWidth: number, cssHeight: number): number {
  const device = Math.min(Math.max(window.devicePixelRatio || 1, 1), 4)
  let extra = 1.75
  if (device >= 2.5) {
    extra = 1.2
  } else if (device >= 1.5) {
    extra = 1.4
  }
  const wanted = device * extra
  const area = Math.max(1, cssWidth * cssHeight)
  const afford = Math.sqrt(PIXEL_BUDGET / area)
  const scale = Math.min(wanted, Math.max(device, afford))
  // 取到 1/8 档：容器拖大拖小时倍率会连续变化，不取整的话静态层每帧都要重建。
  return Math.round(scale * 8) / 8
}

// syncSize 把位图尺寸对齐到「CSS 尺寸 × 倍率」，返回 CSS 尺寸与所用倍率。
//
// canvas 有两套尺寸：style 上的 CSS 尺寸与 width/height 属性上的位图尺寸。
// 只设 CSS 会让位图按默认 300×150 拉伸，画面糊成一片；
// 只设属性又不会跟着容器走。两边都要同步。
//
// 容器在 display:none 或路由切换的瞬间尺寸是 0，这时返回 null 跳过这一帧，
// 而不是把位图设成 0：0 宽的画布之后再往上画会被静默丢弃，且不报错。
function syncSize(
  el: HTMLCanvasElement,
): { width: number; height: number; scale: number } | null {
  const cssWidth = el.clientWidth
  const cssHeight = el.clientHeight
  if (cssWidth <= 0 || cssHeight <= 0) {
    return null
  }

  const scale = renderScale(cssWidth, cssHeight)
  const bufWidth = Math.round(cssWidth * scale)
  const bufHeight = Math.round(cssHeight * scale)

  if (el.width !== bufWidth || el.height !== bufHeight) {
    el.width = bufWidth
    el.height = bufHeight
  }
  return { width: cssWidth, height: cssHeight, scale }
}

// staticLayer 是台面的静态层：木框、库边、台呢、六个袋口。
//
// 这一层一帧里一个像素都不会变，但它最贵（台呢两层全屏渐变、袋口六个各自
// 四层填充加裁剪）。拖动瞄准时每帧重画整张台面，手感就是「拖杆发涩」。
// 现在只在尺寸或像素比变化时重画一次，平时每帧只贴一张图。
let staticLayer: HTMLCanvasElement | null = null
// staticKey 记着这张图是按哪套尺寸画的，跟当前不一致就重建。
let staticKey = ''

// ensureStaticLayer 取（必要时重建）静态层，返回的位图与画布同尺寸。
function ensureStaticLayer(
  width: number,
  height: number,
  dpr: number,
  view: Viewport,
): HTMLCanvasElement {
  const key = String(width) + 'x' + String(height) + '@' + String(dpr)
  if (staticLayer && staticKey === key) {
    return staticLayer
  }
  const layer = document.createElement('canvas')
  layer.width = width
  layer.height = height
  const layerCtx = layer.getContext('2d')
  if (layerCtx) {
    // 静态层也按物理像素画，贴回主画布时才不用再缩放一次。
    layerCtx.setTransform(dpr, 0, 0, dpr, 0, 0)
    drawTable(layerCtx, view)
  }
  staticLayer = layer
  staticKey = key
  return layer
}

// paint 画一帧。ctx 先按倍率缩放，之后绘制代码全程按 CSS 像素算，
// 不用再关心屏幕是几倍图。
function paint(): void {
  const el = canvas.value
  if (!el) {
    return
  }
  const size = syncSize(el)
  if (!size) {
    return
  }
  const ctx = el.getContext('2d')
  if (!ctx) {
    return
  }
  const dpr = size.scale
  const layer = ensureStaticLayer(el.width, el.height, dpr, size)

  // 先复位变换再贴静态层：位图已经是物理像素，带着 dpr 缩放贴会被再放一倍。
  ctx.setTransform(1, 0, 0, 1, 0, 0)
  ctx.clearRect(0, 0, el.width, el.height)
  ctx.imageSmoothingEnabled = true
  // 静态层与画布同尺寸，这是一次 1:1 的拷贝；但超采样时两者都按物理像素画，
  // 这里的质量设置只影响最后那次合成，设高一点不吃亏。
  ctx.imageSmoothingQuality = 'high'
  ctx.drawImage(layer, 0, 0)

  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  drawActors(
    ctx,
    size,
    {
      balls: props.balls,
      aim: props.aim,
      peerAim: props.peerAim,
      ghost: props.ghost,
      headString: props.headString,
    },
    dpr,
  )

  // 落袋动画是唯一「自己会走」的那一段：它靠时间推进，不靠外部输入。
  // 这里不给下一帧，画面就停在「球刚被判落袋」的那一帧上，
  // 表现为母球一直挂在袋口不消失。动画本身只有 340 毫秒，播完即停。
  if (sinkingActive()) {
    schedule()
  }
}

// schedule 把多次重画请求压到下一帧，避免同一帧里画好几遍。
function schedule(): void {
  if (frame) {
    return
  }
  frame = requestAnimationFrame(() => {
    frame = 0
    paint()
  })
}

// 球位是数组里的对象，浅比较会在原地修改坐标时漏掉变化，所以用 deep。
// 16 颗球的 deep 遍历成本远低于一次重绘，不值得为它写更细的脏检查。
watch(() => [props.balls, props.aim, props.peerAim, props.ghost, props.headString], schedule, {
  deep: true,
})

// onPointerDown 记录起点并捕获指针。
// 捕获之后手指移出画布甚至移出窗口，move 与 up 仍然会回到这个元素，
// 否则拖到屏幕边缘松开就会一直卡在「按下」状态。
function onPointerDown(e: PointerEvent) {
  if (e.pointerType === 'mouse' && e.button > 0) {
    return
  }
  pressing = true
  originX = e.clientX
  originY = e.clientY
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  emit('drag', { phase: 'start', dx: 0, dy: 0, point: worldOf(e) })
}

// worldOf 把一次指针事件换算成台面世界坐标（米）。
//
// 变换必须和绘制时一模一样，否则会出现「看着点在中袋、球落在中袋另一侧」。
// 两者都从 fitTable + TABLE_PADDING 取，改一处就一起改。
function worldOf(e: PointerEvent): Point | null {
  const el = canvas.value
  if (!el) {
    return null
  }
  const rect = el.getBoundingClientRect()
  const t = fitTable({ width: rect.width, height: rect.height }, TABLE_PADDING)
  return screenToWorld(t, e.clientX - rect.left, e.clientY - rect.top)
}

// onPointerMove 只在按下之后上报拖动，另外把鼠标悬停的位置也报出去。
function onPointerMove(e: PointerEvent) {
  if (!pressing) {
    if (e.pointerType === 'mouse') {
      const point = worldOf(e)
      if (point) {
        emit('hover', point)
      }
    }
    return
  }
  emit('drag', { phase: 'move', dx: e.clientX - originX, dy: e.clientY - originY, point: worldOf(e) })
}

// onPointerUp 结束拖动。用 pressing 兜底：某些浏览器在取消捕获后仍会补发一次 up。
function onPointerUp(e: PointerEvent) {
  if (!pressing) {
    return
  }
  pressing = false
  const target = e.currentTarget as HTMLElement
  if (target.hasPointerCapture(e.pointerId)) {
    target.releasePointerCapture(e.pointerId)
  }
  emit('drag', { phase: 'end', dx: e.clientX - originX, dy: e.clientY - originY, point: worldOf(e) })

  // 位移小于 4px 才算点按。手指按下时几乎一定会抖几像素，
  // 阈值给 0 会让「想点一下」变成「拖了一下」，落位永远不生效。
  const moved = Math.hypot(e.clientX - originX, e.clientY - originY)
  if (moved > 4) {
    return
  }
  const point = worldOf(e)
  if (point) {
    emit('tap', point)
  }
}

// onDprChange 在像素比变化时重画，并重新注册一次查询。
//
// 注册一次 { once: true } 而不是常驻监听：dppx 是查询条件的一部分，
// 倍率变了旧查询就永远不再匹配，必须用新的倍率重新建一个。
function onDprChange(): void {
  schedule()
  watchDpr()
}

// watchDpr 按当前像素比建一个 matchMedia 查询。老浏览器没有 matchMedia 时跳过，
// 代价只是「拖到外接屏后画面略糊」，刷新即恢复。
function watchDpr(): void {
  if (typeof window.matchMedia !== 'function') {
    return
  }
  dprQuery = window.matchMedia('(resolution: ' + String(window.devicePixelRatio) + 'dppx)')
  dprQuery.addEventListener('change', onDprChange, { once: true })
}

onMounted(() => {
  paint()
  if (host.value) {
    observer = new ResizeObserver(schedule)
    observer.observe(host.value)
  }
  watchDpr()
})

onBeforeUnmount(() => {
  if (observer) {
    observer.disconnect()
    observer = null
  }
  if (dprQuery) {
    dprQuery.removeEventListener('change', onDprChange)
    dprQuery = null
  }
  if (frame) {
    cancelAnimationFrame(frame)
    frame = 0
  }
  // 静态层是整张画布的位图，宽屏上能到几 MB，组件卸了就放掉。
  staticLayer = null
  staticKey = ''
})
</script>

<style scoped>
/* 容器本身不定高度，由父级决定：对局页里它占满剩余空间，
   练习页可能只占上半屏。定死高度会让两种用法打架。 */
.pt {
  position: relative;
  width: 100%;
  height: 100%;
  overflow: hidden;
}

/* 绝对定位而不是普通块级元素：canvas 默认是 inline，
   会在底部留出几个像素的行高空白，导致每次 resize 都慢慢往上飘。 */
.pt__canvas {
  position: absolute;
  inset: 0;
  display: block;
  width: 100%;
  height: 100%;
  touch-action: none;
}
</style>
