import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { LiveMatch, OnlineUser, PoolRoom } from '@/api/pool'
import type { MeResp } from '@/api/user'
import { usePoolStore } from '@/store/modules/pool'
import { useUserStore } from '@/store/modules/user'

// 大厅现在同时是「我的房间」，所以这个文件覆盖两条线：
//   1. 房间：进出、准备、状态翻成 playing 就跳对局页；
//   2. 观战：资料没回来之前不轮询、认出自己的局、失败后能重试。
//
// api 与 ws 全部换成假的：真实现要发 HTTP、连 WebSocket，jsdom 里都不成立。

const mocks = vi.hoisted(() => ({
  live: vi.fn(),
  myRoom: vi.fn(),
  online: vi.fn(),
  join: vi.fn(),
  leave: vi.fn(),
  reopen: vi.fn(),
  ready: vi.fn(),
  invite: vi.fn(),
  // 人机对战：空房卡片下面那行「先和机器人练一杆」走它。
  bot: vi.fn(),
  replace: vi.fn(),
}))

// routeState 是 useRoute() 返回的东西。
//
// 单独放一份而不是塞进 mocks：beforeEach 会对 mocks 里的每一项调 mockReset，
// 把普通对象放进去会直接炸。分享链接那几条用例改 params 来模拟 /pool/join/:code。
const routeState = vi.hoisted(() => ({ params: {} as Record<string, string> }))

vi.mock('@/api/pool', () => ({
  getLiveMatches: (...args: unknown[]) => mocks.live(...args),
  getMyRoom: (...args: unknown[]) => mocks.myRoom(...args),
  getOnlineUsers: (...args: unknown[]) => mocks.online(...args),
  joinRoom: (...args: unknown[]) => mocks.join(...args),
  leaveRoom: (...args: unknown[]) => mocks.leave(...args),
  reopenRoom: (...args: unknown[]) => mocks.reopen(...args),
  setReady: (...args: unknown[]) => mocks.ready(...args),
  startBotMatch: (...args: unknown[]) => mocks.bot(...args),
  listPendingInvites: vi.fn(() => Promise.resolve([])),
  createInvite: (...args: unknown[]) => mocks.invite(...args),
  acceptInvite: vi.fn(),
  declineInvite: vi.fn(),
}))

vi.mock('@/api/user', () => ({
  listFollows: vi.fn(() => Promise.resolve({ list: [], total: 0 })),
}))

// pool store 在 initWS 里注册推送回调，真实现会去连 WebSocket。
vi.mock('@/utils/ws', () => ({
  onFrame: () => {},
  onReconnect: () => {},
}))

// ElMessage 只是往页面上飘一句提示，断言它没有意义；
// 换成假函数还能验「进房成功有没有给一句反馈」。
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
    // 剪贴板拿不到时挂的那个「手动复制」弹窗，同样只验调用。
    ElMessageBox: { alert: vi.fn(() => Promise.resolve()) },
  }
})

vi.mock('vue-router', async (importOriginal) => {
  // 只替换 useRouter / useRoute：整体替换会让 vue-router 的其它导出消失。
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    useRouter: () => ({ replace: mocks.replace, push: mocks.replace }),
    useRoute: () => ({ params: routeState.params }),
  }
})

const LobbyPage = (await import('@/views/pool/lobby.vue')).default

