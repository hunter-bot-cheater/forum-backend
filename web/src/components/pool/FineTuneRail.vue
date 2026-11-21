<template>
  <div class="ft" :class="{ 'is-off': disabled }">
    <button class="ft__step" type="button" :disabled="disabled" @click="nudge(-STEP_DEG)">‹</button>

    <div
      ref="rail"
      class="ft__rail"
      @pointerdown="onDown"
      @pointermove="onMove"
      @pointerup="onUp"
      @pointercancel="onUp"
    >
      <span class="ft__cue"></span>
      <span class="ft__angle">{{ angleText }}°</span>
    </div>

    <button class="ft__step" type="button" :disabled="disabled" @click="nudge(STEP_DEG)">›</button>
  </div>
</template>

<script setup lang="ts">
// 底部微调尺。三个入口：左右按钮各走 0.1°，在刻度尺上横向拖动则连续微调。
//
// 只上报「转多少度」，不自己算 phi：角度的唯一来源在页面里，
// 这里如果也用一份本地状态，拖快了两边就会打架。
import { computed, ref } from 'vue'

// 一格的度数。台球瞄准差 0.1° 在 1.5 米外就是 2.6 毫米，够用了。
const STEP_DEG = 0.1

// 刻度尺上拖动多少像素算一格。8px 与手指的精细控制范围接近，
// 再小就会「怎么拖都过不去」。
const PX_PER_STEP = 8

const props = defineProps<{
  // 当前方位角，弧度。
  phi: number
  disabled?: boolean
}>()

const emit = defineEmits<{
  nudge: [deltaDeg: number]
}>()

const rail = ref<HTMLDivElement | null>(null)

// 拖动过程中的累计格数与上一次上报的格数。
let pressing = false
let originX = 0
let lastStep = 0

// angleText 把弧度换成 0 到 360 的度数，保留一位小数。
// 不做归一化的话会出现「-37.4°」这种负数，读起来要心算。
const angleText = computed(() => {
  const deg = (props.phi * 180) / Math.PI
  const wrapped = ((deg % 360) + 360) % 360
  return wrapped.toFixed(1)
})

// nudge 上报一次固定步长的微调，按钮直接调它。
function nudge(deltaDeg: number) {
  if (props.disabled) {
    return
  }
  emit('nudge', deltaDeg)
}

// onDown 记下起点，按下的那一刻不动角度：误触很常见，抬手就走的按钮不该生效。
function onDown(e: PointerEvent) {
  if (props.disabled || (e.pointerType === 'mouse' && e.button !== 0)) {
    return
  }
  pressing = true
  originX = e.clientX
  lastStep = 0
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
}

// onMove 按整格上报差额。只报差额而不是总位移：
// 总位移每次都会把已经转过的角度再算一遍，父级一加就翻倍。
function onMove(e: PointerEvent) {
  if (!pressing || props.disabled) {
    return
  }
  const steps = Math.trunc((e.clientX - originX) / PX_PER_STEP)
  const delta = steps - lastStep
  if (delta === 0) {
    return
  }
  lastStep = steps
  emit('nudge', delta * STEP_DEG)
}

// onUp 结束拖动，释放指针捕获。
function onUp(e: PointerEvent) {
  if (!pressing) {
    return
  }
  pressing = false
  const target = e.currentTarget as HTMLElement
  if (target.hasPointerCapture(e.pointerId)) {
    target.releasePointerCapture(e.pointerId)
  }
}
</script>

<style scoped>
/* 与力度条同样用深色场景的配色，不套浅色主题 token。 */
.ft {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 36px;
  padding: 0 2px;
}

.ft__step {
  flex: none;
  width: 30px;
  height: 30px;
  padding: 0;
  font-size: 18px;
  line-height: 1;
  color: rgba(255, 255, 255, 0.82);
  background: rgba(255, 255, 255, 0.1);
  border: none;
  border-radius: 8px;
  cursor: pointer;
}

.ft__step:active {
  background: rgba(255, 255, 255, 0.2);
}

.ft__step:disabled {
  opacity: 0.35;
  cursor: default;
}

/* 刻度尺：用重复渐变画短线，比塞几十个 span 轻得多，
   而且宽度随容器自适应，不用监听尺寸。 */
.ft__rail {
  position: relative;
  flex: 1;
  height: 30px;
  overflow: hidden;
  background-image: repeating-linear-gradient(
    to right,
    rgba(255, 255, 255, 0.32) 0,
    rgba(255, 255, 255, 0.32) 1px,
    transparent 1px,
    transparent 8px
  );
  background-position: center;
  background-size: 100% 10px;
  background-repeat: no-repeat;
  border-radius: 8px;
  cursor: ew-resize;
  touch-action: none;
}

/* 尺子中间的球杆图形。只是一段渐变的细条，用来表达「这里在调杆」，
   不按真实角度旋转——刻度尺本身是水平的，旋转反而看不懂。 */
.ft__cue {
  position: absolute;
  top: 50%;
  right: 18%;
  left: 18%;
  height: 3px;
  margin-top: -1.5px;
  background: linear-gradient(to right, #d8c9a8 0%, #d8c9a8 12%, #8a6a44 100%);
  border-radius: 2px;
}

/* 角度数字压在尺子右端，等宽数字避免每位跳动时左右晃。 */
.ft__angle {
  position: absolute;
  top: 50%;
  right: 6px;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: rgba(255, 255, 255, 0.78);
  transform: translateY(-50%);
}

.ft.is-off {
  opacity: 0.4;
}
</style>
