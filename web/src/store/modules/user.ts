import { defineStore } from 'pinia'
import { ref } from 'vue'
import {
  login as loginApi,
  register as registerApi,
  type LoginResp,
} from '@/api/user'
import { getToken, setToken, removeToken } from '@/utils/auth'

// 对应 vue3-element-admin 的 src/store/modules/user.ts。
// 本后端没有「当前用户资料」接口，登录成功返回的 user 对象就是我们能拿到全部信息，
// 所以 store 只存 token + 用户名，足够页面展示和鉴权用。
export const useUserStore = defineStore('user', () => {
  const token = ref(getToken())
  const username = ref('')

  function applyLogin(resp: LoginResp) {
    token.value = resp.token
    username.value = resp.user.username
    setToken(resp.token)
  }

  async function login(data: { username: string; password: string }) {
    const resp = await loginApi(data)
    applyLogin(resp)
    return resp
  }

  // 注册接口不自动登录，所以注册完自己再调一次登录拿到 token。
  async function register(data: { username: string; password: string }) {
    await registerApi(data)
    return login(data)
  }

  function resetToken() {
    removeToken()
    token.value = ''
    username.value = ''
  }

  return { token, username, login, register, resetToken }
})
