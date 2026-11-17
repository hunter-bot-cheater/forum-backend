import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { getToken } from '@/utils/auth'
import Layout from '@/layout/index.vue'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/login/index.vue'),
    meta: { title: '登录', public: true },
  },
  {
    path: '/register',
    name: 'Register',
    component: () => import('@/views/register/index.vue'),
    meta: { title: '注册', public: true },
  },
  {
    path: '/',
    component: Layout,
    redirect: '/articles',
    children: [
      {
        path: 'articles',
        name: 'ArticleList',
        component: () => import('@/views/article/index.vue'),
        meta: { title: '文章' },
      },
      {
        path: 'articles/:id',
        name: 'ArticleDetail',
        component: () => import('@/views/article/detail.vue'),
        meta: { title: '文章详情' },
        props: true,
      },
      {
        // 编辑文章。与 /write 共用同一个组件：页面上会读 :id 判断是新建还是编辑。
        // 段数比 /articles/:id 多一段，路由匹配不会互相抢。
        path: 'articles/:id/edit',
        name: 'ArticleEdit',
        component: () => import('@/views/article/edit.vue'),
        meta: { title: '编辑文章' },
      },
      {
        // 写作页单独占一个一级路径，不塞进 /articles/:id 下面：
        // 静态段虽然优先级更高，但 /articles/new 这种地址会让人误以为是一篇 id 叫 new 的文章。
        path: 'write',
        name: 'ArticleWrite',
        component: () => import('@/views/article/edit.vue'),
        meta: { title: '写文章' },
      },
      {
        path: 'hot',
        name: 'Hot',
        component: () => import('@/views/hot/index.vue'),
        meta: { title: '热榜' },
      },
      {
        path: 'notifications',
        name: 'Notifications',
        component: () => import('@/views/notification/index.vue'),
        meta: { title: '通知' },
      },
      {
        // 管理后台。路由守卫只做「有没有 token」这一件事，不在这里判断管理员：
        // 刷新页面时 profile 还没拉回来，isAdmin 一定是 false，
        // 那样的守卫会把真正的管理员也弹回列表页。页面内部等资料到了再决定显示什么，
        // 真正的权限边界在后端 /api/admin 的 RequireAdmin。
        path: 'admin',
        name: 'Admin',
        component: () => import('@/views/admin/index.vue'),
        meta: { title: '管理后台' },
      },
      {
        // 用户主页。id 用 props 之外的 route.params 读，所以不禁用 props。
        // 放在一级路径而不是 /users/me：同一个人只有一个地址，自己看自己也是这条，
        // 顶栏与详情页都只需要拼 id，不用判断「是不是我」再换地址。
        path: 'users/:id',
        name: 'UserProfile',
        component: () => import('@/views/user/index.vue'),
        meta: { title: '用户主页' },
      },
      {
        // 编辑资料。没有塞进 /users/:id 下面：那是一组「账号设置」的第一个页面，
        // 以后加改密码、通知偏好都往 settings/ 里放，地址不用再挪。
        path: 'settings/profile',
        name: 'ProfileSettings',
        component: () => import('@/views/user/settings.vue'),
        meta: { title: '编辑资料' },
      },
      {
        // userId 可选：/messages 只看会话列表，/messages/12 直接进入与 12 的聊天页。
        // 做成同一个组件的两个路径，是为了让「从别人主页点私信」只需要 push 一个地址，
        // 不必先创建会话再跳转——没聊过的人也能直接进来。
        path: 'messages/:userId?',
        name: 'Messages',
        component: () => import('@/views/message/index.vue'),
        // fullBleed：布局层去掉 el-main 的内边距，让聊天窗口铺满可用区域。
        // 带 20px 内边距的话，顶部标题和左栏会各缩进一块，看起来不像沉浸式私信页。
        meta: { title: '私信', fullBleed: true },
      },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/articles' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
  // 后退 / 前进回到上次离开时的高度，打开新页面回顶部。
  // 少了前半条，从详情页返回列表会被甩回列表最顶上，
  // 远看就像整页重新加载了一次——用户说的「大框架也跳转」多半是这个。
  scrollBehavior(_to, _from, savedPosition) {
    if (savedPosition) {
      return savedPosition
    }
    return { top: 0 }
  },
})

// 路由守卫：没 token 且不是公开页 → 去登录；已登录还去登录/注册 → 回文章列表。
router.beforeEach((to) => {
  const token = getToken()
  const isPublic = to.meta.public === true
  if (token) {
    if (isPublic) return '/articles'
    return true
  }
  return isPublic ? true : '/login'
})

export default router
