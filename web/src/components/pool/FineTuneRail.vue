<template>
  <div class="ft" :class="{ 'is-off': disabled }">
    <button
      class="ft__step"
      type="button"
      :disabled="disabled"
      aria-label="向左微调 0.05 度"
      @pointerdown.prevent="onStepDown(-STEP_DEG)"
      @pointerup="stopRepeat"
      @pointerleave="stopRepeat"
      @pointercancel="stopRepeat"
      @keydown.enter.prevent="nudge(-STEP_DEG)"
      @keydown.space.prevent="nudge(-STEP_DEG)"
    >
      −0.05°
    </button>

    <span class="ft__angle" :title="disabled ? '还没轮到你' : '当前瞄准角度'">
      {{ angleText }}°
    </span>

    <button
      class="ft__step"
      type="button"
      :disabled="disabled"
      aria-label="向右微调 0.05 度"
      @pointerdown.prevent="onStepDown(STEP_DEG)"
      @pointerup="stopRepeat"
      @pointerleave="stopRepeat"
      @pointercancel="stopRepeat"
      @keydown.enter.prevent="nudge(STEP_DEG)"
      @keydown.space.prevent="nudge(STEP_DEG)"
    >
      +0.05°
    </button>
  </div>
</template>

<script setup lang="ts">
// 底部微调：两个按钮各走 0.05°，中间那串数字是当前角度。
//
// 以前这里是一条「刻度尺」，按住横向拖动才转度数。问题在于它没有零点，
// 拖一下不知道转了多少，松手也没有停在哪一格的手感——玩家只能凭感觉试。
// 换成两个明确步长的按钮之后，按几下就是几格，角度数字一直在眼前。
//
// 按住不放会连发（350ms 之后每 55ms 一格）：差 1 度时不用真点二十下。
// 只上报「转多少度」，不自己算 phi：角度的唯一来源在页面里。
import { computed, onBeforeUnmount } from 'vue'

// 一格的度数。0.1° 时在 1.5 米外差 2.6 毫米，打厚打薄都调不到那么细，
// 于是收到 0.02°；0.02 又被反馈「按十下才挪一点、太磨人」，回到 0.05°
// （1.5 米外 1.3 毫米，比一个球皮还细）。读数保留两位小数，按一下能看见变化。
const STEP_DEG = 0.05

// 按住多久开始连发，以及连发的间隔。
//
// 步长是 0.05 之后，55ms 一格等于 0.9°/秒：长按两秒能转过小两度，
// 又不会快到一下冲过头。
const REPEAT_DELAY_MS = 350
const REPEAT_EVERY_MS = 55

const props = defineProps<{
  // 当前方位角，弧度。
  phi: number
  disabled?: boolean
}>()

const emit = defineEmits<{
  nudge: [deltaDeg: number]
}>()

// angleText 把弧度换成 0 到 360 的度数，保留两位小数。
// 不做归一化的话会出现「-37.4°」这种负数，读起来要心算。
// 两位小数是跟着 0.05° 的步长走的：一位小数时按一下读数会从 12.3 跳到 12.4 再跳回，
// 看起来像没生效（0.05 的一半就进位了）。
const angleText = computed(() => {
  const deg = (props.phi * 180) / Math.PI
  const wrapped = ((deg % 360) + 360) % 360
  return wrapped.toFixed(2)
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
  /* 34px 是「够点、又不占地方」的那一档：手机竖屏每一像素底部高度
     都能换成球台宽度（0.55 倍），这一行原来 42px，收掉 8px。 */
  height: 34px;
}

.ft__step {
  flex: none;
  min-width: 62px;
  height: 30px;
  padding: 0 10px;
  /* 底栏这一行是出杆前一直在用的东西，13px 在深色底上仍然看得清。 */
  font-size: 13px;
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

/* 角度数字用等宽字，调的时候数字不会左右跳。
   15px 比正文略大一号：它是这一行里唯一在变的读数，
   但也不该大到把两边按钮推开（两位小数 + 度号，84px 刚好）。 */
.ft__angle {
  min-width: 84px;
  font-size: 15px;
  font-variant-numeric: tabular-nums;
  color: rgba(255, 255, 255, 0.9);
  text-align: center;
}

.ft.is-off {
  opacity: 0.5;
}
</style>