const stubs = {
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  'el-avatar': { template: '<span class="el-avatar"><slot /></span>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  'el-empty': { template: '<div class="el-empty" />' },
  'el-icon': { template: '<span class="el-icon"><slot /></span>' },
}

// room 造一份房间快照。默认「我坐 0 号，对面还空着」。
function room(patch: Partial<PoolRoom> = {}): PoolRoom {
  const base: PoolRoom = {
    match_id: 7,
    room_code: '123456',
    status: 'waiting',
    seat: 0,
    ready: [false, false],
    players: [
      { user_id: 9, nickname: '我', avatar: '' },
      { user_id: 0, nickname: '', avatar: '' },
    ],
  }
  return { ...base, ...patch }
}

// occupied 造一份「对面已经坐了人」的房间。
function occupied(patch: Partial<PoolRoom> = {}): PoolRoom {
  return room({
    players: [
      { user_id: 9, nickname: '我', avatar: '' },
      { user_id: 10, nickname: '小满', avatar: '' },
    ],
    ...patch,
  })
}

// liveMatch 造一条「正在进行的对局」，双方按座位排。
function liveMatch(patch: Partial<LiveMatch> = {}): LiveMatch {
  const base: LiveMatch = {
    match_id: 21,
    status: 'playing',
    turn: 0,
    shot_seq: 4,
    spectators: 0,
    spectators_max: 10,
    players: [
      { user_id: 7, nickname: '阿泽', avatar: '' },
      { user_id: 8, nickname: '小满', avatar: '' },
    ],
  }
  return { ...base, ...patch }
}

function onlineUser(patch: Partial<OnlineUser> = {}): OnlineUser {
  const base: OnlineUser = {
    user_id: 11,
    nickname: '陆陆',
    avatar: '',
    following: false,
  }
  return { ...base, ...patch }
}

// mountPage 装一次大厅。资料默认已经加载完，需要验「没加载完」的用例自己改。
async function mountPage(loaded = true) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const user = useUserStore()
  user.profileLoaded = loaded

  const wrapper = mount(LobbyPage, { global: { plugins: [pinia], stubs } })
  await flushPromises()
  return { wrapper, user }
}

// toolButton 按文案找页头的次要按钮。
//
// 不能写 findAll('.pl__tool')[0]：那一排会随着入口增减而变（曾经有过
// 第三颗「人机对战」，撤掉之后「输入房号」的位置就变了），
// 用例会挂在一个与它要测的东西无关的位置上。
function toolButton(wrapper: ReturnType<typeof mount>, text: string) {
  const found = wrapper.findAll('.pl__tool').find((item) => item.text().includes(text))
  if (!found) {
    throw new Error('页头没有「' + text + '」按钮')
  }
  return found
}

// me 造一份自己的资料。userId 是 profile 的派生值（computed），
// 所以「我是谁」只能通过 profile 设置，不能直接改 userId。
function me(id: number): MeResp {
  return {
    id,
    username: 'me' + String(id),
    nickname: '我',
    avatar_url: '',
    bio: '',
    has_custom_nickname: false,
    created_at: '2026-01-01T00:00:00Z',
    following_count: 0,
    follower_count: 0,
    is_following: false,
    is_admin: false,
  }
}

beforeEach(() => {
  for (const fn of Object.values(mocks)) {
    fn.mockReset()
  }
  routeState.params = {}
  mocks.live.mockResolvedValue([])
  mocks.myRoom.mockResolvedValue(room())
  mocks.online.mockResolvedValue([])
})

afterEach(() => {
  // 假时钟必须在每条用例后还原，否则后面的事件与 flushPromises 都会卡住。
  vi.useRealTimers()
})

