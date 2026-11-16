<template>
  <el-menu
    :default-active="activeMenu"
    router
    class="sidebar-menu"
    background-color="#304156"
    text-color="#bfcbd9"
    active-text-color="#409eff"
  >
    <el-menu-item index="/articles">
      <el-icon><Document /></el-icon>
      <span>文章</span>
    </el-menu-item>
    <el-menu-item index="/hot">
      <el-icon><Histogram /></el-icon>
      <span>热榜</span>
    </el-menu-item>
    <el-menu-item index="/messages">
      <el-icon><ChatDotRound /></el-icon>
      <span>私信</span>
      <el-badge
        v-if="im.totalUnread > 0"
        :value="im.totalUnread"
        :max="99"
        class="menu-badge"
      />
    </el-menu-item>
    <el-menu-item index="/notifications">
      <el-icon><Bell /></el-icon>
      <span>通知</span>
    </el-menu-item>
  </el-menu>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useImStore } from '@/store/modules/im'

const route = useRoute()
// 未读数来自登录用户自己的会话，未登录时组件不会渲染（登录页没有侧边栏），
// 所以这里不需要额外判空。
const im = useImStore()

// 详情页 /articles/:id 也要高亮「文章」菜单项。
const activeMenu = computed(() => {
  const seg = route.path.split('/')[1]
  return seg ? `/${seg}` : '/articles'
})
</script>

<style scoped>
/* 角标贴在菜单项右侧。用绝对定位而不是靠 margin 挤：
   el-menu-item 是 flex 布局，参与流式排列会把「私信」两个字挤歪。 */
.menu-badge {
  position: absolute;
  right: 14px;
  top: 50%;
  transform: translateY(-50%);
}

.menu-badge :deep(.el-badge__content) {
  border: none;
}
</style>
