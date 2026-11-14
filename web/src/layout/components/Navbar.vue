<template>
  <div class="left">
    <span>在线人数：{{ onlineText }}</span>
  </div>
  <div class="right">
    <span class="username">{{ userStore.username || '未登录' }}</span>
    <el-button text type="primary" @click="logout">退出</el-button>
  </div>
</template>

<script setup lang="ts">
import { useRouter } from 'vue-router'
import { useUserStore } from '@/store/modules/user'

const router = useRouter()
const userStore = useUserStore()

// 在线人数由 WebSocket 实时推送，模块 4 接入，这里先占位。
const onlineText = '—'

function logout() {
  userStore.resetToken()
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
