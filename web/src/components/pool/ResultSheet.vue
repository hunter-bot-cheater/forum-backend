<template>
  <!-- 结算面板。全屏覆盖，不提供「关闭后继续打」这条路：
       一局已经结束了，留在球台上只会让人以为还能再出一杆。 -->
  <div class="rs" role="dialog" aria-modal="true">
    <div class="rs__card">
      <p class="rs__kicker">{{ won ? '这局你赢了' : '这局输了' }}</p>
      <p class="rs__reason">{{ reasonText }}</p>

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
//
// 正文来自 game/reasons.ts：战绩列表要同一段话，两处各写一份迟早分叉。
import { computed } from 'vue'
import { reasonLabel } from '@/game/reasons'

const props = defineProps<{
  // won 是「查看者赢了」而不是「谁赢了」，服务端给的 winner 是座位号，
  // 换算成这个布尔只在页面里做一次，组件不必再知道座位。
  won: boolean
  // reason 是服务端的 reason 码：eight_legal / eight_early / eight_with_foul /
  // timeout / leave。
  reason: string
}>()

const emit = defineEmits<{
  again: []
  lobby: []
}>()

const reasonText = computed(() => reasonLabel(props.reason, props.won))
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
  margin: 0 0 24px;
  font-size: 13px;
  color: #8a919c;
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
