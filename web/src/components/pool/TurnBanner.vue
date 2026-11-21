<template>
  <!-- 换人横幅。从视频里照搬的做法：换人时整块横幅从中间滑过，
       一眼能看到「现在轮到谁」，比在角落里改一行小字清楚得多。
       它不是弹窗——不拦任何操作，0.4 秒后自己滑走。 -->
  <Transition name="tb">
    <div v-if="visible" class="tb" :class="{ 'is-mine': isMe }">
      <div class="tb__inner">
        <el-avatar
          :size="34"
          :src="avatar || undefined"
          :style="{ backgroundColor: avatarColor(name) }"
        >
          {{ initial }}
        </el-avatar>
        <div class="tb__text">
          <span class="tb__title">{{ isMe ? '你的回合' : '对手回合' }}</span>
          <span class="tb__sub">{{ subtitle }}</span>
        </div>
      </div>
    </div>
  </Transition>
</template>

<script setup lang="ts">
// 换人横幅。对局页在 turn 帧到达时把 visible 置 true，1.4 秒后置回 false。
//
// 计时放在页面而不是组件里：横幅的显示时长要和「读秒开始」这一步对齐，
// 组件自己管计时会让两件事各自跑一套定时器，调试时很难说清谁先谁后。
import { computed } from 'vue'
import { avatarColor } from '@/utils/avatar'
import { initials } from '@/utils/format'

const props = defineProps<{
  visible: boolean
  // isMe 为 true 表示轮到查看者自己，横幅用主色，否则用中性深色。
  isMe: boolean
  name: string
  avatar: string
  subtitle: string
}>()

const initial = computed(() => {
  if (!props.name) {
    return ''
  }
  return initials(props.name)
})
</script>

<style scoped>
/* 横幅横向铺满、垂直居中，压住球台但半透明：
   球在动的时候也能看到，不会错过一次落袋。 */
.tb {
  position: absolute;
  top: 50%;
  left: 0;
  right: 0;
  z-index: 30;
  display: flex;
  justify-content: center;
  transform: translateY(-50%);
  pointer-events: none;
}

.tb__inner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 22px 10px 12px;
  background: rgba(18, 22, 30, 0.82);
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 999px;
  backdrop-filter: blur(6px);
}

/* 轮到自己时整条横幅换主色，余光就能分辨，
   不用去读上面的字。 */
.tb.is-mine .tb__inner {
  background: rgba(76, 126, 243, 0.92);
  border-color: rgba(255, 255, 255, 0.24);
}

.tb__text {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.tb__title {
  font-size: 14px;
  font-weight: 600;
  color: #fff;
}

.tb__sub {
  font-size: 11px;
  color: rgba(255, 255, 255, 0.72);
}

/* 入场从左滑到中间、出场向右滑走：
   两个方向一致，看起来像一条横幅穿过屏幕，而不是淡入淡出。 */
.tb-enter-active,
.tb-leave-active {
  transition: opacity 0.24s ease, transform 0.24s ease;
}

.tb-enter-from {
  opacity: 0;
  transform: translateY(-50%) translateX(-40px);
}

.tb-leave-to {
  opacity: 0;
  transform: translateY(-50%) translateX(40px);
}
</style>
