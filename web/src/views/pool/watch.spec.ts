import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { DownMessage, GameSocketOptions } from '@/game/protocol'
import type { MatchSnapshot } from '@/game/types'
import { SEAT_SPECTATOR } from '@/game/types'

// 观战页的用例集中在五件事上：
//   1. 快照到达后双方都在、且界面上没有一处「我」；
//   2. 结算与断线的提示文案，以及「重试也没用」时不给重试按钮；
//   3. 观战页一个上行命令都不发（它是只读页面）；
//   4. 动画没播完时到达的快照与结算要排在后面，不能抢跑；
//   5. 自己就是当事人时让位回对局页。
//
// GameSocket 换成假的：真实实现要连 WebSocket，jsdom 里连不上。
// animator 的 playShot 也是假的，让用例自己决定什么时候「播完」。

const mocks = vi.hoisted(() => ({
  options: [] as unknown[],
  connects: 0,
  closes: 0,
  uplinks: [] as string[],
  replace: vi.fn(),
  playCalls: [] as Array<{ options: { onSample: (s: never[]) => void; onDone?: () => void } }>,
}))

vi.mock('@/game/protocol', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/game/protocol')>()
  return {
    ...actual,
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

      // 下面三个是被测页面绝不该调用的：调了就说明观战页在写服务端。
      shoot() {
        mocks.uplinks.push('shoot')
        return 1
      }

      placeCue() {
        mocks.uplinks.push('place_cue')
        return 1
      }

      leave() {
        mocks.uplinks.push('leave')
        return 1
      }
    },
  }
})

vi.mock('@/game/animator', async (importOriginal) => {
  // 只换 playShot：它靠 requestAnimationFrame 与真实时间，
  // 而这几条用例要的是「动画播到一半」这个确定的状态。
  const actual = await importOriginal<typeof import('@/game/animator')>()
  return {
    ...actual,
    playShot: (_shot: unknown, options: never) => {
      mocks.playCalls.push({ options })
      return { stop: () => {} }
    },
  }
})

vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    useRouter: () => ({ replace: mocks.replace, push: vi.fn(), back: vi.fn() }),
  }
})

const WatchPage = (await import('@/views/pool/watch.vue')).default

const stubs = {
  PoolTable: { props: ['balls', 'aim'], template: '<div class="tbl" />' },
  'el-avatar': { template: '<span class="el-avatar"><slot /></span>' },
  'el-icon': { template: '<span class="el-icon"><slot /></span>' },
}

// snapshot 造一份观战视角的快照：座位是 -1，双方都已定组。
//
// 按 MatchSnapshot 逐字段写，不用 as unknown as 绕过校验：
// 绕过之后把 group 写成 'none'（协议里未定组是空串）也照样绿，
// 那正是这个页面上最容易写错的一个值。
function snapshot(patch: Partial<MatchSnapshot> = {}): MatchSnapshot {
  const base: MatchSnapshot = {
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
  }
  return { ...base, ...patch }
}

// mountPage 装一次观战页，返回包装器与「往页面里灌帧」的入口。
async function mountPage() {
  const wrapper = mount(WatchPage, { props: { id: '7' }, global: { stubs } })
  await flushPromises()
  const options = mocks.options[mocks.options.length - 1] as GameSocketOptions
  const push = (msg: DownMessage) => options.onMessage(msg)
  const setState = (state: 'connecting' | 'open' | 'closed' | 'gaveup') => options.onState(state)
  const fatal = (code: number) => options.onFatal?.(code)
  return { wrapper, push, setState, fatal }
}

