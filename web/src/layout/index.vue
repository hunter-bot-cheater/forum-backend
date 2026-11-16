<template>
  <el-container class="app-wrapper">
    <el-aside width="200px" class="sidebar-container">
      <div class="logo">论坛</div>
      <Sidebar />
    </el-aside>
    <el-container>
      <el-header class="navbar">
        <Navbar />
      </el-header>
      <el-main :class="{ 'is-full-bleed': route.meta.fullBleed === true }">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute } from 'vue-router'
import Sidebar from './components/Sidebar.vue'
import Navbar from './components/Navbar.vue'
import { getToken } from '@/utils/auth'
import { connectWS } from '@/utils/ws'
import { useUserStore } from '@/store/modules/user'
import { useImStore } from '@/store/modules/im'

// route 用来判断当前页面要不要「铺满」：私信页要，其它带内边距的页面不要。
const route = useRoute()
const user = useUserStore()
const im = useImStore()

onMounted(() => {
  const token = getToken()
  if (!token) {
    return
  }
  // 布局组件只在登录后的页面出现，所以这里是「进入应用」的统一入口。
  //
  // 刷新页面后内存里只剩 localStorage 里的 token：用户资料、会话列表、WebSocket
  // 全都要重建一遍。三件事都在这里做，避免每个页面各自判断登录态。
  // 顺序上先注册帧处理再建连：连接一旦建立就可能立刻收到推送，
  // 处理函数还没挂上就会把那一帧丢掉。
  im.initWS()
  connectWS(token)
  void user.fetchMe()
  void im.refresh()
})
</script>

<style scoped>
.app-wrapper {
  height: 100vh;
}

/* 沉浸式页面（私信）：去掉 el-main 的 20px 内边距和自身滚动。
   两件事必须一起去掉——只去内边距的话，聊天区内部滚动和外层 el-main 滚动
   会各滚一半，滚轮手感很怪。 */
.is-full-bleed {
  padding: 0;
  overflow: hidden;
}
</style>
