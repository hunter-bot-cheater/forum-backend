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
    <el-menu-item index="/notifications">
      <el-icon><Bell /></el-icon>
      <span>通知</span>
    </el-menu-item>
  </el-menu>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()
// 详情页 /articles/:id 也要高亮「文章」菜单项。
const activeMenu = computed(() => {
  const seg = route.path.split('/')[1]
  return seg ? `/${seg}` : '/articles'
})
</script>
