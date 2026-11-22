import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { DownMessage, GameSocketOptions } from '@/game/protocol'
import type { BallState, MatchSnapshot } from '@/game/types'

// 对局页的用例只盯「服务端说了什么，界面就说什么」这一层：
//   1. 进球之后回合没换人 → 卡片与横幅都说「继续击打」，不是「你的回合」；
//   2. 真的换人了 → 不再挂连击标记；
//   3. 拿到手中球 → 弹一块「自由球」大牌子，底栏也留着一条。
//
// GameSocket 与 animator 都换成假的：前者要连 WebSocket，后者靠 rAF 与真实时间，
// jsdom 里都不成立。动画由用例自己决定什么时候播完，这样才能精确控制
// 「帧在路上」与「播完了」两个状态。

const mocks = vi.hoisted(() => ({
  options: [] as unknown[],
  sends: [] as string[],
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

      connect() {}

      close() {}

      // 上行命令全部记下来：这一页的用例不验上行，但被调到说明流程走歪了。
      shoot() {
        mocks.sends.push('shoot')
        return 1
      }

      placeCue() {
        mocks.sends.push('place_cue')
        return 1
      }

      sendAim() {
        mocks.sends.push('aim')
      }

      leave() {
        mocks.sends.push('leave')
        return 1
      }
    },
  }
})

vi.mock('@/game/animator', async (importOriginal) => {
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
    // 页面用它发「中途离开算判负」，这套用例不离开，给一个空实现。
    onBeforeRouteLeave: () => {},
  }
})

const MatchPage = (await import('@/views/pool/match.vue')).default

// 球台与力度条都是 canvas / 拖拽组件，这里只关心文案，用空壳替掉。
// 球台那个壳留两个按钮：一个是「点了一下台面」，一个是「拖动后抬手」，
// 摆球流程的两种落位方式各走一条。
const stubs = {
  PoolTable: {
    props: ['balls', 'aim'],
    emits: ['tap', 'drag'],
    template:
      '<div>' +
      '<button class="tbl" @click="$emit(\'tap\', { x: 0.5, y: 0.6 })" />' +
      '<button class="tbl-drag" @click="$emit(\'drag\', { phase: \'end\', dx: 30, dy: 10, point: { x: 0.7, y: 0.8 } })" />' +
      '</div>',
  },
  PowerSlider: { template: '<div class="pw" />' },
  'el-avatar': { template: '<span class="el-avatar"><slot /></span>' },
  'el-icon': { template: '<span class="el-icon"><slot /></span>' },
}

// snapshot 造一份对局页视角的快照：我在座位 0，默认轮到我。
function snapshot(patch: Partial<MatchSnapshot> = {}): MatchSnapshot {
  const base: MatchSnapshot = {
    match_id: 7,
    room_code: '123456',
    status: 'playing',
    seat: 0,
    turn: 0,
    deadline_ms: 20000,
    balls: [
      { id: 'cue', x: 0.5, y: 0.5, z: 0, on_table: true },
      { id: '1', x: 0.6, y: 1.0, z: 0, on_table: true },
    ],
    group: ['solids', 'stripes'],
    ready: [true, true],
    ball_in_hand: false,
    cue_placed: false,
    shot_seq: 1,
    players: [
      { user_id: 1, nickname: '阿泽', avatar: '' },
      { user_id: 2, nickname: '小满', avatar: '' },
    ],
    winner: null,
    win_reason: '',
  }
  return { ...base, ...patch }
}

// cuePotted 造一份「母球已经进袋」的球位：它还在列表里但 on_table=false，
// 坐标停在袋口附近且 z 为负——这正是摔袋之后、落位之前的状态。
function cuePotted(): BallState[] {
  return [
    { id: 'cue', x: 0.2, y: 0.1, z: -0.05, on_table: false },
    { id: '1', x: 0.6, y: 1.0, z: 0, on_table: true },
  ]
}

// shotFrame 造一杆的轨迹。frames 与 final 只要形状对得上就够，
// 动画是假的，不会真去插值。
function shotFrame(seat: 0 | 1) {
  return {
    seq: 2,
    seat,
    params: { phi: 0, power: 0.5, theta: 0, a: 0, b: 0 },
    duration: 1.2,
    events: [],
    frames: [],
    final: [{ id: 'cue', x: 0.5, y: 0.6, z: 0, on_table: true }],
    sim_ms: 30,
  }
}

// mountPage 装一次对局页，返回包装器与「往页面里灌帧」的入口。
async function mountPage() {
  // 页面 setup 里就要 usePoolStore（「再约一局」用），没有 pinia 会直接抛。
  const pinia = createPinia()
  setActivePinia(pinia)
  const wrapper = mount(MatchPage, { props: { id: '7' }, global: { plugins: [pinia], stubs } })
  await flushPromises()
  const options = mocks.options[mocks.options.length - 1] as GameSocketOptions
  const push = (msg: DownMessage) => options.onMessage(msg)
  // finishShot 让当前这一杆的动画「播完」，页面才会把压住的 turn/state 放出来。
  const finishShot = async () => {
    const call = mocks.playCalls[mocks.playCalls.length - 1]
    call.options.onDone?.()
    await flushPromises()
  }
  return { wrapper, push, finishShot }
}