describe('大厅的房间', () => {
  it('进大厅就开一间房：房号摆在上面，自己那侧写着「你」，对面是空位', async () => {
    const { wrapper } = await mountPage()

    expect(mocks.myRoom).toHaveBeenCalled()
    expect(wrapper.find('.pl__code-value').text()).toBe('123456')

    const seats = wrapper.findAll('.pl__seat')
    expect(seats).toHaveLength(2)
    expect(seats[0].classes()).toContain('is-me')
    expect(seats[0].text()).toContain('我')
    // 对面空着：虚位卡把「还能坐一个人」说出来，而不是「对手掉线了」。
    expect(seats[1].classes()).toContain('is-empty')
    expect(seats[1].text()).toContain('空位')
  })

  it('对面没人时准备按钮是禁用的，文案说明在等谁', async () => {
    const { wrapper } = await mountPage()

    const button = wrapper.find('.pl__ready')
    expect(button.text()).toBe('等对手进来')
    expect(button.attributes('disabled')).toBeDefined()
  })

  it('对方进来之后画出他的资料，双方准备后两边都标成已准备', async () => {
    mocks.myRoom.mockResolvedValue(occupied({ ready: [true, true] }))
    const { wrapper } = await mountPage()

    const seats = wrapper.findAll('.pl__seat')
    expect(seats[1].classes()).not.toContain('is-empty')
    expect(seats[1].text()).toContain('小满')
    expect(seats[0].classes()).toContain('is-ready')
    expect(seats[1].classes()).toContain('is-ready')
  })

  it('推送正好撞上一次在途轮询时，收尾会补拉一次，不会停在「只有自己」', async () => {
    // 第一次请求挂住：模拟「对面进房的那一瞬间，我这边正好有一发在路上」。
    // 以前这一发收尾时会把推送触发的那次重拉直接丢掉，界面就停在旧快照上，
    // 要等下一轮十秒的慢轮询才变——「对方明明进来了，我这一屏还是只有我自己」。
    let release = (value: PoolRoom) => {
      void value
    }
    mocks.myRoom.mockImplementationOnce(
      () =>
        new Promise<PoolRoom>((resolve) => {
          release = resolve
        }),
    )
    const { wrapper } = await mountPage()
    expect(mocks.myRoom).toHaveBeenCalledTimes(1)

    // 对方进房的推送到了（store 里就是把 roomTick 加一，大厅 watch 它重拉）。
    const pool = usePoolStore()
    pool.roomTick += 1
    await flushPromises()
    // 前一次还在路上，这一发只能先记下，不能发出去。
    expect(mocks.myRoom).toHaveBeenCalledTimes(1)

    // 前一次回来了：补拉的那一发必须立刻发出，并且用补回来的那份画出对面。
    mocks.myRoom.mockResolvedValue(occupied())
    release(room())
    await flushPromises()
    expect(mocks.myRoom).toHaveBeenCalledTimes(2)
    expect(wrapper.findAll('.pl__seat')[1].text()).toContain('小满')
  })

  it('座位号是 1 时自己仍然排在左边，标题说明这是对方的房间', async () => {
    // ready 故意反过来：只有座位 1（我）准备了。
    // 如果代码按「界面上的第几项」去取 ready，这一条就会红。
    mocks.myRoom.mockResolvedValue(
      occupied({
        seat: 1,
        ready: [false, true],
        players: [
          { user_id: 12, nickname: '房主甲', avatar: '' },
          { user_id: 9, nickname: '我', avatar: '' },
        ],
      }),
    )
    const { wrapper } = await mountPage()

    const seats = wrapper.findAll('.pl__seat')
    expect(seats[0].classes()).toContain('is-me')
    expect(seats[0].text()).toContain('我')
    expect(seats[0].classes()).toContain('is-ready')
    expect(seats[1].text()).toContain('房主甲')
    expect(wrapper.find('.pl__room-title').text()).toContain('对方的房间')

    // 在别人的房间里既不能开自己的房、也不该去邀请第三个人：
    // 这两块整块不出现，免得点了只拿到一句「你已经在别人的房间里了」。
    expect(wrapper.find('.pl__join').exists()).toBe(false)
    expect(wrapper.find('.pl__invite').exists()).toBe(false)
    // 但必须能退出去，否则就卡在这一页了。
    expect(wrapper.find('.pl__leave').exists()).toBe(true)
  })

  it('点准备把请求发出去，并按返回的状态更新界面', async () => {
    mocks.myRoom.mockResolvedValue(occupied())
    mocks.ready.mockResolvedValue(occupied({ ready: [true, false] }))
    const { wrapper } = await mountPage()

    await wrapper.find('.pl__ready').trigger('click')
    await flushPromises()

    expect(mocks.ready).toHaveBeenCalledWith(7, true)
    expect(wrapper.find('.pl__ready').text()).toBe('取消准备')
    expect(wrapper.findAll('.pl__seat')[0].classes()).toContain('is-ready')
  })

  it('返回的是 playing 时直接跳对局页', async () => {
    mocks.myRoom.mockResolvedValue(occupied({ status: 'playing' }))
    await mountPage()

    expect(mocks.replace).toHaveBeenCalledWith('/pool/match/7')
  })

  it('房间没开出来时留下重试入口，点一下会立刻再要一次', async () => {
    mocks.myRoom.mockRejectedValueOnce(new Error('boom'))
    const { wrapper } = await mountPage()

    expect(wrapper.find('.pl__retry').exists()).toBe(true)
    mocks.myRoom.mockResolvedValue(room())
    await wrapper.find('.pl__retry').trigger('click')
    await flushPromises()

    expect(wrapper.find('.pl__code-value').text()).toBe('123456')
  })

  // 对面有人在等的时候两秒一轮：对方的准备状态要尽快看到。
  it('对面有人时两秒轮询一次，准备标记自己出现', async () => {
    vi.useFakeTimers()
    mocks.myRoom.mockResolvedValueOnce(occupied()).mockResolvedValue(occupied({ ready: [false, true] }))
    const pinia = createPinia()
    setActivePinia(pinia)

    const wrapper = mount(LobbyPage, { global: { plugins: [pinia], stubs } })
    await vi.advanceTimersByTimeAsync(2500)

    expect(mocks.myRoom.mock.calls.length).toBeGreaterThan(1)
    expect(wrapper.findAll('.pl__seat')[1].classes()).toContain('is-ready')
  })

  // 只有自己时十秒一轮：对面进人靠推送，两秒一次的轮询只会把限流额度烧掉。
  it('只有自己时十秒才轮询一次', async () => {
    vi.useFakeTimers()
    const pinia = createPinia()
    setActivePinia(pinia)

    mount(LobbyPage, { global: { plugins: [pinia], stubs } })
    await vi.advanceTimersByTimeAsync(2500)

    // 挂载时那一次之外不该有第二个请求。
    expect(mocks.myRoom).toHaveBeenCalledTimes(1)
  })

  // 有人进房的推送一到就重拉，不等下一轮轮询——这是上面那条慢轮询的前提。
  it('收到「有人进房」的推送后立刻重拉一次', async () => {
    mocks.myRoom.mockResolvedValueOnce(room()).mockResolvedValue(occupied())
    const pinia = createPinia()
    setActivePinia(pinia)
    const pool = usePoolStore()

    const wrapper = mount(LobbyPage, { global: { plugins: [pinia], stubs } })
    await flushPromises()
    expect(wrapper.findAll('.pl__seat')[1].classes()).toContain('is-empty')

    pool.roomTick += 1
    await flushPromises()

    expect(wrapper.findAll('.pl__seat')[1].classes()).not.toContain('is-empty')
    expect(wrapper.findAll('.pl__seat')[1].text()).toContain('小满')
  })
})

