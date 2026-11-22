<template>
  <div class="ft" :class="{ 'is-off': disabled }">
    <button
      class="ft__step"
      type="button"
      :disabled="disabled"
      aria-label="向左微调 0.1 度"
      @pointerdown.prevent="onStepDown(-STEP_DEG)"
      @pointerup="stopRepeat"
      @pointerleave="stopRepeat"
      @pointercancel="stopRepeat"
      @keydown.enter.prevent="nudge(-STEP_DEG)"
      @keydown.space.prevent="nudge(-STEP_DEG)"
    >
      −0.1°
    </button>

    <span class="ft__angle" :title="disabled ? '还没轮到你' : '当前瞄准角度'">
      {{ angleText }}°
    </span>

    <button
      class="ft__step"
      type="button"
      :disabled="disabled"
      aria-label="向右微调 0.1 度"
      @pointerdown.prevent="onStepDown(STEP_DEG)"
      @pointerup="stopRepeat"
      @pointerleave="stopRepeat"
      @pointercancel="stopRepeat"
      @keydown.enter.prevent="nudge(STEP_DEG)"
      @keydown.space.prevent="nudge(STEP_DEG)"
    >
      +0.1°
    </button>
  </div>
</template>

<script setup lang="ts">
// 底部微调：两个按钮各走 0.1°，中间那串数字是当前角度。
//
// 以前这里是一条「刻度尺」，按住横向拖动才转度数。问题在于它没有零点，
// 拖一下不知道转了多少，松手也没有停在哪一格的手感——玩家只能凭感觉试。
// 换成两个明确步长的按钮之后，按几下就是几格，角度数字一直在眼前。
//
// 按住不放会连发（350ms 之后每 70ms 一格）：差 1 度时不用真点十下。
// 只上报「转多少度」，不自己算 phi：角度的唯一来源在页面里。
import { computed, onBeforeUnmount } from 'vue'

// 一格的度数。台球瞄准差 0.1° 在 1.5 米外就是 2.6 毫米，够用了。
const STEP_DEG = 0.1

// 按住多久开始连发，以及连发的间隔。
const REPEAT_DELAY_MS = 350
const REPEAT_EVERY_MS = 70

const props = defineProps<{
  // 当前方位角，弧度。
  phi: number
  disabled?: boolean
}>()

const emit = defineEmits<{
  nudge: [deltaDeg: number]
}>()

// angleText 把弧度换成 0 到 360 的度数，保留一位小数。
// 不做归一化的话会出现「-37.4°」这种负数，读起来要心算。
const angleText = computed(() => {
  const deg = (props.phi * 180) / Math.PI
  const wrapped = ((deg % 360) + 360) % 360
  return wrapped.toFixed(1)
})

let delayTimer: ReturnType<typeof setTimeout> | null = null
let repeatTimer: ReturnType<typeof setInterval> | null = null

// nudge 上报一次固定步长的微调。
function nudge(deltaDeg: number) {
  if (props.disabled) {
    return
  }
  emit('nudge', deltaDeg)
}

// onStepDown 先把这一格走出去，再等一会儿开始连发。
// 立即走一格是必须的：只按一下（不到 350ms）也该生效。
function onStepDown(deltaDeg: number) {
  if (props.disabled) {
    return
  }
  nudge(deltaDeg)
  stopRepeat()
  delayTimer = setTimeout(() => {
    repeatTimer = setInterval(() => {
      emit('nudge', deltaDeg)
    }, REPEAT_EVERY_MS)
  }, REPEAT_DELAY_MS)
}

// stopRepeat 收掉连发。抬手、移出按钮、指针被系统取消都走它。
function stopRepeat() {
  if (delayTimer) {
    clearTimeout(delayTimer)
    delayTimer = null
  }
  if (repeatTimer) {
    clearInterval(repeatTimer)
    repeatTimer = null
  }
}

// 组件卸载时还有定时器在跑就会往已经销毁的父级发事件，收尾时必须停。
onBeforeUnmount(stopRepeat)
</script>

<style scoped>
/* 与力度条同样用深色场景的配色，不套浅色主题 token。 */
.ft {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  height: 36px;
}

.ft__step {
  flex: none;
  min-width: 58px;
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  color: rgba(255, 255, 255, 0.86);
  background: rgba(255, 255, 255, 0.09);
  border: none;
  border-radius: 8px;
  cursor: pointer;
  touch-action: none;
}

.ft__step:active {
  background: rgba(255, 255, 255, 0.2);
}

.ft__step:disabled {
  opacity: 0.35;
  cursor: default;
}

/* 角度数字用等宽字，调的时候数字不会左右跳。 */
.ft__angle {
  min-width: 68px;
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  color: rgba(255, 255, 255, 0.9);
  text-align: center;
}

.ft.is-off {
  opacity: 0.5;
}
</style>
