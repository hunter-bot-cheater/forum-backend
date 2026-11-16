<template>
  <div class="left">
    <span>在线人数：{{ onlineText }}</span>
  </div>
  <div class="right">
    <span class="username">{{ shownName }}</span>
    <el-button text type="primary" @click="logout">退出</el-button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useUserStore } from '@/store/modules/user'
import { useImStore } from '@/store/modules/im'

const router = useRouter()
const userStore = useUserStore()
const im = useImStore()

// 昵称优先：profile 还没拉回来时退回令牌里的用户名，最后才是「未登录」。
// 首屏那一瞬间 profile 是空的，直接用 nickname 会闪一下空白。
const shownName = computed(() => {
  if (userStore.displayName) {
    return userStore.displayName
  }
  if (userStore.username) {
    return userStore.username
  }
  return '未登录'
})

// 在线人数由 WebSocket 实时推送，模块 4 接入，这里先占位。
const onlineText = '—'

function logout() {
  userStore.resetToken()
  // 会话列表和未读角标也要清掉，否则下一个登录的人会先看到上一个人的私信。
  im.reset()
  router.replace('/login')
}
</script>

<style scoped>
.left,
.right {
  display: flex;
  align-items: center;
  gap: 12px;
}
.username {
  font-weight: 600;
}
</style>
