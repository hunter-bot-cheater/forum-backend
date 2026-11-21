import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { DownMessage, GameSocketOptions } from '@/game/protocol'
import type { MatchSnapshot } from '@/game/types'
import { SEAT_SPECTATOR } from '@/game/types'

// 观战页的用例集中在三件事上：
//   1. 快照到达后双方都在、且界面上没有一处「我」；
//   2. 结算与断线的提示文案；
//   3. 观战连接只连不收命令（页面没有任何操作入口）。
//
// GameSocket 换成假的：真实实现要连 WebSocket，jsdom 里连不上。
// 假 socket 把 onMessage / onState 留在外面，用例自己往里灌帧。

const mocks = vi.hoisted(() => ({
  options: [] as unknown[],
  connects: 0,
  closes: 0,
}))

vi.mock('@/game/protocol', () => ({
  GameSocket: class {
    constructor(options: unknown) {
      mocks.options.push(options)
    }

    connect() {
      mocks.connects += 1
    }

    close() {
      mocks.closes += 1
    }
  },
}))

vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    useRouter: () => ({ replace: vi.fn(), push: vi.fn(), back: vi.fn() }),
  }
})

const WatchPage = (await import('@/views/pool/watch.vue')).default

const stubs = {
  PoolTable: { props: ['balls', 'aim'], template: '<div class="tbl" />' },
  'el-avatar': { template: '<span class="el-avatar"><slot /></span>' },
  'el-icon': { template: '<span class="el-icon"><slot /></span>' },
}

// snapshot 造一份观战视角的快照：座位是 -1，双方都已定组。
function snapshot(patch: Partial<MatchSnapshot> = {}): MatchSnapshot {
  const base = {
    match_id: 7,
    room_code: '123456',
    status: 'playing',
    seat: SEAT_SPECTATOR,
    turn: 0,
    deadline_ms: 20000,
    balls: [
      { id: 'cue', x: 0.5, y: 0.5, z: 0, on_table: true },
      { id: '1', x: 0.6, y: 1.0, z: 0, on_table: true },
      { id: '9', x: 0.7, y: 1.1, z: 0, on_table: true },
    ],
    group: ['solids', 'stripes'],
    ready: [true, true],
    ball_in_hand: false,
    shot_seq: 3,
    players: [
      { user_id: 1, nickname: '阿泽', avatar: '' },
      { user_id: 2, nickname: '小满', avatar: '' },
    ],
    winner: null,
    win_reason: '',
  } as unknown as MatchSnapshot
  return { ...base, ...patch }
}

// mountPage 装一次观战页，返回包装器与「往页面里灌帧」的入口。
async function mountPage() {
  const wrapper = mount(WatchPage, { props: { id: '7' }, global: { stubs } })
  await flushPromises()
  const options = mocks.options[mocks.options.length - 1] as GameSocketOptions
  const push = (msg: DownMessage) => options.onMessage(msg)
  const setState = (state: 'connecting' | 'open' | 'closed' | 'gaveup') => options.onState(state)
  return { wrapper, push, setState }
}

beforeEach(() => {
  mocks.options.length = 0
  mocks.connects = 0
  mocks.closes = 0
})

describe('观战页', () => {
  it('快照到达后双方名字都在，界面上一处「我」都没有', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    const seats = wrapper.find('.wt__seats').text()
    expect(seats).toContain('阿泽')
    expect(seats).toContain('小满')
    // 观战没有「我」这一侧，座位卡不该标「你」。
    expect(seats).not.toContain('你')

    // 观战页不该出现任何操作控件。
    expect(wrapper.find('.wt__power').exists()).toBe(false)
    expect(wrapper.find('.wt__turn').text()).toContain('轮到 阿泽')
  })

  it('观战者进房用的是同一条对局连接，且只连一次', async () => {
    const { wrapper } = await mountPage()

    expect(mocks.connects).toBe(1)
    expect(wrapper.find('.wt__badge').text()).toBe('观战')

    wrapper.unmount()
    // 卸载必须关连接，否则服务端会一直挂着一位已经离开的观众。
    expect(mocks.closes).toBe(1)
  })

  it('结算帧说明是谁赢的，不说「你赢了」', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    push({ type: 'result', ts: 0, data: { winner: 1, reason: 'leave', elo_delta_0: 0, elo_delta_1: 0 } })
    await flushPromises()

    const end = wrapper.find('.wt__end')
    expect(end.text()).toBe('小满 赢下这一局')
    expect(end.text()).not.toContain('你')
  })

  it('重连窗口走完时给一个重新连接，点了会再连一次', async () => {
    const { wrapper, setState } = await mountPage()
    setState('gaveup')
    await flushPromises()

    expect(wrapper.find('.wt__hint').text()).toContain('连接已断开')
    await wrapper.find('.wt__retry').trigger('click')
    expect(mocks.connects).toBe(2)
  })

  it('还没拿到快照时提示正在进入，不提前画空球台', async () => {
    const { wrapper } = await mountPage()

    expect(wrapper.find('.wt__hint').text()).toContain('正在进入对局')
    expect(wrapper.find('.wt__turn').text()).toBe('正在进入对局…')
  })
})