describe('人机对战', () => {
  // 人机局不需要房号、也不需要等谁点准备：点一下就该直接进对局页。
  // 入口在空房那张卡片的「等对手进来」下面——页头那颗同名按钮已经撤掉，
  // 两个入口说的是同一件事，只留更贴近场景的那一个。
  it('点「先和机器人练一杆」直接开局并跳到对局页', async () => {
    mocks.bot.mockResolvedValue(room({ match_id: 66, status: 'playing' }))
    const { wrapper } = await mountPage()

    const also = wrapper.find('.pl__also-btn')
    expect(also.exists()).toBe(true)
    await also.trigger('click')
    await flushPromises()

    expect(mocks.bot).toHaveBeenCalledTimes(1)
    expect(mocks.replace).toHaveBeenCalledWith('/pool/match/66')
    // 页头那一排只剩「输入房号」与「我的战绩」。
    const tools = wrapper.findAll('.pl__tool').map((item) => item.text())
    expect(tools).not.toContain('人机对战')
  })

  // 对面已经坐了人时不该再出现这一行：那会儿想打球就是跟他打。
  it('座位有人时不显示这一行', async () => {
    mocks.myRoom.mockResolvedValue(occupied())
    const { wrapper } = await mountPage()

    expect(wrapper.find('.pl__also-btn').exists()).toBe(false)
  })

  // 开局失败时按钮要能再点：一直灰着等于让人重启页面。
  it('开局失败后按钮解锁', async () => {
    mocks.bot.mockRejectedValue(new Error('boom'))
    const { wrapper } = await mountPage()

    const button = wrapper.find('.pl__also-btn')
    await button.trigger('click')
    await flushPromises()

    expect(button.attributes('disabled')).toBeUndefined()
    expect(button.text()).toBe('先和机器人练一杆')
    expect(mocks.replace).not.toHaveBeenCalled()
  })
})

