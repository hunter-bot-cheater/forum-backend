<template>
  <div
    class="pw"
    :class="{ 'is-off': disabled }"
    role="slider"
    :aria-valuenow="Math.round(value * 100)"
    aria-valuemin="0"
    aria-valuemax="100"
    aria-label="力度"
    @pointerdown="onDown"
    @pointermove="onMove"
    @pointerup="onUp"
    @pointercancel="onUp"
  >
    <div ref="track" class="pw__track">
      <div class="pw__fill" :style="{ height: percent }"></div>
      <div class="pw__knob" :style="{ top: percent }"></div>
    </div>
    <span class="pw__value">{{ Math.round(value * 100) }}</span>
  </div>
</template>

<script setup lang="ts">
// 竖向力度条。手指或鼠标在轨道上任意位置按下即取值，松手出杆。
//
// 方向是「往下拉变大」：越往下球杆拉得越开，像真把杆往后拽。
// 反过来（往上推变大）很违和——往上推是「把球推出去」，可推得越猛却越轻。
//
// 拖动过程中不锁定手指与滑块的对应关系（不是「拖着球走」），
// 而是「手指在哪，力度就是哪」：竖屏手机上滑块只有手指肚大小，
// 要求精确按住它再拖，实际很难操作。
import { computed, ref } from 'vue'

const props = defineProps<{
  // 0 到 1 的力度。
  value: number
  disabled?: boolean
}>()

const emit = defineEmits<{
  'update:value': [value: number]
  // 松手时触发，表示「这一杆定了」。
  shoot: []
}>()

const track = ref<HTMLDivElement | null>(null)
let pressing = false

// percent 是给样式用的字符串，省得模板里拼两次。
const percent = computed(() => Math.round(props.value * 100) + '%')

// clamp01 收进 0 到 1。
function clamp01(n: number): number {
  if (n < 0) {
    return 0
  }
  if (n > 1) {
    return 1
  }
  return n
}

// valueAt 把指针位置换算成力度。从轨道顶部算起，顶部是 0，越往下越大。
//
// 用渲染后的实际矩形算，而不是用组件高度：轨道上下有内边距，
// 按组件高度算会让「拖到最底」只能到 95% 上下，永远出不了满力球。
function valueAt(clientY: number): number {
  const el = track.value
  if (!el) {
    return props.value
  }
  const rect = el.getBoundingClientRect()
  if (rect.height <= 0) {
    return props.value
  }
  return clamp01((clientY - rect.top) / rect.height)
}

// onDown 按下即取值并捕获指针，拖出轨道也能继续调。
function onDown(e: PointerEvent) {
  if (props.disabled || (e.pointerType === 'mouse' && e.button !== 0)) {
    return
  }
  pressing = true
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  emit('update:value', valueAt(e.clientY))
}

// onMove 只在按下后响应。
function onMove(e: PointerEvent) {
  if (!pressing || props.disabled) {
    return
  }
  emit('update:value', valueAt(e.clientY))
}

// onUp 松手出杆。
//
// 力度小于 2% 当成误触：手指只是碰了一下轨道就抬手，不应该把球打出去。
// 这个阈值也给「取消出杆」留了口子——把力度拖回顶部再松手即可。
function onUp(e: PointerEvent) {
  if (!pressing) {
    return
  }
  pressing = false
  const target = e.currentTarget as HTMLElement
  if (target.hasPointerCapture(e.pointerId)) {
    target.releasePointerCapture(e.pointerId)
  }
  if (props.disabled || props.value < 0.02) {
    return
  }
  emit('shoot')
}
</script>

<style scoped>
/* 对局页是深色沉浸场景，这里的颜色不套浅色主题 token，
   否则一条白底滑块会像贴在别人的界面上。 */
.pw {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  width: 100%;
  height: 100%;
  touch-action: none;
}

/* 轨道是一条凹进面板里的窄槽，跟参考视频一样：外面一圈深色底，
   里面一条更深的缝，滑块压在缝上。左右留出的余量让手指按在槽旁边也算按中。 */
.pw__track {
  position: relative;
  flex: 1;
  width: 12px;
  min-height: 120px;
  margin: 0 9px;
  background: #1a1d26;
  border-radius: 6px;
  box-shadow:
    inset 0 1px 3px rgba(0, 0, 0, 0.9),
    0 0 0 1px rgba(255, 255, 255, 0.2);
}

/* 填充从顶部往下长：顶部是 0，越往下越满。
   渐变末端的暖色提示「再往下就过头了」。 */
.pw__fill {
  position: absolute;
  top: 0;
  right: 0;
  left: 0;
  background: linear-gradient(to bottom, #3ea675 0%, #d7c447 62%, #e0613f 100%);
  border-radius: 6px;
  transition: none;
}

/* 手柄：一颗奶油色的圆，位置与填充底端一致。
   外圈一道暖白描边，压在深色槽上边界才清楚。 */
.pw__knob {
  position: absolute;
  top: 0;
  left: 50%;
  width: 20px;
  height: 20px;
  margin: -10px 0 0 -10px;
  background: radial-gradient(circle at 34% 30%, #ffffff 0%, #f3ead6 55%, #d9c8a8 100%);
  border-radius: 50%;
  box-shadow:
    0 1px 4px rgba(0, 0, 0, 0.6),
    0 0 0 1px rgba(255, 255, 255, 0.35);
}

.pw__value {
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: rgba(255, 255, 255, 0.72);
}

/* 禁用时整体压暗，仍保留形状，避免布局跳动。 */
.pw.is-off {
  opacity: 0.4;
}

.pw.is-off .pw__track {
  background: rgba(255, 255, 255, 0.08);
}
</style>
