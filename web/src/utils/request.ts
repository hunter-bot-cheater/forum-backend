import axios, {
  type AxiosInstance,
  type AxiosRequestConfig,
  type AxiosResponse,
  type InternalAxiosRequestConfig,
} from 'axios'
import { ElMessage } from 'element-plus'
import {
  getToken,
  setToken,
  getRefreshToken,
  setRefreshToken,
  clearTokens,
} from '@/utils/auth'
import router from '@/router'
import { useUserStore } from '@/store/modules/user'

// 这套结构照搬 vue3-element-admin 的 src/utils/request.ts：
// 一个 axios 实例 + 请求拦截器统一带 token + 响应拦截器统一拆包 {code,message,data}。
//
// 后端响应约定（见 backend/pkg/response）：
//   成功：HTTP 200，{"code":0,"message":"ok","data":{...}}，不带下面两个错误字段
//   失败：HTTP 非 2xx（400/401/404/405/429...），
//         {"code":1,"message":"...","data":null,"error_code":"...","request_id":"..."}
//   error_code 是机读错误码，改中文文案不会变；request_id 也放在 X-Request-ID 响应头。
// 所以响应拦截器成功时直接返回 res.data（即真正的业务数据），
// 页面里拿到的是 data 字段本身，不用再 res.data.data.data 层层取。
const baseURL = import.meta.env.VITE_API_BASE_URL || '/api'

const service: AxiosInstance = axios.create({
  baseURL,
  timeout: 10000,
})

// RetryableConfig 给 axios 配置加一个私有标记，用来区分「首次请求」与「刷新后重放」。
// 不标的话，重放又拿到 401 会再触发一轮刷新，刷新接口本身出问题时会无限循环。
interface RetryableConfig extends InternalAxiosRequestConfig {
  _retried?: boolean
}

// isAuthEndpoint 判断这个请求本身是不是认证接口。
//
// 认证接口的 401 有确定含义，不能走刷新流程：
//   - /login 401 是密码错，刷新只会让「输错密码」变成一串额外请求；
//   - /auth/refresh 401 是 refresh 真的失效了，再去刷新就是递归；
//   - /auth/logout 不需要身份，401 也不该拦住退出。
function isAuthEndpoint(url: string | undefined): boolean {
  if (!url) {
    return false
  }
  return url === '/login' || url === '/register' || url.startsWith('/auth/')
}

// ===== 刷新锁与等待队列 =====
//
// 首页一进来会并发好几个请求。access 过期时它们会同时收到 401，
// 如果每个都去刷新，就会有 N 个刷新请求，而 refresh 是「用一次就作废」的：
// 第一个成功，其余全部失败并触发泄露撤销，用户被直接踢下线。
//
// 所以这里只有一把锁：
//   refreshing 为 true 时，后来的 401 不再发请求，而是把 resolve 塞进 waiters 排队；
//   刷新结束后，无论成败，一次性唤醒队列。
// 这就是「第一个去刷新，后续挂起」的实现，锁是 refreshing，队列是 waiters。

let refreshing = false
let waiters: Array<(token: string | null) => void> = []

// requestNewToken 用 refresh 换一对新 token，并就地更新本地存储。
// 用裸 axios 而不是 service：走实例会再进一遍拦截器，
// 万一刷新本身 401，就会递归调用到这里。
async function requestNewToken(): Promise<string> {
  const refresh = getRefreshToken()
  if (!refresh) {
    throw new Error('没有可用的 refresh token')
  }

  const resp = await axios.post(
    baseURL + '/auth/refresh',
    { refresh_token: refresh },
    { timeout: 10000, headers: { 'Content-Type': 'application/json' } },
  )
  const body = resp.data
  if (!body || body.code !== 0 || !body.data) {
    throw new Error('刷新响应格式不正确')
  }
  // 先存再返回：调用方拿到 token 后会立刻重放请求，
  // 而请求拦截器读的是 localStorage，存晚了会带着旧 access 再 401 一次。
  setToken(body.data.access_token)
  setRefreshToken(body.data.refresh_token)
  return body.data.access_token
}

// flushWaiters 把队列一次清空并逐个唤醒。
// 先取出来再清空：唤醒时如果某个回调同步触发了新的 401，
// 它应该去排下一轮队，而不是往正在遍历的数组里追加。
function flushWaiters(token: string | null): void {
  const pending = waiters
  waiters = []
  for (const resolve of pending) {
    resolve(token)
  }
}

