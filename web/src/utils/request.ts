import axios, {
  type AxiosInstance,
  type AxiosRequestConfig,
  type AxiosResponse,
  type InternalAxiosRequestConfig,
} from 'axios'
import { ElMessage } from 'element-plus'
import { getToken, removeToken } from '@/utils/auth'
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
const service: AxiosInstance = axios.create({
  baseURL: import.meta.env.VITE_API_BASE_URL || '/api',
  timeout: 10000,
})

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
  (error) => {
    const resp = error.response
    const status = resp?.status
    const message = resp?.data?.message || error.message || '请求失败'
    if (status === 401) {
      // 登录过期或没带 token：清掉本地状态，跳回登录页。
      // resetToken 会连带跑一遍各 store 注册的清理（私信会话列表、通知红点），
      // 所以这里不逐个 store 去清——漏一个就会出现「下一个账号看到上一个账号的数据」。
      removeToken()
      useUserStore().resetToken()
      ElMessage.error('登录已过期，请重新登录')
      // 首页会并发发好几个请求，同时 401 时只跳一次。
      // 已经在登录页还去 replace，会让路由收到一串重复导航。
      if (router.currentRoute.value.path !== '/login') {
        router.replace('/login')
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
