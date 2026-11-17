import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

// 这一层要测的是「登录态与当前用户」的状态机，不是 HTTP 细节：
// 接口全部替换成可编程的假实现，ws 建连也换掉——真的建连会在 jsdom 里
// 尝试打开一条到 127.0.0.1 的 WebSocket，用例会挂住或报未处理异常。
const loginApi = vi.fn()
const logoutApi = vi.fn()
const registerApi = vi.fn()
const getMe = vi.fn()
const connectWS = vi.fn()
const disconnectWS = vi.fn()

vi.mock('@/api/user', () => ({
  login: (...args: unknown[]) => loginApi(...args),
  logout: (...args: unknown[]) => logoutApi(...args),
  register: (...args: unknown[]) => registerApi(...args),
  getMe: (...args: unknown[]) => getMe(...args),
}))

vi.mock('@/utils/ws', () => ({
  connectWS: (...args: unknown[]) => connectWS(...args),
  disconnectWS: (...args: unknown[]) => disconnectWS(...args),
}))

// store 在模块加载时就会读一次 localStorage 里的 token，
// 所以必须先 mock 完再 import，否则拿到的是空 token 的旧实例。
const { useUserStore } = await import('@/store/modules/user')

// 一份普通用户的资料。管理员那份只把 is_admin 改成 true，其余字段一致，
// 用来证明 isAdmin 只认这一个字段，不是靠 id 或昵称猜出来的。
function meResp(isAdmin: boolean) {
  return {
    id: 7,
    username: 'learner_007',
    nickname: '阿七',
    avatar_url: '',
    bio: '',
    has_custom_nickname: true,
    created_at: '2026-11-01T10:00:00+08:00',
    following_count: 3,
    follower_count: 5,
    is_following: false,
    is_admin: isAdmin,
  }
}

function loginResp() {
  return {
    access_token: 'access-1',
    refresh_token: 'refresh-1',
    expires_in: 900,
    user: {
      id: 7,
      username: 'learner_007',
      nickname: '阿七',
      avatar_url: '',
      bio: '',
      has_custom_nickname: true,
      created_at: '2026-11-01T10:00:00+08:00',
    },
  }
}

beforeEach(() => {
  setActivePinia(createPinia())
  localStorage.clear()
  vi.clearAllMocks()
})

describe('登录后的本地状态', () => {
  it('登录响应一到，顶栏就有名字可显示', async () => {
    loginApi.mockResolvedValue(loginResp())
    const store = useUserStore()

    await store.login({ username: 'learner_007', password: '123456' })

    // 不能让顶栏等到 fetchMe 回来才有内容：登录接口本身就带回了 user，
    // 这时候还显示空昵称会像「登录没成功」。
    expect(store.displayName).toBe('阿七')
    expect(store.userId).toBe(7)
    // 双 token 都要落盘，否则刷新页面后只剩一个 15 分钟的 access。
    expect(localStorage.getItem('forum_token')).toBe('access-1')
    expect(localStorage.getItem('forum_refresh_token')).toBe('refresh-1')
    // 登录即建连：等进私信页才连会漏掉这期间的通知。
    expect(connectWS).toHaveBeenCalledWith('access-1')
  })

  it('登录响应里没有 is_admin，先按非管理员处理', async () => {
    loginApi.mockResolvedValue(loginResp())
    const store = useUserStore()

    await store.login({ username: 'learner_007', password: '123456' })

    // 登录只发身份、不发权限。这一瞬间先当普通用户：
    // 菜单里少一个入口，好过给管理员一个点进去就 403 的入口。
    expect(store.isAdmin).toBe(false)
  })
})

