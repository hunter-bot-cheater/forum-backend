import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ElementPlus from 'element-plus'
import type { ReportItem } from '@/api/report'

// 管理后台的用例重点不在排版，而在几条判断错了不会报错的规则：
// 谁看得到面板、什么时候才发请求、终态举报还能不能操作、缺失字段怎么兜底。
// 这些错一条，要么普通用户看见管理界面，要么管理员对着空白页猜原因。

// vi.mock 的工厂函数会被提升到文件最顶部执行，那时普通的 const 还没初始化，
// 直接引用会报 "Cannot access before initialization"。
// vi.hoisted 里的代码同样被提升，但作用域是同一个，于是工厂能安全地拿到这些假函数。
const {
  listReports,
  handleReport,
  adminListTags,
  renameTag,
  replace,
  messageSuccess,
  messageError,
  messageWarning,
  confirmBox,
} = vi.hoisted(() => ({
  listReports: vi.fn(),
  handleReport: vi.fn(),
  adminListTags: vi.fn(),
  renameTag: vi.fn(),
  replace: vi.fn(),
  messageSuccess: vi.fn(),
  messageError: vi.fn(),
  messageWarning: vi.fn(),
  confirmBox: vi.fn(),
}))

// 部分替换而不是整体替换：reasonLabel 是真实实现，
// 「理由显示成中文」正是要被验证的行为，不能也换成假的。
vi.mock('@/api/report', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/report')>()
  return {
    ...actual,
    listReports: (...args: unknown[]) => listReports(...args),
    handleReport: (...args: unknown[]) => handleReport(...args),
  }
})

vi.mock('@/api/article', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/article')>()
  return {
    ...actual,
    adminListTags: (...args: unknown[]) => adminListTags(...args),
    renameTag: (...args: unknown[]) => renameTag(...args),
  }
})

// 页面里只用到 router.replace（「回到文章列表」按钮），
// 挂真路由还得配一份路由表，收益为零。
// 但这里只能替换 useRouter，不能整个模块换成假的：
// @/utils/request 在 401 时要 import 真实 router 实例去跳登录页，
// 那份模块要 createRouter / createWebHistory，整体替换会在加载阶段就炸。
vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    useRouter: () => ({ replace, push: vi.fn() }),
  }
})

// ElMessage / ElMessageBox 换成假的：真的会往 document.body 里插节点，
// 而这里要断言的是「有没有弹提示、提示说了什么」。
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: {
      success: messageSuccess,
      error: messageError,
      warning: messageWarning,
    },
    ElMessageBox: { confirm: confirmBox, prompt: vi.fn() },
  }
})

const { default: AdminPage } = await import('@/views/admin/index.vue')
const { useUserStore } = await import('@/store/modules/user')

// 一份举报。默认是「文章 + 待处理」，各用例只改自己关心的字段，
// 免得每个用例都抄一遍十几个字段。
function report(overrides: Partial<ReportItem> = {}): ReportItem {
  return {
    id: 1,
    target_type: 'article',
    target_id: 24,
    article_id: 24,
    target_title: '如何理解 Redis 的 singleflight',
    excerpt: '',
    target_exists: true,
    reporter: { id: 2, username: 'learner_002', nickname: '小二', avatar_url: '' },
    target_author: { id: 3, username: 'learner_003', nickname: '老三', avatar_url: '' },
    reason: 'spam',
    detail: '整篇都是复制粘贴的广告，还带了外链。',
    status: 'pending',
    created_at: '2026-11-17T10:00:00+08:00',
    handled_at: null,
    ...overrides,
  }
}

function profile(isAdmin: boolean) {
  return {
    id: 1,
    username: 'learner_001',
    nickname: '一号',
    avatar_url: '',
    bio: '',
    has_custom_nickname: true,
    created_at: '2026-11-01T10:00:00+08:00',
    following_count: 0,
    follower_count: 0,
    is_following: false,
    is_admin: isAdmin,
  }
}

function mountPage() {
  return mount(AdminPage, {
    global: {
      plugins: [ElementPlus],
      // 悬空路由链接在 jsdom 里没有可跳转的上下文，替换成一个记录 to 的替身。
      stubs: { RouterLink: RouterLinkStub },
    },
  })
}

// prepare 把 store 摆到「资料已拉到」的稳定状态再挂载。
// 不这么做的话，页面挂载那一刻 isAdmin 还是 false，会先渲染「没有权限」。
function prepare(isAdmin: boolean) {
  const user = useUserStore()
  user.profile = profile(isAdmin)
  user.profileLoaded = true
  return user
}