describe('分享链接', () => {
  // 点链接进来就直接坐进对面那个座位，不用再手抄六位房号。
  it('带房号进来直接进房，并替他按下准备', async () => {
    routeState.params = { code: '654321' }
    mocks.join.mockResolvedValue(
      room({
        match_id: 31,
        room_code: '654321',
        seat: 1,
        ready: [false, true],
        players: [
          { user_id: 12, nickname: '房主甲', avatar: '' },
          { user_id: 9, nickname: '我', avatar: '' },
        ],
      }),
    )
    const { wrapper } = await mountPage()

    // 第二个参数是「顺手替我准备」：点链接这个动作已经表达了我要打。
    expect(mocks.join).toHaveBeenCalledWith('654321', true)
    // 不能先给自己开一间：那会让房间卡闪一下「我的房间」再换成对方的。
    expect(mocks.myRoom).not.toHaveBeenCalled()
    // 地址换回 /pool，留在 join 链接上刷新一次会再进一次房。
    expect(mocks.replace).toHaveBeenCalledWith('/pool')
    expect(wrapper.find('.pl__code-value').text()).toBe('654321')
    expect(wrapper.find('.pl__room-title').text()).toContain('对方的房间')
  })

  // 房间过期、链接被改过：不能把人卡在一个坏链接上。
  it('链接失效时退回大厅，并给自己开一间房', async () => {
    routeState.params = { code: '111111' }
    mocks.join.mockRejectedValue(new Error('房间不在了'))
    const { wrapper } = await mountPage()
    await flushPromises()

    expect(mocks.replace).toHaveBeenCalledWith('/pool')
    expect(mocks.myRoom).toHaveBeenCalled()
    expect(wrapper.find('.pl__code-value').text()).toBe('123456')
    // 不是人满，就不该出现那条提示。
    expect(wrapper.find('.pl__full').exists()).toBe(false)
  })

  // 分享链接是发给一群人的：第一个点开的坐进来，后面的人要看到「人满」。
  it('链接那间房坐满了，页面上要说清人满并把自己那间开出来', async () => {
    routeState.params = { code: '222222' }
    // 拦截器把 409 的 error_code 挂在错误对象上，页面按它分支。
    const refused = Object.assign(new Error('房间人满，这张球桌已经坐满了'), {
      errorCode: 'room_full',
    })
    mocks.join.mockRejectedValue(refused)
    const { wrapper } = await mountPage()
    await flushPromises()

    expect(mocks.replace).toHaveBeenCalledWith('/pool')
    // 不能停在别人的满桌上：自己的桌子照样要开出来。
    expect(mocks.myRoom).toHaveBeenCalled()
    const notice = wrapper.find('.pl__full')
    expect(notice.exists()).toBe(true)
    expect(notice.text()).toContain('人满')
    // 说明要留在页面上，不能只弹一条会自己消失的 toast。
    expect(wrapper.find('.pl__code-value').text()).toBe('123456')
  })

  // 分享发的是链接而不是房号：对方点开就进来，不用再手动输。
  it('点分享复制一条能直接进房的链接', async () => {
    const writeText = vi.fn((text: string) => Promise.resolve(text))
    // jsdom 没有 navigator.share，正好走剪贴板那条路。
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })

    const { wrapper } = await mountPage()
    const button = wrapper.find('.pl__share')
    expect(button.exists()).toBe(true)
    await button.trigger('click')
    await flushPromises()

    const copied = String(writeText.mock.calls[0][0])
    expect(copied).toContain(window.location.origin + '/pool/join/123456')
    // 光一条网址看不出是打球：说明里要有台球，收链接的人才认得出。
    expect(copied).toContain('台球')
    expect(copied).toContain('先到先得')
    // 撤掉这份假 clipboard，别让它漏到下一条用例里。
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
  })

  // 电脑上的 navigator.share 接的是 Windows 系统分享，点下去只会弹「共享失败」。
  // 桌面一律走剪贴板：点一下就复制，用户自己粘给对方。
  it('桌面不调系统分享，点一下就复制到剪贴板', async () => {
    const writeText = vi.fn((text: string) => Promise.resolve(text))
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    // jsdom 的 UA 是桌面版，这里把 share 也塞进去：有也不能用。
    const share = vi.fn(() => Promise.resolve())
    Object.defineProperty(navigator, 'share', { value: share, configurable: true })

    const { wrapper } = await mountPage()
    await wrapper.find('.pl__share').trigger('click')
    await flushPromises()

    expect(share).not.toHaveBeenCalled()
    expect(writeText).toHaveBeenCalledTimes(1)
    // 桌面上的动作是「复制」，按钮文案要跟着说实话。
    expect(wrapper.find('.pl__share-go').text()).toBe('复制链接')

    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    Object.defineProperty(navigator, 'share', { value: undefined, configurable: true })
  })

  // 剪贴板被禁（非 https、浏览器不给权限）时，得让人看得见链接、手动选中。
  it('复制失败时把链接摆出来让人手动复制', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn(() => Promise.reject(new Error('denied'))) },
      configurable: true,
    })
    const { ElMessageBox } = await import('element-plus')

    const { wrapper } = await mountPage()
    await wrapper.find('.pl__share').trigger('click')
    await flushPromises()

    expect(vi.mocked(ElMessageBox.alert)).toHaveBeenCalledTimes(1)
    expect(String(vi.mocked(ElMessageBox.alert).mock.calls[0][0])).toBe(
      window.location.origin + '/pool/join/123456',
    )

    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
  })

  // 手机上的 share 走的是微信、QQ 那套系统面板，能用就继续用。
  it('手机点分享走系统分享面板，不走剪贴板', async () => {
    const uaDesc = Object.getOwnPropertyDescriptor(navigator, 'userAgent')
    Object.defineProperty(navigator, 'userAgent', {
      value:
        'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148',
      configurable: true,
    })
    const share = vi.fn(() => Promise.resolve())
    Object.defineProperty(navigator, 'share', { value: share, configurable: true })
    const writeText = vi.fn((text: string) => Promise.resolve(text))
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })

    const { wrapper } = await mountPage()
    await wrapper.find('.pl__share').trigger('click')
    await flushPromises()

    expect(share).toHaveBeenCalledTimes(1)
    expect(writeText).not.toHaveBeenCalled()
    expect(wrapper.find('.pl__share-go').text()).toBe('发链接')

    if (uaDesc) {
      Object.defineProperty(navigator, 'userAgent', uaDesc)
    } else {
      delete (navigator as { userAgent?: string }).userAgent
    }
    Object.defineProperty(navigator, 'share', { value: undefined, configurable: true })
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
  })

  // 对面有人之后再分享只会让对方点进来吃一句「人满」。
  it('对面有人时不给分享入口', async () => {
    mocks.myRoom.mockResolvedValue(occupied())
    const { wrapper } = await mountPage()

    expect(wrapper.find('.pl__share').exists()).toBe(false)
  })
})

