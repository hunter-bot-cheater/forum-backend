<template>
  <header class="hdr">
    <div class="hdr__inner">
      <router-link to="/articles" class="hdr__brand">
        <span class="hdr__logo">论</span>
        <span class="hdr__name">论坛</span>
      </router-link>

      <!-- 主导航只放两个内容入口。私信和通知做成右侧图标：
           它们有未读数、需要常驻可见，但不像「文章/热榜」那样是浏览主线，
           平铺成四个导航项会让主次不分。 -->
      <nav class="hdr__nav">
        <router-link
          v-for="item in navs"
          :key="item.path"
          :to="item.path"
          class="hdr__link"
          :class="{ 'is-active': isActive(item.path) }"
        >
          {{ item.label }}
        </router-link>
      </nav>

      <div class="hdr__actions">
        <!-- 写文章是社区里唯一的「生产」动作，做成主色按钮常驻：
             藏进头像下拉菜单里，新用户永远找不到入口，
             论坛就只剩下「看」。 -->
        <el-button type="primary" class="hdr__write" title="写文章" @click="goWrite">
          <el-icon class="hdr__write-icon"><EditPen /></el-icon>
          <span class="hdr__write-text">写文章</span>
        </el-button>

        <router-link to="/messages" class="hdr__icon" title="私信">
          <el-badge :value="im.totalUnread" :max="99" :hidden="im.totalUnread === 0">
            <el-icon :size="20"><ChatDotRound /></el-icon>
          </el-badge>
        </router-link>

        <router-link to="/notifications" class="hdr__icon" title="通知">
          <el-badge :value="notification.summary.total" :max="99" :hidden="notification.summary.total === 0">
            <el-icon :size="20"><Bell /></el-icon>
          </el-badge>
        </router-link>

        <el-dropdown trigger="click" @command="onCommand">
          <div class="hdr__user">
            <el-avatar :size="28" :src="avatar">{{ initial }}</el-avatar>
            <span v-if="shownName" class="hdr__user-name">{{ shownName }}</span>
            <el-icon :size="12" class="hdr__caret"><ArrowDown /></el-icon>
          </div>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="profile">我的主页</el-dropdown-item>
              <el-dropdown-item command="settings">编辑资料</el-dropdown-item>
              <!-- 只有白名单里的账号才看到这一项。isAdmin 来自 /users/me 的
                   is_admin，未加载完时是 false，所以刷新后这一项会晚几百毫秒出现。 -->
              <el-dropdown-item v-if="userStore.isAdmin" command="admin">管理后台</el-dropdown-item>
              <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </div>
  </header>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useUserStore } from '@/store/modules/user'
import { useImStore } from '@/store/modules/im'
import { useNotificationStore } from '@/store/modules/notification'
import { initials } from '@/utils/format'

const route = useRoute()
const router = useRouter()
// 未读数来自登录用户自己的会话与通知；登录页没有这个顶栏，所以不必判空。
const userStore = useUserStore()
const im = useImStore()
const notification = useNotificationStore()

const navs = [
  { path: '/articles', label: '文章' },
  { path: '/hot', label: '热榜' },
]

// 详情页 /articles/:id 也要让「文章」保持选中：
// 只比全等的话，点进详情后导航会全部变成未选中，像离开了这个栏目。
function isActive(path: string): boolean {
  return route.path === path || route.path.startsWith(path + '/')
}

// 昵称优先：profile 还没拉回来时退回用户名。
//
// 有 token 但 profile 还在路上时返回空串，让名字整段不渲染。
// 这一瞬间写成「未登录」是错的——用户明明登录了，闪一下「未登录」再变回昵称，
// 看起来像掉线又自动重登。真的没 token（登录页没有顶栏，只有 401 后的瞬间）才写「未登录」。
const shownName = computed(() => {
  if (userStore.displayName) {
    return userStore.displayName
  }
  if (userStore.username) {
    return userStore.username
  }
  if (userStore.token) {
    return ''
  }
  return '未登录'
})

const avatar = computed(() => {
  if (userStore.profile && userStore.profile.avatar_url) {
    return userStore.profile.avatar_url
  }
  return undefined
})

// 名字为空时头像里也不放字：放个「?」比空白更像出错。
const initial = computed(() => {
  if (!shownName.value) {
    return ''
  }
  return initials(shownName.value)
})

function onCommand(command: string) {
  if (command === 'logout') {
    logout()
    return
  }
  if (command === 'profile') {
    goProfile()
    return
  }
  if (command === 'settings') {
    goSettings()
  }
  if (command === 'admin') {
    goAdmin()
  }
}

// 下拉里的「我的主页」要拼自己的 id。profile 还没拉回来时 userId 是 0，
// 这时点了也拼不出正确地址，直接忽略——这一瞬间只有几百毫秒，
// 比跳到 /users/0 再吃一个 404 好。
function goProfile() {
  if (userStore.userId === 0) {
    return
  }
  void router.push('/users/' + String(userStore.userId))
}

// 编辑资料页自己从 store / 接口取资料，不需要 id，任何时候都能进。
function goSettings() {
  void router.push('/settings/profile')
}

// 管理后台。菜单项已经按 isAdmin 过滤，这里不再判一次：
// 就算有人手动敲 /admin，页面会显示「只有管理员能打开」，
// 后端接口也会 403，前端这层判断只是为了不让入口出现在普通用户面前。
function goAdmin() {
  void router.push('/admin')
}

