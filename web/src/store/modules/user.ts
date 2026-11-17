import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  login as loginApi,
  logout as logoutApi,
  register as registerApi,
  getMe,
  type LoginResp,
  type MeResp,
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
  //
  // 类型用 MeResp 而不是 UserProfile：多出来的 is_admin 是顶栏菜单的开关依据。
  const profile = ref<MeResp | null>(null)
  // profileLoaded 表示 fetchMe 已经跑过一次（不论成败）。
  //
  // 页面靠它区分「资料还在路上」和「资料拉失败了」：只看 profile 是不是 null
  // 的话，请求失败时页面会永远停在骨架屏上，用户既没提示也没得重试。
  const profileLoaded = ref(false)

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
  // isAdmin 决定顶栏菜单里出不出现「管理后台」。
  // 只做展示开关，真正的权限判定在后端（/api/admin 那一组挂了白名单），
  // 前端把它改成 true 也调不动任何管理接口。
  const isAdmin = computed(() => {
    if (profile.value) {
      return profile.value.is_admin === true
    }
    return false
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
      // 登录响应里没有这个字段（登录只发身份），先按「不是管理员」填，
      // 随后 fetchMe 会用真实值覆盖。这一瞬间菜单里少一个入口比多一个 403 好。
      is_admin: false,
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
    } finally {
      profileLoaded.value = true
    }
  }

  // applyProfile 用一份完整资料替换本地状态。
  // 只给「资料刚改完」的场景用：顶栏的昵称头像要立刻变，
  // 等下一次 fetchMe 才刷新的话，用户会以为没保存成功。
  function applyProfile(data: MeResp) {
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
    // 换人登录后要重新走一遍 fetchMe，否则下一个账号会继承「已加载完」这个状态。
    profileLoaded.value = false
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

  return {
    token,
    profile,
    profileLoaded,
    username,
    displayName,
    userId,
    isAdmin,
    login,
    register,
    fetchMe,
    applyProfile,
    resetToken,
    logout,
  }
})