beforeEach(() => {
  mocks.options.length = 0
  mocks.connects = 0
  mocks.closes = 0
  mocks.uplinks.length = 0
  mocks.playCalls.length = 0
  mocks.replace.mockReset()
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

    // 观战页不该有任何可操作的控件（.wt__power 是对局页的类名，
    // 拿它断言观战页恒真）。只允许「返回」这一个按钮。
    expect(wrapper.findAll('input')).toHaveLength(0)
    expect(wrapper.findAll('[role="slider"]')).toHaveLength(0)
    expect(wrapper.findAll('button')).toHaveLength(1)
    expect(wrapper.find('.wt__turn').text()).toContain('轮到 阿泽')
  })

  // 观战是只读的：一条上行命令都不该发出去。
  // 这条用例挡的是「以后有人在观战页加个按钮顺手接上 shoot」。
  it('全程不发任何上行命令', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    push({ type: 'shot', ts: 0, data: shotFrame() })
    push({ type: 'result', ts: 0, data: { winner: 0, reason: 'eight_legal', elo_delta_0: 0, elo_delta_1: 0 } })
    await flushPromises()

    expect(mocks.uplinks).toEqual([])
    // 卸载时只关连接，也不发 leave：服务端把断线当成观众离场。
    wrapper.unmount()
    expect(mocks.uplinks).toEqual([])
  })

  it('观战者进房用的是同一条对局连接，只连一次，卸载时关掉', async () => {
    const { wrapper } = await mountPage()

    expect(mocks.connects).toBe(1)
    expect(wrapper.find('.wt__badge').text()).toBe('观战')

    wrapper.unmount()
    // 不关的话服务端会一直挂着一位已经离开的观众。
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

  // 结算帧紧跟在最后一杆后面。排在动画前面的话，
  // 会先盖一条「谁赢下这一局」再继续放最后一杆的球，看着像结算抢跑。
  it('动画没播完时结算排在后面', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    push({ type: 'shot', ts: 0, data: shotFrame() })
    push({ type: 'result', ts: 0, data: { winner: 1, reason: 'leave', elo_delta_0: 0, elo_delta_1: 0 } })
    await flushPromises()

    expect(wrapper.find('.wt__end').exists()).toBe(false)

    expect(mocks.playCalls).toHaveLength(1)
    mocks.playCalls[0].options.onDone?.()
    await flushPromises()

    expect(wrapper.find('.wt__end').text()).toBe('小满 赢下这一局')
  })

  // 换人快照紧跟在 shot 帧后面。动画没播完就套新快照的话，
  // 球会先跳到这一杆的终局位置，再被动画拽回开头重放一遍。
  it('动画没播完时换人快照排在后面', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    push({ type: 'shot', ts: 0, data: shotFrame() })
    push({ type: 'state', ts: 0, data: snapshot({ turn: 1 }) })
    await flushPromises()

    // 动画还在播：轮次不该变。
    expect(wrapper.find('.wt__turn').text()).toContain('轮到 阿泽')

    expect(mocks.playCalls).toHaveLength(1)
    mocks.playCalls[0].options.onDone?.()
    await flushPromises()

    expect(wrapper.find('.wt__turn').text()).toContain('轮到 小满')
  })

  // 看完了之后断线，结果条四周不能再露出「正在重连…」——
  // 两个盒子都在球台正中，同时出现就是一层字压一层字。
  it('结算之后断线不再叠提示', async () => {
    const { wrapper, push, setState } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    push({ type: 'result', ts: 0, data: { winner: 1, reason: 'leave', elo_delta_0: 0, elo_delta_1: 0 } })
    await flushPromises()
    expect(wrapper.find('.wt__end').exists()).toBe(true)

    setState('closed')
    await flushPromises()

    expect(wrapper.find('.wt__hint').exists()).toBe(false)
    expect(wrapper.find('.wt__end').exists()).toBe(true)
  })

  // 上一杆还没播完就来了下一杆（后台标签页、慢设备上会有）：
  // 上一杆排队的快照必须立刻生效，否则它会挂到下一杆播完，
  // 轮次与读秒整体倒退一格。
  it('动画中来下一杆时，排队的快照立刻生效', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    push({ type: 'shot', ts: 0, data: shotFrame() })
    push({ type: 'state', ts: 0, data: snapshot({ turn: 1 }) })
    await flushPromises()
    expect(wrapper.find('.wt__turn').text()).toContain('轮到 阿泽')

    push({ type: 'shot', ts: 0, data: shotFrame() })
    await flushPromises()

    expect(mocks.playCalls).toHaveLength(2)
    expect(wrapper.find('.wt__turn').text()).toContain('轮到 小满')
  })

  it('重连窗口走完时给一个重新连接，点了会再连一次', async () => {
    const { wrapper, setState } = await mountPage()
    setState('gaveup')
    await flushPromises()

    expect(wrapper.find('.wt__hint').text()).toContain('连接已断开')
    await wrapper.find('.wt__retry').trigger('click')
    expect(mocks.connects).toBe(2)
  })

  // 名额在握手之后被抢走时，服务端关连接会带 4001。
  // 这种情况下重试必然还是同一个结果，只能给个台阶回大厅。
  it('名额被抢走时说明原因，只给回大厅', async () => {
    const { wrapper, fatal, setState } = await mountPage()
    fatal(4001)
    setState('gaveup')
    await flushPromises()

    expect(wrapper.find('.wt__hint').text()).toContain('观战人数已满')
    const button = wrapper.find('.wt__retry')
    expect(button.text()).toBe('回大厅')
  })

  // 4004 是另一回事：对局已经结束并被回收。话不能一样——
  // 「人数已满」会让人以为等一会还有位置。
  it('对局已经没了时说结束，不说人数已满', async () => {
    const { wrapper, fatal, setState } = await mountPage()
    fatal(4004)
    setState('gaveup')
    await flushPromises()

    const hint = wrapper.find('.wt__hint').text()
    expect(hint).toContain('已经结束')
    expect(hint).not.toContain('人数已满')
    expect(wrapper.find('.wt__retry').text()).toBe('回大厅')
  })

  it('还没拿到快照时提示正在进入，不提前画空球台', async () => {
    const { wrapper } = await mountPage()

    expect(wrapper.find('.wt__hint').text()).toContain('正在进入对局')
    expect(wrapper.find('.wt__turn').text()).toBe('正在进入对局…')
  })

  // 手里有旧快照时掉线，画面会停在最后一帧：
  // 不提示的话看起来就像两个人一起不动了。
  it('已经看到球台之后掉线，仍然要提示正在重连', async () => {
    const { wrapper, push, setState } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()
    expect(wrapper.find('.wt__hint').exists()).toBe(false)

    setState('closed')
    await flushPromises()

    expect(wrapper.find('.wt__hint').text()).toContain('正在重连')
  })

  // 大厅里点开了自己那一局：服务端把自己安排到座位上。
  // 留在观战页等于用第二条连接顶掉自己那条对局连接。
  it('自己就是当事人时让位回对局页，并先关掉这条连接', async () => {
    const { push } = await mountPage()
    push({
      type: 'joined',
      ts: 0,
      data: { match_id: 7, room_code: '123456', seat: 0, practice: false },
    })
    await flushPromises()

    expect(mocks.replace).toHaveBeenCalledWith('/pool/match/7')
    // 跳转是异步的：不先关，导航窗口里它还占着你的座位，
    // 一旦它先断，对手会收到一条「他掉线了」。
    expect(mocks.closes).toBe(1)
  })
})

// shotFrame 造一杆最小可播的轨迹：一帧、两颗球。
function shotFrame() {
  return {
    seq: 4,
    seat: 0 as const,
    params: { phi: 1.5, power: 0.6, theta: 0, a: 0, b: 0 },
    duration: 1,
    events: [],
    frames: [
      {
        t: 0,
        balls: [
          ['cue', 0.5, 0.5, 0],
          ['1', 0.6, 1.0, 0],
        ] as Array<[string, number, number, number]>,
      },
    ],
    final: [],
    sim_ms: 10,
  }
}
