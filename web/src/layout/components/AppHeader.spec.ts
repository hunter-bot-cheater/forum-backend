import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import ElementPlus from 'element-plus'

// 顶栏只关心「游客看到什么」和「登录用户看到什么」。
// 三个 store 用最小替身，不必为了渲染一行顶栏去连真实接口或建 pinia。
const userState = {
  token: '',
  displayName: '',
  username: '',
  profile: null as null | { avatar_url: string },
  userId: 0,
  isAdmin: false,
  logout: vi.fn(),
}

vi.mock('@/store/modules/user', () => ({
  useUserStore: () => userState,
}))

vi.mock('@/store/modules/im', () => ({
  useImStore: () => ({ totalUnread: 0 }),
}))

vi.mock('@/store/modules/notification', () => ({
  useNotificationStore: () => ({ summary: { total: 0 } }),
}))

const route = { path: '/hot', fullPath: '/hot' }
const push = vi.fn()

// vue-router 只替换两个组合函数，其余（RouterLink 等）保留真实定义，
// 挂载时再用 RouterLinkStub 替掉，避免为了 stub 而破坏 router 的类型。
vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return { ...actual, useRoute: () => route, useRouter: () => ({ push }) }
})

const AppHeader = (await import('./AppHeader.vue')).default

// 四个图标在构建时由 unplugin-vue-components 自动引入，测试环境没有这层插件。
// 不 stub 的话每个用例都会刷一屏「Failed to resolve component」，真正的失败会被埋掉。
const iconStub = { template: '<i />' }

function mountHeader() {
  return mount(AppHeader, {
    global: {
      plugins: [ElementPlus],
      stubs: {
        RouterLink: RouterLinkStub,
        EditPen: iconStub,
        ChatDotRound: iconStub,
        Bell: iconStub,
        ArrowDown: iconStub,
      },
    },
  })
}

beforeEach(() => {
  userState.token = ''
  userState.displayName = ''
  userState.username = ''
  userState.profile = null
  userState.userId = 0
  userState.isAdmin = false
  push.mockReset()
})

describe('顶栏用户区', () => {
  it('游客不渲染下拉菜单，只有一个登录按钮', () => {
    const wrapper = mountHeader()

    expect(wrapper.find('.hdr__login').exists()).toBe(true)
    // 菜单里三项对游客都是无效入口，整块不该出现。
    expect(wrapper.find('.hdr__user').exists()).toBe(false)
  })

  it('游客点登录时带上当前地址', async () => {
    const wrapper = mountHeader()

    await wrapper.find('.hdr__login').trigger('click')

    expect(push).toHaveBeenCalledWith({ path: '/login', query: { redirect: '/hot' } })
  })

  it('登录后显示用户下拉，不再有登录按钮', () => {
    userState.token = 'token-abc'
    userState.displayName = '阿七'
    userState.userId = 7

    const wrapper = mountHeader()

    expect(wrapper.find('.hdr__user').exists()).toBe(true)
    expect(wrapper.find('.hdr__login').exists()).toBe(false)
    expect(wrapper.text()).toContain('阿七')
  })

  it('已登录但资料还没到时不留「未登录」字样', () => {
    // 回归用例。这一瞬间是「已登录、资料在路上」，
    // 一旦 shownName 的兜底改回「未登录」，用户会看到掉线又自动重登的闪动。
    userState.token = 'token-abc'

    const wrapper = mountHeader()

    expect(wrapper.text()).not.toContain('未登录')
  })
})
