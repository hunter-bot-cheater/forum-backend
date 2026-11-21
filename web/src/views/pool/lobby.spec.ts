import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { LiveMatch } from '@/api/pool'
import type { MeResp } from '@/api/user'
import { useUserStore } from '@/store/modules/user'

// 大厅的用例只盯「正在进行的对局」这一块，其余部分（关注列表、邀请卡片）
// 各有自己的链路，不在这里重复。三条一个主题：
//   1. 资料没回来之前不轮询——否则自己的局会被当成别人的局摆上「观战」；
//   2. 认出自己的局就换成「回到对局」，不能让观战连接顶掉自己的对局连接；
//   3. 刷新失败后点重试，面板不能先消失一下再回来。
//
// api 与 ws 全部换成假的：真实现要发 HTTP、连 WebSocket，jsdom 里都不成立。

const mocks = vi.hoisted(() => ({
  live: vi.fn(),
  follows: vi.fn(),
}))

vi.mock('@/api/pool', () => ({
  getLiveMatches: mocks.live,
  listPendingInvites: vi.fn(() => Promise.resolve([])),
  createInvite: vi.fn(),
  acceptInvite: vi.fn(),
  declineInvite: vi.fn(),
}))

vi.mock('@/api/user', () => ({
  listFollows: mocks.follows,
}))

// pool store 在 setup 里注册推送回调，真实现会去连 WebSocket。
vi.mock('@/utils/ws', () => ({
  onFrame: () => {},
  onReconnect: () => {},
}))

const LobbyPage = (await import('@/views/pool/lobby.vue')).default

const stubs = {
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  'el-avatar': { template: '<span class="el-avatar"><slot /></span>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  'el-empty': { template: '<div class="el-empty" />' },
  'el-icon': { template: '<span class="el-icon"><slot /></span>' },
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
  mocks.live.mockReset()
  mocks.follows.mockReset()
  mocks.live.mockResolvedValue([])
  // listFollows 的形状是 { list, total }，不是 { items }。
  mocks.follows.mockResolvedValue({ list: [], total: 0 })
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
      liveMatch({ match_id: 31, players: [
        { user_id: 9, nickname: '我', avatar: '' },
        { user_id: 8, nickname: '小满', avatar: '' },
      ] }),
      liveMatch({ match_id: 32 }),
    ])

    const { wrapper, user } = await mountPage(false)
    user.profile = me(9)
    user.profileLoaded = true
    await flushPromises()

    const rows = wrapper.findAll('.pl__row')
    expect(rows).toHaveLength(2)
    expect(rows[0].find('.pl__watch').text()).toBe('回到对局')
    expect(rows[0].find('.pl__watch').attributes('href')).toBe('/pool/match/31')
    expect(rows[1].find('.pl__watch').text()).toBe('观战')
    expect(rows[1].find('.pl__watch').attributes('href')).toBe('/pool/watch/32')
  })

  // 失败时面板必须留着：没有它，用户就没有任何地方能点重试。
  // 点重试的那一刻也不能先清标记——面板是 v-if="live.length > 0 || liveFailed"，
  // 先清会让空列表时整块消失几百毫秒。
  it('刷新失败后面板留下并能重试，重试时不清空面板', async () => {
    mocks.live.mockRejectedValueOnce(new Error('boom'))
    const { wrapper } = await mountPage()

    expect(wrapper.find('.pl__retry').exists()).toBe(true)
    expect(wrapper.find('.pl__empty').exists()).toBe(true)

    // 第二次让它慢一点回，好在「还在路上」的那一刻看面板状态。
    let release: (value: LiveMatch[]) => void = () => {}
    mocks.live.mockImplementationOnce(
      () =>
        new Promise<LiveMatch[]>((resolve) => {
          release = resolve
        }),
    )
    await wrapper.find('.pl__retry').trigger('click')
    await flushPromises()

    // 请求还没回来：面板和重试按钮都还在。
    expect(wrapper.find('.pl__panel').exists()).toBe(true)
    expect(wrapper.find('.pl__retry').exists()).toBe(true)

    release([liveMatch()])
    await flushPromises()

    expect(wrapper.find('.pl__retry').exists()).toBe(false)
    expect(wrapper.findAll('.pl__row')).toHaveLength(1)
  })
})
