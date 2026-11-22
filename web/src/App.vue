<template>
  <!-- 同一个路由换参数时要重建页面，否则组件会被复用：「再约一局」跳到新的一局，
       路由还是 /pool/match/:id，Vue 会认为是同一个页面、只更新 props，
       而球台、WebSocket、读秒全都在挂载时一次性建好，结果人停在新局的地址上、
       画面还是上一局的结算卡。

       只对这两个全屏页加 key（对局与回放）：其它页面的查询参数是给页面自己
       响应式处理的，一律重建反而会丢状态。 -->
  <router-view v-slot="{ Component }">
    <component :is="Component" :key="routeKey" />
  </router-view>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()

// routeKey 只在「同一条路由、不同参数」有意义的那两页给出值。
// 其它页面返回 undefined，等同不加 key，保持原来的复用行为。
const routeKey = computed(() => {
  if (route.name === 'PoolMatch' || route.name === 'PoolReplay') {
    return route.fullPath
  }
  return undefined
})
</script>