describe('输房号进房', () => {
  it('输入条平时收着，点「输入房号」才展开，满六位就把房号发出去', async () => {
    mocks.join.mockResolvedValue(
      occupied({
        seat: 1,
        players: [
          { user_id: 12, nickname: '房主甲', avatar: '' },
          { user_id: 9, nickname: '我', avatar: '' },
        ],
      }),
    )
    const { wrapper } = await mountPage()

    // 不常驻：页面最显眼的位置留给房间卡。
    expect(wrapper.find('.pl__join').exists()).toBe(false)
    await toolButton(wrapper, '输入房号').trigger('click')

    const input = wrapper.find('.pl__join-input')
    await input.setValue('12345')
    expect(wrapper.find('.pl__join-btn').attributes('disabled')).toBeDefined()

    await input.setValue('123456')
    await wrapper.find('.pl__join').trigger('submit')
    await flushPromises()

    expect(mocks.join).toHaveBeenCalledWith('123456')
    // 进房之后整份快照换掉：标题变成「对方的房间」，输入条收了回去。
    expect(wrapper.find('.pl__room-title').text()).toContain('对方的房间')
    expect(wrapper.find('.pl__join').exists()).toBe(false)
  })

  it('输入里的非数字被过滤掉，长度也只留六位', async () => {
    const { wrapper } = await mountPage()
    await toolButton(wrapper, '输入房号').trigger('click')

    const input = wrapper.find('.pl__join-input')
    await input.setValue('12 34-56 78')
    await input.trigger('input')

    expect((input.element as HTMLInputElement).value).toBe('123456')
  })
})