beforeEach(() => {
  setActivePinia(createPinia())
  localStorage.clear()
  vi.clearAllMocks()
  listReports.mockResolvedValue({ list: [report()], total: 1, page: 1, page_size: 10 })
  adminListTags.mockResolvedValue({
    list: [{ id: 5, name: 'golang', count: 12 }],
    total: 1,
    page: 1,
    page_size: 10,
  })
})

describe('权限与加载态', () => {
  it('资料还没拉到时只有骨架屏，不发任何管理请求', async () => {
    // profileLoaded 为 false 是一瞬间的状态：这时 isAdmin 恒为 false，
    // 提前拉列表只会换来一次 403 和一条错误提示。
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.find('.adm__skeleton').exists()).toBe(true)
    expect(listReports).not.toHaveBeenCalled()
    expect(adminListTags).not.toHaveBeenCalled()
  })

  it('拉过一次但没有资料时给重试入口，而不是说没权限', async () => {
    const user = useUserStore()
    user.profileLoaded = true
    user.profile = null

    const wrapper = mountPage()
    await flushPromises()

    // 网络断了也落到这个分支。如果这里显示「只有管理员能打开」，
    // 管理员会以为自己的白名单被人改了。
    expect(wrapper.text()).toContain('没能加载你的账号资料')
    expect(wrapper.text()).not.toContain('只有管理员能打开')
    expect(listReports).not.toHaveBeenCalled()
  })

  it('普通用户看到「只有管理员能打开」，且不请求管理接口', async () => {
    prepare(false)

    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.text()).toContain('这个页面只有管理员能打开')
    expect(wrapper.find('.adm__panels').exists()).toBe(false)
    // 前端这层只是显示开关，但连请求都不该发：明知会 403 的请求
    // 会在控制台和提示条上各留一条噪音。
    expect(listReports).not.toHaveBeenCalled()
    expect(adminListTags).not.toHaveBeenCalled()
  })

  it('管理员进入后同时拉举报与标签', async () => {
    prepare(true)

    mountPage()
    await flushPromises()

    expect(listReports).toHaveBeenCalledWith('pending', 1, 10)
    expect(adminListTags).toHaveBeenCalledWith('', 1, 10)
  })
})

describe('举报列表', () => {
  it('一条举报把判断所需的字段一次摆全', async () => {
    prepare(true)

    const wrapper = mountPage()
    await flushPromises()

    const text = wrapper.find('.rp').text()
    // 类型、理由都要是人话：一屏几十条，没人愿意在脑子里翻译 spam。
    expect(wrapper.find('.rp__kind').text()).toBe('文章')
    expect(wrapper.find('.rp__reason').text()).toBe('广告或刷屏')
    expect(text).toContain('如何理解 Redis 的 singleflight')
    expect(text).toContain('整篇都是复制粘贴的广告')
    expect(text).toContain('小二')
    expect(text).toContain('老三')
  })

  it('举报人已注销时显示「已注销」，不抛出', async () => {
    prepare(true)
    // 账号注销后后端把整个 reporter 字段省掉。
    listReports.mockResolvedValue({
      list: [report({ reporter: undefined })],
      total: 1,
      page: 1,
      page_size: 10,
    })

    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.find('.rp__meta').text()).toContain('已注销')
  })

  it('内容已被删除时标明，并禁掉「下架内容」', async () => {
    prepare(true)
    listReports.mockResolvedValue({
      list: [report({ target_exists: false })],
      total: 1,
      page: 1,
      page_size: 10,
    })

    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.text()).toContain('内容已被删除')
    const buttons = wrapper.findAll('.rp__ops button')
    // 顺序是 驳回 / 下架内容 / 下架并封禁；第二条置灰，
    // 因为内容已经没了，「下架」这个动作没有对象。
    expect(buttons[1].attributes('disabled')).toBeDefined()
  })

  it('已处置的举报只标状态，不给操作按钮', async () => {
    prepare(true)
    listReports.mockResolvedValue({
      list: [report({ status: 'dismissed', handled_at: '2026-11-17T11:00:00+08:00' })],
      total: 1,
      page: 1,
      page_size: 10,
    })

    const wrapper = mountPage()
    await flushPromises()

    // 处置是终态：改判只能重新举报，所以这里不该再出现按钮。
    expect(wrapper.find('.rp__status').text()).toBe('已驳回')
    expect(wrapper.find('.rp__ops').exists()).toBe(false)
  })

  it('切换状态会回第一页并按新状态重拉', async () => {
    prepare(true)
    // 造够三页数据，才能先翻到第 2 页再验证「换筛选回第一页」。
    listReports.mockResolvedValue({ list: [report()], total: 25, page: 1, page_size: 10 })
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.findAll('.el-pager li')[1].trigger('click')
    await flushPromises()
    expect(listReports).toHaveBeenLastCalledWith('pending', 2, 10)

    await wrapper.findAll('.adm__seg-btn')[1].trigger('click')
    await flushPromises()

    // 不回第一页的话，原状态第 2 页在有 25 条时正常、切到只有几条的状态就是空页，
    // 管理员会以为这个状态下一条都没有。
    expect(listReports).toHaveBeenLastCalledWith('dismissed', 1, 10)
  })

  it('驳回直接执行并弹提示', async () => {
    prepare(true)
    handleReport.mockResolvedValue(null)

    const wrapper = mountPage()
    await flushPromises()

    const buttons = wrapper.findAll('.rp__ops button')
    await buttons[0].trigger('click')
    await flushPromises()

    // 驳回不处罚任何人，误点只是这条举报回到待处理，所以不弹二次确认。
    expect(confirmBox).not.toHaveBeenCalled()
    expect(handleReport).toHaveBeenCalledWith(1, 'dismiss', 0)
    expect(messageSuccess).toHaveBeenCalled()
  })
})