beforeEach(() => {
  mocks.options.length = 0
  mocks.sends.length = 0
  mocks.playCalls.length = 0
  mocks.replace.mockReset()
  // 页面挂载时先看有没有登录态，没有就直接跳登录页、什么都不做。
  localStorage.setItem('forum_token', 'test-token')
})

describe('对局页的回合提示', () => {
  it('进了球回合没换人：卡片与横幅都写「继续击打」', async () => {
    const { wrapper, push, finishShot } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    // 这一杆还是我打的，打完 turn 帧里的座位没变。
    push({ type: 'shot', ts: 0, data: shotFrame(0) })
    await flushPromises()
    push({ type: 'turn', ts: 0, data: { seat: 0, deadline_ms: 20000 } })
    await finishShot()

    // 横幅说的是连击，不是「你的回合」——玩家要能分辨这两种情况。
    expect(wrapper.find('.tb').text()).toContain('继续击打')
    // 卡片上还要一直挂着，横幅滑走之后也看得到。
    expect(wrapper.find('.sc__cont').text()).toBe('继续击打')
  })

  it('换人了就不挂连击标记，横幅说「对手回合」', async () => {
    const { wrapper, push, finishShot } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    push({ type: 'shot', ts: 0, data: shotFrame(0) })
    await flushPromises()
    push({ type: 'turn', ts: 0, data: { seat: 1, deadline_ms: 20000 } })
    await finishShot()

    expect(wrapper.find('.sc__cont').exists()).toBe(false)
    expect(wrapper.find('.tb').text()).toContain('对手回合')
  })
})

describe('对局页的自由球', () => {
  it('母球摔袋后进摆球模式：弹牌子，底栏换成提示条', async () => {
    const { wrapper, push } = await mountPage()
    push({
      type: 'state',
      ts: 0,
      data: snapshot({ ball_in_hand: true, balls: cuePotted() }),
    })
    await flushPromises()

    const tip = wrapper.find('.pm__ballin')
    expect(tip.exists()).toBe(true)
    expect(tip.text()).toContain('自由球')

    // 底栏那一条不能只靠牌子：牌子三秒后就收了，而球可能还没落。
    expect(wrapper.find('.pm__place-tag').text()).toBe('自由球')
    // 摆球时底部微调尺换成提示，不该同时出现。
    expect(wrapper.find('.ft').exists()).toBe(false)
    // 母球还没落到台上，这时候没有杆可以瞄，「去瞄准」点了也没用。
    expect(wrapper.find('.pm__place-btn').attributes('disabled')).toBeDefined()
  })

  it('母球还在台上时先给瞄准线，底栏留一个重摆入口', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ ball_in_hand: true }) })
    await flushPromises()

    // 犯规但没摔袋：球就在台上，多数人只想接着瞄这一杆。
    expect(wrapper.find('.pm__place').exists()).toBe(false)
    expect(wrapper.find('.ft').exists()).toBe(true)
    expect(wrapper.find('.pm__reopen').text()).toContain('自由球')
  })

  it('摆下去的母球还能再挪：每次点台面都发一条 place_cue', async () => {
    const { wrapper, push } = await mountPage()
    push({
      type: 'state',
      ts: 0,
      data: snapshot({ ball_in_hand: true, balls: cuePotted() }),
    })
    await flushPromises()

    await wrapper.find('.tbl').trigger('click')
    await flushPromises()

    // 服务端落位成功：母球回到台上，手中球的权利还留着（出杆才收）。
    push({ type: 'state', ts: 0, data: snapshot({ ball_in_hand: true, cue_placed: true }) })
    await flushPromises()
    // 球一落到台上就回瞄准，微调尺与重摆入口一起回来。
    expect(wrapper.find('.pm__place').exists()).toBe(false)
    expect(wrapper.find('.ft').exists()).toBe(true)

    // 点「重摆」再进摆球模式，第二下照样能挪。
    await wrapper.find('.pm__reopen').trigger('click')
    await wrapper.find('.tbl').trigger('click')
    await flushPromises()

    expect(mocks.sends.filter((item) => item === 'place_cue')).toHaveLength(2)
  })

  it('手机上拖动摆球：抬手那一下落位', async () => {
    const { wrapper, push } = await mountPage()
    push({
      type: 'state',
      ts: 0,
      data: snapshot({ ball_in_hand: true, balls: cuePotted() }),
    })
    await flushPromises()

    // 触摸屏没有悬停，拖动是唯一的挪法；落位挂在抬手上，
    // 不然手指松开之后球还悬在半空，玩家会以为卡住了。
    await wrapper.find('.tbl-drag').trigger('click')
    await flushPromises()
    expect(mocks.sends).toContain('place_cue')
  })

  it('球落定之后牌子与底栏提示一起消失', async () => {
    const { wrapper, push } = await mountPage()
    push({
      type: 'state',
      ts: 0,
      data: snapshot({ ball_in_hand: true, balls: cuePotted() }),
    })
    await flushPromises()
    expect(wrapper.find('.pm__ballin').exists()).toBe(true)

    push({ type: 'state', ts: 0, data: snapshot({ ball_in_hand: false }) })
    await flushPromises()
    expect(wrapper.find('.pm__ballin').exists()).toBe(false)
    expect(wrapper.find('.pm__place').exists()).toBe(false)
    expect(wrapper.find('.pm__reopen').exists()).toBe(false)
  })
})
