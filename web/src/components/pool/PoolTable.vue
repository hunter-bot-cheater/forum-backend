<template>
  <div ref="host" class="pt">
    <canvas ref="canvas" class="pt__canvas"></canvas>
  </div>
</template>

<script setup lang="ts">
// 对局与练习共用的画布容器。只做三件事：跟住容器尺寸、按 DPR 配好位图、
// 球位变了就重画一帧。几何与绘制都在 @/game 里，这里不掺业务逻辑。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { drawScene } from '@/game/renderer'
import type { BallState } from '@/game/types'

const props = defineProps<{
  balls: BallState[]
}>()

const host = ref<HTMLDivElement | null>(null)
const canvas = ref<HTMLCanvasElement | null>(null)

// ResizeObserver 只在浏览器里有，组件卸载时必须断开，否则容器被移除后回调仍会触发。
let observer: ResizeObserver | null = null
// 合帧句柄。球位一帧内可能变多次（动画播放时每帧都变），
// 直接重画会把一帧的工作重复做几遍。
let frame = 0

// dprOf 读当前像素比并封顶到 3。
// 4K 屏上 devicePixelRatio 能到 4，位图面积翻倍但肉眼已经看不出差别，
// 反而让每帧的贴图缩放更慢。
function dprOf(): number {
  const raw = window.devicePixelRatio || 1
  return Math.min(Math.max(raw, 1), 3)
}

// syncSize 把位图尺寸对齐到「CSS 尺寸 × DPR」。
//
// canvas 有两套尺寸：style 上的 CSS 尺寸与 width/height 属性上的位图尺寸。
// 只设 CSS 会让位图按默认 300×150 拉伸，画面糊成一片；
// 只设属性又不会跟着容器走。两边都要同步。
function syncSize(): boolean {
  const el = canvas.value
  if (!el) {
    return false
  }
  const dpr = dprOf()
  const cssWidth = Math.max(1, el.clientWidth)
  const cssHeight = Math.max(1, el.clientHeight)
  const bufWidth = Math.round(cssWidth * dpr)
  const bufHeight = Math.round(cssHeight * dpr)

  if (el.width !== bufWidth || el.height !== bufHeight) {
    el.width = bufWidth
    el.height = bufHeight
  }
  return true
}

// paint 画一帧。ctx 先按 DPR 缩放，之后 drawScene 全程按 CSS 像素算，
// 绘制代码里不用再关心屏幕是几倍图。
function paint(): void {
  const el = canvas.value
  if (!el || !syncSize()) {
    return
  }
  const ctx = el.getContext('2d')
  if (!ctx) {
    return
  }
  const dpr = dprOf()
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  drawScene(ctx, { width: el.clientWidth, height: el.clientHeight }, { balls: props.balls }, dpr)
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
watch(() => props.balls, schedule, { deep: true })

onMounted(() => {
  paint()
  if (host.value) {
    observer = new ResizeObserver(schedule)
    observer.observe(host.value)
  }
})

onBeforeUnmount(() => {
  if (observer) {
    observer.disconnect()
    observer = null
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