describe('换一间房', () => {
  it('对面一直不进来时可以换一间，房号跟着更新', async () => {
    mocks.reopen.mockResolvedValue(room({ room_code: '654321' }))
    const { wrapper } = await mountPage()

    expect(wrapper.find('.pl__code-value').text()).toBe('123456')
    await wrapper.find('.pl__leave').trigger('click')
    await flushPromises()

    expect(mocks.reopen).toHaveBeenCalled()
    expect(wrapper.find('.pl__code-value').text()).toBe('654321')
  })
})

describe('在线名单', () => {
  it('在线的人可以直接邀请，不再要求关注', async () => {
    mocks.online.mockResolvedValue([onlineUser({ user_id: 11, nickname: '陆陆', following: false })])
    mocks.invite.mockResolvedValue({
      invite_id: 'inv-1',
      match_id: 7,
      room_code: '123456',
      expires_in: 60,
    })
    const { wrapper } = await mountPage()

    const row = wrapper.find('.pl__row')
    expect(row.text()).toContain('陆陆')

    await row.find('.pl__invite').trigger('click')
    await flushPromises()

    expect(mocks.invite).toHaveBeenCalledWith(11)
    // 发出去之后那一行变成「已发送」，避免连点拿到 409。
    expect(wrapper.find('.pl__invite').text()).toBe('已发送')
  })

  it('没人在线时只说这一句，不再把房号念一遍', async () => {
    const { wrapper } = await mountPage()

    const empty = wrapper.find('.pl__empty')
    expect(empty.text()).toBe('暂无其他人在线')
    expect(empty.text()).not.toContain('123456')
  })

  // 一个人待在大厅时名单本来就是空的：每十秒那一轮轮询不该把它换成骨架屏。
  // 「右边的在线列表总在闪」就是这条路径：空列表 → 骨架屏 → 空列表，十秒一次。
  it('名单是空的时候，轮询不会把「暂无其他人在线」换成骨架屏', async () => {
    vi.useFakeTimers()
    mocks.online.mockResolvedValueOnce([])
    // 第二轮故意挂住：这样这一发在途期间正好是「以前会闪」的那个窗口。
    mocks.online.mockImplementationOnce(() => new Promise<OnlineUser[]>(() => {}))

    const pinia = createPinia()
    setActivePinia(pinia)
    const wrapper = mount(LobbyPage, { global: { plugins: [pinia], stubs } })
    await vi.advanceTimersByTimeAsync(50)

    expect(wrapper.find('.pl__empty').text()).toBe('暂无其他人在线')

    await vi.advanceTimersByTimeAsync(10000)

    expect(mocks.online).toHaveBeenCalledTimes(2)
    expect(wrapper.find('.el-skeleton').exists()).toBe(false)
    expect(wrapper.find('.pl__empty').text()).toBe('暂无其他人在线')
  })

  it('名单拉失败时给重试入口，不当成「没人在线」', async () => {
    mocks.online.mockRejectedValueOnce(new Error('boom'))
    const { wrapper } = await mountPage()

    // 说的是「没加载出来」，而不是「没有其他人在线」。
    expect(wrapper.find('.pl__empty').text()).toContain('没加载出来')
    mocks.online.mockResolvedValue([onlineUser()])
    await wrapper.find('.pl__retry').trigger('click')
    await flushPromises()

    expect(wrapper.findAll('.pl__row')).toHaveLength(1)
  })
})

