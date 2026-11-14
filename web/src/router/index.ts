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