describe('fetchMe', () => {
  it('成功后用真实资料覆盖登录时补的零值', async () => {
    loginApi.mockResolvedValue(loginResp())
    getMe.mockResolvedValue(meResp(true))
    const store = useUserStore()
    await store.login({ username: 'learner_007', password: '123456' })

    const data = await store.fetchMe()

    expect(data).not.toBeNull()
    // 登录时这两个计数被填成 0，fetchMe 之后必须是接口给的 3 / 5，
    // 否则个人主页上关注数会一直显示 0。
    expect(store.profile?.following_count).toBe(3)
    expect(store.profile?.follower_count).toBe(5)
    expect(store.isAdmin).toBe(true)
    expect(store.profileLoaded).toBe(true)
  })

  it('失败时 profile 仍为空，但已标记加载过', async () => {
    localStorage.setItem('forum_token', 'access-1')
    getMe.mockRejectedValue(new Error('500'))
    const store = useUserStore()

    const data = await store.fetchMe()

    expect(data).toBeNull()
    expect(store.profile).toBeNull()
    // 关键点：失败也要置位。只看 profile 是否为 null 的话，
    // 页面会永远停在骨架屏上，用户既看不到提示也没法重试。
    expect(store.profileLoaded).toBe(true)
  })

  it('没有 token 时不发请求，也不标记已加载', async () => {
    const store = useUserStore()

    const data = await store.fetchMe()

    expect(data).toBeNull()
    expect(getMe).not.toHaveBeenCalled()
    // 未登录时不能置位：否则登录页/管理页会以为「已经拉过了、只是没资料」。
    expect(store.profileLoaded).toBe(false)
  })
})

describe('isAdmin', () => {
  it('只认 is_admin 字段本身', async () => {
    localStorage.setItem('forum_token', 'access-1')
    getMe.mockResolvedValue({ ...meResp(false), id: 1, username: 'admin' })
    const store = useUserStore()

    await store.fetchMe()

    // id 是 1、用户名还叫 admin，但字段是 false，就必须判成非管理员。
    // 前端一旦开始按 id 猜权限，后端改白名单时两边就会不一致。
    expect(store.isAdmin).toBe(false)
  })
})

describe('退出登录', () => {
  it('先让服务端撤销 refresh，再清本地', async () => {
    loginApi.mockResolvedValue(loginResp())
    // 记下「发起撤销那一刻本地还剩什么」。
    //
    // 不能在假实现里直接 expect：logout 自己包了 try/catch 吞异常，
    // 断言抛出的错误会被当成「撤销失败」悄悄吃掉，用例照样绿——
    // 这个陷阱正好会掩盖掉本用例要防的那次顺序颠倒。
    let refreshOnRevoke = ''
    logoutApi.mockImplementation(() => {
      refreshOnRevoke = localStorage.getItem('forum_refresh_token') || ''
      return Promise.resolve({ message: 'ok' })
    })
    const store = useUserStore()
    await store.login({ username: 'learner_007', password: '123456' })

    await store.logout()

    expect(logoutApi).toHaveBeenCalledWith('refresh-1')
    // 撤销请求发出时 refresh 必须还在本地，否则服务端那张票会一直有效到过期。
    expect(refreshOnRevoke).toBe('refresh-1')
    expect(localStorage.getItem('forum_token')).toBeNull()
    expect(localStorage.getItem('forum_refresh_token')).toBeNull()
    expect(store.profile).toBeNull()
    expect(store.token).toBe('')
    // 连接必须断开：留着的话服务端会继续往这条连接推上一账号的私信。
    expect(disconnectWS).toHaveBeenCalled()
  })

  it('撤销请求失败也照样清本地', async () => {
    loginApi.mockResolvedValue(loginResp())
    logoutApi.mockRejectedValue(new Error('network down'))
    const store = useUserStore()
    await store.login({ username: 'learner_007', password: '123456' })

    await store.logout()

    // 用户的意图是退出，不该被网络问题拦住；
    // 服务端那条链会靠过期自然失效。
    expect(store.token).toBe('')
    expect(localStorage.getItem('forum_token')).toBeNull()
    expect(store.profile).toBeNull()
  })

  it('换人登录后要重新走一遍 fetchMe', async () => {
    loginApi.mockResolvedValue(loginResp())
    getMe.mockResolvedValue(meResp(false))
    const store = useUserStore()
    await store.login({ username: 'learner_007', password: '123456' })
    await store.fetchMe()
    expect(store.profileLoaded).toBe(true)

    store.resetToken()

    // 复位「已加载」标记，否则下一个账号进来时页面会认为资料已经拉过，
    // 一直显示上一个人的内容（或空白）而不再请求。
    expect(store.profileLoaded).toBe(false)
  })
})
