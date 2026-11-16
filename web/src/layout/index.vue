<template>
  <div class="shell">
    <AppHeader />
    <!-- 主区域宽度交给每个页面自己控制（列表页要两栏、详情页要单栏窄版），
         外壳只负责背景、上下留白和「私信页铺满」这一个例外。 -->
    <main class="shell__main" :class="{ 'is-full-bleed': route.meta.fullBleed === true }">
      <router-view />
    </main>
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute } from 'vue-router'
import AppHeader from './components/AppHeader.vue'
import { getToken } from '@/utils/auth'
import { connectWS } from '@/utils/ws'
import { useUserStore } from '@/store/modules/user'
import { useImStore } from '@/store/modules/im'
import { useNotificationStore } from '@/store/modules/notification'

// route 用来判断当前页面要不要「铺满」：私信页要，其它带内边距的页面不要。
const route = useRoute()
const user = useUserStore()
const im = useImStore()
const notification = useNotificationStore()

onMounted(() => {
  const token = getToken()
  if (!token) {
    return
  }
  // 布局组件只在登录后的页面出现，所以这里是「进入应用」的统一入口。
  //
  // 刷新页面后内存里只剩 localStorage 里的 token：用户资料、会话列表、WebSocket
  // 全都要重建一遍。几件事都在这里做，避免每个页面各自判断登录态。
  // 顺序上先注册帧处理再建连：连接一旦建立就可能立刻收到推送，
  // 处理函数还没挂上就会把那一帧丢掉。
  im.initWS()
  notification.initWS()
  connectWS(token)
  void user.fetchMe()
  void im.refresh()
  // 通知只拉计数（一次 GROUP BY），列表由通知页自己按分类按页拉，
  // 首屏没必要把所有通知正文都取回来。
  void notification.refreshSummary()
})
</script>

<style scoped>
.shell {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  background: var(--fs-bg);
}

.shell__main {
  flex: 1;
  padding: 20px 24px 48px;
}

/* 沉浸式页面（私信）：去掉内边距并锁死高度，让聊天区自己滚。
   高度用 flex 拉满而不是指望页面写 height: 100%：
   子项的百分比高度要能解析，前提是父级高度是「确定值」，
   而主区域的高度是 flex 分配出来的，实测 100% 会退化成内容高度（聊天页只有 356px 高），
   整页就缩成一条。改成 flex 容器 + 子项 flex: 1，高度由布局层直接给死。 */
.is-full-bleed {
  display: flex;
  /* 必须是纵向：默认的 row 只会把子项在宽度上撑开，
     高度走交叉轴，而子项自己写了 height: 100% 时 stretch 不生效。 */
  flex-direction: column;
  padding: 0;
  /* flex: none 不能省：主区域的 flex: 1 是 flex-basis: 0，高度会被「内容撑开」这条规则接管，
     页面里消息一多，主区域就从 840 长到 1172，整页出现纵向滚动条、
     输入框被顶到屏幕外。写死高度才对得上「沉浸式」这三个字。 */
  flex: none;
  height: calc(100vh - var(--fs-header-h));
  overflow: hidden;
}

.is-full-bleed > :deep(*) {
  flex: 1;
  min-height: 0;
}

@media (max-width: 720px) {
  .shell__main {
    padding: 12px 12px 32px;
  }

  /* 沉浸式页面（私信）整屏铺：手机屏幕上多一圈 12px 白边，
     气泡和输入框都会缩一圈，聊天窗看起来更挤。 */
  .shell__main.is-full-bleed {
    padding: 0;
  }
}
</style>
