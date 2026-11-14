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
      <el-main>
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup lang="ts">
import Sidebar from './components/Sidebar.vue'
import Navbar from './components/Navbar.vue'
</script>

<style scoped>
.app-wrapper {
  height: 100vh;
}
</style>
