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
