<template>
  <!-- 结算面板。全屏覆盖，不提供「关闭后继续打」这条路：
       一局已经结束了，留在球台上只会让人以为还能再出一杆。 -->
  <div class="rs" role="dialog" aria-modal="true">
    <div class="rs__card">
      <p class="rs__kicker">{{ won ? '这局你赢了' : '这局输了' }}</p>
      <p class="rs__reason">{{ reasonText }}</p>

      <!-- 再约的三个状态共用一个位置：等待对方、对方已经点了、什么都没发生。
           同一件事（要不要再来一局）不该在卡片上出现两种说法。
           人机局是第四种：那边没有第二个人可以等，所以这一行不出现。 -->
      <p v-if="bot" class="rs__note">再打一局还是这台机器，它不会累</p>
      <p v-else-if="waiting" class="rs__note">已经告诉对方了，等他也点一下</p>
      <p v-else-if="peerName" class="rs__note is-peer">{{ peerName }} 想再打一局</p>

      <div class="rs__actions">
        <button class="rs__btn rs__btn--ghost" type="button" @click="emit('lobby')">回大厅</button>
        <button
          class="rs__btn rs__btn--primary"
          type="button"
          :disabled="waiting && !bot"
          @click="emit('again')"
        >
          {{ againLabel }}
        </button>
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
  // waiting 表示我已经点了「再约」，正等对方也点。这时候按钮只剩读秒作用，
  // 再点一次会打一个重复请求。
  waiting?: boolean
  // peerName 是「对方先点了再约」时对方的昵称，空串表示对方还没点。
  peerName?: string
  // bot 表示这一局是打人机：按钮变成「再打一局」，也不需要等任何人回应。
  bot?: boolean
}>()

const emit = defineEmits<{
  again: []
  lobby: []
}>()

const reasonText = computed(() => reasonLabel(props.reason, props.won))

// againLabel 是主按钮上的字。四种情况里只有「等对方回应」是一种进行中的状态，
// 其余三种都是「再开一局」这个动作本身，只是对象不同。
const againLabel = computed(() => {
  if (props.bot) {
    return '再打一局'
  }
  if (props.waiting) {
    return '等对方回应…'
  }
  if (props.peerName) {
    return '开局'
  }
  return '再约一局'
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
  max-width: 350px;
  padding: 34px 26px 24px;
  text-align: center;
  background: #161a21;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 20px;
  animation: rs-in 0.2s ease-out;
}

.rs__kicker {
  margin: 0 0 10px;
  font-size: 24px;
  font-weight: 800;
  color: #fff;
}

.rs__reason {
  margin: 0 0 22px;
  font-size: 14px;
  line-height: 1.5;
  color: #8a919c;
}

/* 再约的状态行。两种状态颜色不同：等对方是中性，对方已经点了是暖色——
   它其实是一个需要我表态的邀请，不该和一句普通说明长得一样。 */
.rs__note {
  margin: 0 0 18px;
  font-size: 14px;
  line-height: 1.5;
  color: #8a919c;
}

.rs__note.is-peer {
  color: #f0b866;
}

.rs__actions {
  display: flex;
  gap: 10px;
}

.rs__btn {
  flex: 1;
  height: 48px;
  font-size: 15px;
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

.rs__btn--primary:disabled {
  color: rgba(255, 255, 255, 0.72);
  background: rgba(76, 126, 243, 0.45);
  cursor: default;
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