describe('标签面板', () => {
  it('切到标签管理显示名字与文章数', async () => {
    prepare(true)
    const wrapper = mountPage()
    await flushPromises()

    const panels = wrapper.findAll('.adm__panel-btn')
    await panels[1].trigger('click')
    await flushPromises()

    expect(wrapper.find('.tg').text()).toContain('golang')
    // count 为 0 的标签也要列出来：它们正是最该清理的一批。
    expect(wrapper.findAll('.tg__count')[1].text()).toBe('12')
  })

  it('行内改名只在当前行出现输入框', async () => {
    prepare(true)
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.findAll('.adm__panel-btn')[1].trigger('click')
    await flushPromises()

    expect(wrapper.find('.tg__input').exists()).toBe(false)
    await wrapper.find('.tg__ops button').trigger('click')
    await flushPromises()

    // 同一时刻只允许一行在编辑态，否则容易把「保存」点成另一行的。
    expect(wrapper.findAll('.tg__input')).toHaveLength(1)
  })

  it('名字没改就点保存：不发请求，直接退出编辑态', async () => {
    prepare(true)
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.findAll('.adm__panel-btn')[1].trigger('click')
    await flushPromises()

    await wrapper.find('.tg__ops button').trigger('click')
    await flushPromises()
    // 打开编辑态但不改内容，直接点保存。
    await wrapper.findAll('.tg__ops button')[0].trigger('click')
    await flushPromises()

    // 后端同名分支虽然幂等，但白跑一次请求毫无意义。
    expect(renameTag).not.toHaveBeenCalled()
    expect(wrapper.find('.tg__input').exists()).toBe(false)
  })

  it('名字清空时拦在前端，不发请求', async () => {
    prepare(true)
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.findAll('.adm__panel-btn')[1].trigger('click')
    await flushPromises()

    await wrapper.find('.tg__ops button').trigger('click')
    await flushPromises()
    await wrapper.find('.tg__input input').setValue('   ')
    await wrapper.findAll('.tg__ops button')[0].trigger('click')
    await flushPromises()

    // 空名字在后端会被校验拦掉，但让请求白跑一趟再弹 400 的提示
    // 比在本地直接说「不能为空」差得多。
    expect(renameTag).not.toHaveBeenCalled()
    expect(messageWarning).toHaveBeenCalled()
    // 留在编辑态，管理员可以直接补内容，不用重新点「改名」。
    expect(wrapper.find('.tg__input').exists()).toBe(true)
  })

  it('改名遇到重名时提示合并，并把影响条数说出来', async () => {
    prepare(true)
    renameTag.mockResolvedValue({ tag_id: 5, name: 'Go', merged: true, moved_articles: 12 })

    const wrapper = mountPage()
    await flushPromises()
    await wrapper.findAll('.adm__panel-btn')[1].trigger('click')
    await flushPromises()

    await wrapper.find('.tg__ops button').trigger('click')
    await flushPromises()
    await wrapper.find('.tg__input input').setValue('Go')
    // 输入框之后按钮变成 保存 / 取消，第一个是保存。
    await wrapper.findAll('.tg__ops button')[0].trigger('click')
    await flushPromises()

    expect(renameTag).toHaveBeenCalledWith(5, 'Go')
    const tip = String(messageSuccess.mock.calls[0][0])
    // 「合并成功」和「改名成功」必须能区分：管理员要知道有 12 篇文章被带过来了。
    expect(tip).toContain('合并')
    expect(tip).toContain('12')
  })
})
