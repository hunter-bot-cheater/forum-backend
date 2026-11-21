<template>
  <div class="shell">
    <AppHeader />
    <!-- 主区域宽度交给每个页面自己控制（列表页要两栏、详情页要单栏窄版），
         外壳只负责背景、上下留白和「私信页铺满」这一个例外。 -->
    <main class="shell__main" :class="{ 'is-full-bleed': route.meta.fullBleed === true }">
      <router-view />
    </main>

    <!-- 台球邀请弹层。挂在布局里而不是大厅页：邀请到达时用户可能正在读文章，
         弹层必须到处都在。它是固定定位的，放在最后不影响文档流。
         对局页不套这个布局，所以打球时不会被邀请打断——那条邀请会留在
         store 里，回到大厅才弹出来。 -->
    <InviteRequest />
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute } from 'vue-router'
import AppHeader from './components/AppHeader.vue'
import InviteRequest from '@/components/pool/InviteRequest.vue'
import { getToken } from '@/utils/auth'
import { connectWS } from '@/utils/ws'
import { useUserStore } from '@/store/modules/user'
import { useImStore } from '@/store/modules/im'
import { useNotificationStore } from '@/store/modules/notification'
import { usePoolStore } from '@/store/modules/pool'

// route 用来判断当前页面要不要「铺满」：私信页要，其它带内边距的页面不要。
const route = useRoute()
const user = useUserStore()
const im = useImStore()
const notification = useNotificationStore()
const pool = usePoolStore()

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
  pool.initWS()
  connectWS(token)
  void user.fetchMe()
  void im.refresh()
  // 通知只拉计数（一次 GROUP BY），列表由通知页自己按分类按页拉，
  // 首屏没必要把所有通知正文都取回来。
  void notification.refreshSummary()
  // 待接受的邀请只靠推送会漏：刷新页面时若正好有邀请在路上，
  // 那一条推送已经发过了，补拉一次才能把它找回来。
  void pool.refresh()
})
</script>

<style scoped>
.shell {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  /* iOS Safari 的 100vh 是「地址栏收起时」的高度，比当前可视区高 60 到 100px。
     用它撑底会让页面底部一段永远在屏幕外。dvh 跟着工具栏伸缩走，
     写两行是先给老浏览器一个可用的值，再让新浏览器覆盖。 */
  min-height: 100dvh;
  background: var(--fs-bg);
}

.shell__main {
  flex: 1;
  padding: 20px 24px 48px;
}

/* 换页时让内容淡入一下。只做进入、不做离场：
   离场动画要先播完才换内容（transition 的 out-in），中间会空一帧，
   看起来反而更像整页跳转。
   子组件只有一个根节点时会带上本组件的作用域属性，所以这条能选中页面本身。 */
.shell__main > :deep(*) {
  animation: page-in 0.16s ease-out;
}

@keyframes page-in {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}

/* 系统里关掉了动画的人不该看到任何淡入。 */
@media (prefers-reduced-motion: reduce) {
  .shell__main > :deep(*) {
    animation: none;
  }
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
  /* 手机上必须用 dvh。iOS Safari 的 100vh 把底部工具栏那一条也算进去了，
     照它算高度，聊天输入框正好被工具栏压在屏幕外；而这里 overflow: hidden，
     用户连滚都滚不到，表现就是「输入框没了、页面拖不动」。
     dvh 是当前可视高度，键盘弹起时也会跟着缩，输入框始终留在屏幕内。 */
  height: calc(100dvh - var(--fs-header-h));
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
