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
  replace: vi.fn(),
}))

vi.mock('@/api/pool', () => ({
  getLiveMatches: (...args: unknown[]) => mocks.live(...args),
  getMyRoom: (...args: unknown[]) => mocks.myRoom(...args),
  getOnlineUsers: (...args: unknown[]) => mocks.online(...args),
  joinRoom: (...args: unknown[]) => mocks.join(...args),
  leaveRoom: (...args: unknown[]) => mocks.leave(...args),
  reopenRoom: (...args: unknown[]) => mocks.reopen(...args),
  setReady: (...args: unknown[]) => mocks.ready(...args),
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
  }
})

vi.mock('vue-router', async (importOriginal) => {
  // 只替换 useRouter：整体替换会让 vue-router 的其它导出消失，
  // 以后加一行 useRoute 就炸。
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    useRouter: () => ({ replace: mocks.replace, push: mocks.replace }),
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
    await wrapper.findAll('.pl__tool')[0].trigger('click')

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
    await wrapper.findAll('.pl__tool')[0].trigger('click')

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