// waitForToken 返回一个「可用的新 access」，必要时触发刷新。
// 已经有请求在刷新时，这里返回的 Promise 会一直挂着，直到那一轮刷新结束。
function waitForToken(): Promise<string | null> {
  if (refreshing) {
    return new Promise((resolve) => {
      waiters.push(resolve)
    })
  }

  refreshing = true
  return requestNewToken()
    .then((token) => {
      // 先松锁再唤醒队列，两步放在同一个同步块里：
      // finally 是另一个微任务，如果在那里松锁，恰好在这个间隙收到 401 的请求
      // 会看到 refreshing 仍为 true 而排进队列，可队列刚被清空，它就永远挂着。
      refreshing = false
      flushWaiters(token)
      return token
    })
    .catch(() => {
      // 刷新失败：把 null 发给所有等待者，让它们自己走登出分支。
      // 这里不抛出，否则第一个请求的错误会盖掉排队请求各自的原始错误。
      refreshing = false
      flushWaiters(null)
      return null
    })
}

// logoutNotified 保证并发 401 只提示一次、只跳一次登录页。
// 没有它会连弹五条「登录已过期」，看起来像页面坏了。
let logoutNotified = false

// forceLogout 清登录态并把用户送回登录页。
// 由刷新失败、或请求需要登录而本地根本没有 refresh 时调用。
function forceLogout(message: string): void {
  clearTokens()
  // resetToken 不再发请求，只清内存状态与各个 store 的缓存，可以安全地在拦截器里调。
  useUserStore().resetToken()

  if (logoutNotified) {
    return
  }
  logoutNotified = true
  ElMessage.error(message)
  if (router.currentRoute.value.path !== '/login') {
    void router.replace('/login')
  }
  // 3 秒后复位：用户重新登录后如果再次过期，还能再提示一次。
  // 不用登录成功回调来复位，是因为拦截器不该反过来依赖 store 的生命周期。
  window.setTimeout(() => {
    logoutNotified = false
  }, 3000)
}

service.interceptors.request.use(
  (config: InternalAxiosRequestConfig) => {
    const token = getToken()
    if (token) {
      config.headers.set('Authorization', `Bearer ${token}`)
    }
    return config
  },
  (error) => Promise.reject(error),
)

service.interceptors.response.use(
  (response: AxiosResponse) => {
    const res = response.data
    if (res && res.code === 0) {
      return res.data
    }
    ElMessage.error(res?.message || '请求失败')
    return Promise.reject(new Error(res?.message || 'Error'))
  },
  async (error) => {
    const resp = error.response
    const status = resp?.status
    const message = resp?.data?.message || error.message || '请求失败'
    const config = error.config as RetryableConfig | undefined

    // 只有「需要登录的业务接口」才尝试刷新。
    // 认证接口的 401 直接透传给页面（比如登录页要显示「用户名或密码错误」）。
    if (status === 401 && config && !isAuthEndpoint(config.url) && !config._retried) {
      config._retried = true

      if (!getRefreshToken()) {
        // 本地没有 refresh，说明这个人是游客，不是「登录过期」。
        // 两者的提示与后续动作相同，只是文案要让人知道自己该做什么。
        forceLogout('请先登录')
        return Promise.reject(error)
      }

      const token = await waitForToken()
      if (token) {
        // 重放原请求。不再手动改 Authorization：
        // 请求拦截器会读刚写进 localStorage 的新 access，只有一处来源。
        // 返回的是新的 Promise，页面拿到的是重放结果，感知不到中间换过票。
        return service(config)
      }

      forceLogout('登录已过期，请重新登录')
      return Promise.reject(error)
    }

    if (status === 401) {
      // 走到这里只剩两种情况：
      //   1. 认证接口自己的 401（登录密码错、刷新票失效），要把后端文案透出来，
      //      否则登录页输错密码会一点反馈都没有；
      //   2. 重放后仍然 401，说明刷新没救回来，清登录态并提示。
      if (config && config._retried) {
        forceLogout('登录已过期，请重新登录')
      } else {
        ElMessage.error(message)
      }
    } else if (status === 429) {
      // 限流：后端按客户端 IP 分桶，参数见 config/config.yaml 的 rate_limit，首页会并发多个请求。
      // 不要把 429 自动重试——重试本身也吃令牌，越重试越晚恢复。
      const retryAfter = resp?.headers?.['retry-after']
      ElMessage.error(
        retryAfter ? `请求过于频繁，请 ${retryAfter} 秒后重试` : '请求过于频繁，请稍后重试',
      )
    } else {
      ElMessage.error(message)
    }

    // 把机读错误码与追踪号挂到错误对象上，页面可按 errorCode 分支，
    // 用户报障时能直接把 requestId 给后端查日志。
    Object.assign(error, {
      errorCode: resp?.data?.error_code,
      requestId: resp?.data?.request_id || resp?.headers?.['x-request-id'],
    })
    return Promise.reject(error)
  },
)

// 带类型的请求封装：页面里 request<ArticleResp>({...}) 直接得到业务数据的 Promise。
function request<T = any>(config: AxiosRequestConfig): Promise<T> {
  return service(config) as unknown as Promise<T>
}

export default request