describe('大厅的观战区块', () => {
  // 冷启动时个人资料还在路上，user.userId 是 0。
  // 这时候轮询的话，「哪一局是我在打」全都没有答案，自己的局会摆上「观战」，
  // 点进去等于用第二条连接顶掉自己那条对局连接。
  it('资料没回来之前不轮询，回来之后才开始', async () => {
    const { user } = await mountPage(false)
    expect(mocks.live).not.toHaveBeenCalled()

    user.profileLoaded = true
    await flushPromises()

    expect(mocks.live).toHaveBeenCalledTimes(1)
  })

  it('自己的局给「回到对局」，别人的局才给「观战」', async () => {
    mocks.live.mockResolvedValue([
      liveMatch({
        match_id: 31,
        players: [
          { user_id: 9, nickname: '我', avatar: '' },
          { user_id: 8, nickname: '小满', avatar: '' },
        ],
      }),
      liveMatch({ match_id: 32 }),
    ])

    const { wrapper, user } = await mountPage(false)
    user.profile = me(9)
    user.profileLoaded = true
    await flushPromises()

    const watchRows = wrapper.findAll('.pl__watch')
    expect(watchRows[0].text()).toBe('回到对局')
    expect(watchRows[0].attributes('href')).toBe('/pool/match/31')
    expect(watchRows[1].text()).toBe('观战')
    expect(watchRows[1].attributes('href')).toBe('/pool/watch/32')
  })

  // 失败时面板必须留着：没有它，用户就没有任何地方能点重试。
  // 点重试的那一刻也不能先清标记——面板是 v-if="live.length > 0 || liveFailed"，
  // 先清会让空列表时整块消失几百毫秒。
  it('刷新失败后面板留下并能重试，重试时不清空面板', async () => {
    mocks.live.mockRejectedValueOnce(new Error('boom'))
    const { wrapper } = await mountPage()

    const livePanel = wrapper.find('.pl__live')
    expect(livePanel?.find('.pl__retry').exists()).toBe(true)

    // 第二次让它慢一点回，好在「还在路上」的那一刻看面板状态。
    let release: (value: LiveMatch[]) => void = () => {}
    mocks.live.mockImplementationOnce(
      () =>
        new Promise<LiveMatch[]>((resolve) => {
          release = resolve
        }),
    )
    await livePanel?.find('.pl__retry').trigger('click')
    await flushPromises()

    // 请求还没回来：面板和重试按钮都还在。
    expect(wrapper.find('.pl__live').find('.pl__retry').exists()).toBe(true)

    release([liveMatch()])
    await flushPromises()

    expect(wrapper.find('.pl__live').find('.pl__retry').exists()).toBe(false)
    expect(wrapper.findAll('.pl__watch')).toHaveLength(1)
  })
})
