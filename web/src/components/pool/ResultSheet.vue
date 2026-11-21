<template>
  <!-- 结算面板。全屏覆盖，不提供「关闭后继续打」这条路：
       一局已经结束了，留在球台上只会让人以为还能再出一杆。 -->
  <div class="rs" role="dialog" aria-modal="true">
    <div class="rs__card">
      <p class="rs__kicker">{{ won ? '这局你赢了' : '这局输了' }}</p>
      <p class="rs__reason">{{ reasonText }}</p>

      <div v-if="delta !== null" class="rs__elo">
        <span class="rs__elo-label">积分</span>
        <!-- 增长用红色、下降用绿色（国内习惯，与股市一致）。
             不用 ± 号让用户自己读：颜色加符号一起看才不容易看反。 -->
        <span class="rs__elo-delta" :class="delta >= 0 ? 'is-up' : 'is-down'">
          {{ delta > 0 ? '+' : '' }}{{ delta }}
        </span>
      </div>

      <div class="rs__actions">
        <button class="rs__btn rs__btn--ghost" type="button" @click="emit('lobby')">回大厅</button>
        <button class="rs__btn rs__btn--primary" type="button" @click="emit('again')">再约一局</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
// 对局结算。文案全部在前端定，服务端只给机读码：
// 改一句中文不该牵动后端发版，样式分支也不该依赖中文串。
import { computed } from 'vue'

const props = defineProps<{
  // won 是「查看者赢了」而不是「谁赢了」，服务端给的 winner 是座位号，
  // 换算成这个布尔只在页面里做一次，组件不必再知道座位。
  won: boolean
  // reason 是服务端的 reason 码：eight_legal / eight_early / eight_with_foul /
  // timeout / leave。
  reason: string
  // delta 是查看者自己的积分变化，null 表示这一局没有积分数据。
  //
  // 为什么允许 null：结算帧里的 elo 字段目前恒为 0（积分是在落库那一步算的，
  // 那一刻广播已经发出去了）。用 0 展示会写成「+0」，看起来像积分没生效。
  delta: number | null
}>()

const emit = defineEmits<{
  again: []
  lobby: []
}>()

// REASONS 把机读码翻成一句人话。
// 未登记的原因不加兜底猜测，直接说「本局结束」：编一句可能错的原因更糟。
const REASONS: Record<string, string> = {
  eight_legal: '打进黑八，赢下这一局',
  eight_early: '黑八提前落袋',
  eight_with_foul: '犯规同时打进黑八',
  timeout: '连续两次超时，判负',
  leave: '中途离开，判负',
}

const reasonText = computed(() => {
  const text = REASONS[props.reason]
  if (text) {
    return text
  }
  return '本局结束'
})
</script>

<style scoped>
.rs {
  position: fixed;
  inset: 0;
  z-index: 1400;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: rgba(9, 12, 18, 0.62);
  backdrop-filter: blur(3px);
}

.rs__card {
  width: 100%;
  max-width: 300px;
  padding: 28px 22px 20px;
  text-align: center;
  background: #161a21;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 18px;
  animation: rs-in 0.2s ease-out;
}

.rs__kicker {
  margin: 0 0 8px;
  font-size: 20px;
  font-weight: 700;
  color: #fff;
}

.rs__reason {
  margin: 0 0 20px;
  font-size: 13px;
  color: #8a919c;
}

.rs__elo {
  display: flex;
  align-items: baseline;
  justify-content: center;
  gap: 8px;
  margin-bottom: 24px;
}

.rs__elo-label {
  font-size: 12px;
  color: #8a919c;
}

.rs__elo-delta {
  font-size: 22px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

.rs__elo-delta.is-up {
  color: #e0553f;
}

.rs__elo-delta.is-down {
  color: #35a06a;
}

.rs__actions {
  display: flex;
  gap: 10px;
}

.rs__btn {
  flex: 1;
  height: 42px;
  font-size: 14px;
  font-weight: 600;
  border: none;
  border-radius: 12px;
  cursor: pointer;
}

.rs__btn--ghost {
  color: #c8ccd3;
  background: rgba(255, 255, 255, 0.08);
}

.rs__btn--primary {
  color: #fff;
  background: #4c7ef3;
}

@keyframes rs-in {
  from {
    opacity: 0;
    transform: translateY(12px) scale(0.98);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
</style>
