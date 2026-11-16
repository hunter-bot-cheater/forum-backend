import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  login as loginApi,
  logout as logoutApi,
  register as registerApi,
  getMe,
  type LoginResp,
  type UserProfile,
} from '@/api/user'
import {
  getToken,
  setToken,
  getRefreshToken,
  setRefreshToken,
  clearTokens,
  runLogoutHooks,
} from '@/utils/auth'
import { connectWS, disconnectWS } from '@/utils/ws'

// 登录态与「当前用户是谁」。
//
// 早期版本只存 token + 用户名，够登录和顶栏展示用。
// 聊天、个人中心、点赞这些功能都需要用户 id 与昵称头像，
// 所以现在多了一份 profile，来源是 GET /users/me。
export const useUserStore = defineStore('user', () => {
  const token = ref(getToken())
  // profile 在刷新后是空的，layout 挂载时会调 fetchMe 补上。
  // 页面不能假设它一定有值：首屏那一瞬间就是 null，
  // 需要展示名字的地方用 displayName 兜底。
  const profile = ref<UserProfile | null>(null)

  const username = computed(() => {
    if (profile.value) {
      return profile.value.username
    }
    return ''
  })
  const displayName = computed(() => {
    if (profile.value) {
      return profile.value.nickname
    }
    return ''
  })
  const userId = computed(() => {
    if (profile.value) {
      return profile.value.id
    }
    return 0
  })

  function applyLogin(resp: LoginResp) {
    token.value = resp.access_token
    setToken(resp.access_token)
    // refresh 也要落盘：不存的话刷新页面后只剩一个 15 分钟的 access，
    // 过期就直接被踢回登录页，双 token 的意义没了。
    setRefreshToken(resp.refresh_token)
    // 登录响应是 userResp，比 profileResp 少了两个关注计数，
    // 但 id/昵称/头像都有，先填进去让顶栏立刻有内容，
    // 计数字段补 0，随后 fetchMe 会整体覆盖成真实值。
    profile.value = {
      ...resp.user,
      following_count: 0,
      follower_count: 0,
      is_following: false,
    }
    // 登录成功后立刻建连：用户接下来多半要聊天或看通知，
    // 等到进入私信页才连会错过这段时间的推送。
    connectWS(resp.access_token)
  }

  // fetchMe 拉当前用户资料。失败不抛给调用方：
  // 顶栏少个昵称不影响使用，401 由请求拦截器统一处理跳登录。
  async function fetchMe() {
    if (!token.value) {
      return null
    }
    try {
      const data = await getMe()
      profile.value = data
      return data
    } catch {
      return null
    }
  }

  // applyProfile 用一份完整资料替换本地状态。
  // 只给「资料刚改完」的场景用：顶栏的昵称头像要立刻变，
  // 等下一次 fetchMe 才刷新的话，用户会以为没保存成功。
  function applyProfile(data: UserProfile) {
    profile.value = data
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

  // resetToken 只做本地清理，不发任何请求。
  //
  // 拦截器在刷新失败时会调它：那一刻 access 已经不可用，再发请求只会再 401 一次。
  // 需要「真正退出」的场景用下面的 logout。
  function resetToken() {
    clearTokens()
    token.value = ''
    profile.value = null
    // 登出必须断开 WebSocket：留着的话服务端会继续往这条连接推进来消息，
    // 下一个登录的人可能看到上一个人的私信。
    disconnectWS()
    // 私信、通知这些带账号数据的 store 由它们自己注册到这里，
    // 登出与 401 都走这一条路径，不会出现「退出登录清了、token 过期没清」。
    runLogoutHooks()
  }

  // logout 是用户点「退出登录」走的路：先让服务端撤销 refresh，再清本地。
  //
  // 顺序不能反。先清本地就拿不到 refresh 了，服务端那条链会一直有效到过期，
  // 相当于「退出了但票还在别人兜里」。
  // 请求失败也照样清本地：用户的意图是退出，不该被网络问题拦住。
  async function logout() {
    const refreshToken = getRefreshToken()
    if (refreshToken) {
      try {
        await logoutApi(refreshToken)
      } catch {
        // 拦截器可能已经弹过提示，这里不再重复；本地清理在下面照常执行。
      }
    }
    resetToken()
  }

  return { token, profile, username, displayName, userId, login, register, fetchMe, applyProfile, resetToken, logout }
})