// 写文章是登录后才能做的事。这里不自己判 token，交给路由守卫：
// 统一的「没 token 就跳登录」只有一处逻辑，页面里再判一遍会出现两套口径。
function goWrite() {
  void router.push('/write')
}

async function logout() {
  // 先让服务端撤销 refresh（store.logout 内部完成），再跳登录页。
  // 私信、通知、文章列表这些账号级 store 由 utils/auth 的回调表统一清理，
  // 这里不再逐个 store 手动 reset：漏掉一个的表现是「下一个人登录看到上一个人的数据」。
  // 接口失败也照样退出，所以不用 try/catch 拦住跳转。
  await userStore.logout()
  void router.replace('/login')
}
</script>

<style scoped>
.hdr {
  /* 吸顶而不是固定定位：固定定位要额外给内容区补等高的 padding，
     漏了就有一块内容被顶栏压住。sticky 参与文档流，高度自动让出来。 */
  position: sticky;
  top: 0;
  z-index: 100;
  height: var(--fs-header-h);
  background: rgba(255, 255, 255, 0.88);
  /* 半透明加模糊：滚动时内容从顶栏下面透出来一点，不至于像被切断。 */
  backdrop-filter: saturate(180%) blur(8px);
  border-bottom: 1px solid var(--fs-line);
}

.hdr__inner {
  display: flex;
  align-items: center;
  gap: 36px;
  height: 100%;
  max-width: var(--fs-content-max);
  margin: 0 auto;
  padding: 0 24px;
}

.hdr__brand {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: none;
}

/* 品牌标记用一块主色圆角方块：整站只有一个强调色，
   标志用同一支蓝，视觉上自然连成一套。 */
.hdr__logo {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: 9px;
  background: var(--fs-primary);
  color: #fff;
  font-size: 16px;
  font-weight: 700;
}

.hdr__name {
  font-size: 17px;
  font-weight: 700;
  letter-spacing: 1px;
  color: var(--fs-text-1);
}

.hdr__nav {
  display: flex;
  align-items: center;
  gap: 4px;
  flex: 1;
}

.hdr__link {
  padding: 6px 12px;
  border-radius: var(--fs-radius-sm);
  font-size: 15px;
  color: var(--fs-text-2);
  transition: color 0.15s, background-color 0.15s;
}

.hdr__link:hover {
  background: var(--fs-surface-hover);
  color: var(--fs-text-1);
}

/* 选中态用「加粗 + 主色」，不加下划线：
   下划线会和顶栏底边线凑成两条平行线，看着像错位。 */
.hdr__link.is-active {
  color: var(--fs-primary);
  font-weight: 600;
}

.hdr__actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: none;
}

/* 写文章按钮压到 34px 高，和两侧 36px 的图标按钮视觉齐平：
   el-button 默认 32px 高、字号 14px，在 60px 顶栏里会显得偏小。 */
.hdr__write {
  height: 34px;
  margin-right: 6px;
  padding: 0 14px;
  font-size: 14px;
}

.hdr__write-icon {
  margin-right: 4px;
}

/* 图标按钮做成 36px 方形热区：图标本身 20px，直接当点击区域太小，
   鼠标经常点空。 */
.hdr__icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border-radius: var(--fs-radius-sm);
  color: var(--fs-text-2);
  transition: background-color 0.15s, color 0.15s;
}

.hdr__icon:hover {
  background: var(--fs-surface-hover);
  color: var(--fs-text-1);
}

/* el-badge 默认是 inline-block，自身带着一行行高和基线对齐，
   塞进 36px 方框后图标会被往上推 4px：实测铃铛中线在 26px，
   而头像、写文章按钮都在 30px，一排看过去像是没对齐。
   改成 flex 让它的高度收缩成图标本身，和两侧元素落在同一条中线上。 */
.hdr__icon :deep(.el-badge) {
  display: flex;
  align-items: center;
}

/* el-badge 的角标默认贴在内容右上角，图标有内边距时会被裁掉一点，
   这里给角标单独推几像素。 */
.hdr__icon :deep(.el-badge__content) {
  top: 2px;
  right: 4px;
}

.hdr__user {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 36px;
  padding: 0 8px 0 4px;
  border-radius: var(--fs-radius-sm);
  cursor: pointer;
  transition: background-color 0.15s;
}

.hdr__user:hover {
  background: var(--fs-surface-hover);
}

.hdr__user-name {
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  color: var(--fs-text-1);
}

.hdr__caret {
  color: var(--fs-text-3);
}

/* 窄屏：品牌名与用户名让位给导航，只留图标。
   不隐藏导航——论坛的主要动作就是浏览文章。 */
@media (max-width: 720px) {
  .hdr__inner {
    gap: 12px;
    padding: 0 14px;
  }

  .hdr__name,
  .hdr__user-name {
    display: none;
  }

  /* 窄屏下写文章只留图标：文字宽度加上两边图标会挤掉导航。
     按钮本身保留——它是入口，不因为屏幕窄就消失。 */
  .hdr__write {
    padding: 0 10px;
  }

  .hdr__write-text {
    display: none;
  }

  .hdr__write-icon {
    margin-right: 0;
  }
}
</style>
