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
import { screenToWorld, fitTable, type Point } from '@/game/geometry'
import type { AimLayer } from '@/game/renderer'
import { drawScene, TABLE_PADDING } from '@/game/renderer'
import type { BallState, PoolDrag } from '@/game/types'

const props = defineProps<{
  balls: BallState[]
  aim?: AimLayer | null
  // peerAim 是对面那一方此刻的瞄准层，对局页与观战页都靠它看到「对面在转杆」。
  peerAim?: AimLayer | null
  // ghost 是手中球还没落位时跟着指针走的虚影位置（世界坐标）。
  ghost?: { x: number; y: number } | null
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

// dprOf 读当前像素比并封顶到 3。
// 4K 屏上 devicePixelRatio 能到 4，位图面积翻倍但肉眼已经看不出差别，
// 反而让每帧的贴图缩放更慢。
function dprOf(): number {
  const raw = window.devicePixelRatio || 1
  return Math.min(Math.max(raw, 1), 3)
}

// syncSize 把位图尺寸对齐到「CSS 尺寸 × DPR」，返回当前的 CSS 尺寸。
//
// canvas 有两套尺寸：style 上的 CSS 尺寸与 width/height 属性上的位图尺寸。
// 只设 CSS 会让位图按默认 300×150 拉伸，画面糊成一片；
// 只设属性又不会跟着容器走。两边都要同步。
//
// 容器在 display:none 或路由切换的瞬间尺寸是 0，这时返回 null 跳过这一帧，
// 而不是把位图设成 0：0 宽的画布之后再往上画会被静默丢弃，且不报错。
function syncSize(el: HTMLCanvasElement): { width: number; height: number } | null {
  const cssWidth = el.clientWidth
  const cssHeight = el.clientHeight
  if (cssWidth <= 0 || cssHeight <= 0) {
    return null
  }

  const dpr = dprOf()
  const bufWidth = Math.round(cssWidth * dpr)
  const bufHeight = Math.round(cssHeight * dpr)

  if (el.width !== bufWidth || el.height !== bufHeight) {
    el.width = bufWidth
    el.height = bufHeight
  }
  return { width: cssWidth, height: cssHeight }
}

// paint 画一帧。ctx 先按 DPR 缩放，之后 drawScene 全程按 CSS 像素算，
// 绘制代码里不用再关心屏幕是几倍图。
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
  const dpr = dprOf()
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  drawScene(
    ctx,
    size,
    { balls: props.balls, aim: props.aim, peerAim: props.peerAim, ghost: props.ghost },
    dpr,
  )
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
watch(() => [props.balls, props.aim, props.peerAim, props.ghost], schedule, { deep: true })

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
