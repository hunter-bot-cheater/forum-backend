import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { usePoolStore } from '@/store/modules/pool'
import type { DownMessage, GameSocketOptions } from '@/game/protocol'
import { CUE_HEAD_STRING } from '@/game/table'
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
  requestRematch: vi.fn(),
  options: [] as unknown[],
  sends: [] as string[],
  // placeAt 记下最后一次落位点的坐标：开球时那条开球线靠它验证。
  placeAt: { x: 0, y: 0 },
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

      placeCue(x: number, y: number) {
        mocks.sends.push('place_cue')
        mocks.placeAt = { x, y }
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

// 「再约一局」走 store 的接口层，这里换成可编程的假实现：
// 真实实现要打 HTTP，用例只关心「点了之后界面变成什么」。
vi.mock('@/api/pool', () => ({
  requestRematch: (id: number) => mocks.requestRematch(id),
  listPendingInvites: () => Promise.resolve([]),
  createInvite: () => Promise.reject(new Error('不该走到这里')),
  acceptInvite: () => Promise.reject(new Error('不该走到这里')),
  declineInvite: () => Promise.resolve(),
  getMyRoom: () => Promise.resolve(null),
}))

const MatchPage = (await import('@/views/pool/match.vue')).default

// 球台与力度条都是 canvas / 拖拽组件，这里只关心文案，用空壳替掉。
// 球台那个壳留两个按钮：一个是「点了一下台面」，一个是「拖动后抬手」，
// 摆球流程的两种落位方式各走一条。
const stubs = {
  PoolTable: {
    props: ['balls', 'aim', 'ghost', 'headString'],
    emits: ['tap', 'drag'],
    template:
      '<div class="tbl-wrap" :data-ghost="ghost ? ghost.x + \',\' + ghost.y : \'\'"' +
      ' :data-head="headString ? \'1\' : \'\'"' +
      ' :data-phi="aim ? aim.phi : \'\'">' +
      '<button class="tbl" @click="$emit(\'tap\', { x: 0.5, y: 0.6 })" />' +
      '<button class="tbl-drag" @click="$emit(\'drag\', { phase: \'end\', dx: 30, dy: 10, point: { x: 0.7, y: 0.8 } })" />' +
      '<button class="tbl-aim-move" @click="$emit(\'drag\', { phase: \'move\', dx: 20, dy: 10, point: { x: 0.7, y: 0.8 } })" />' +
      '<button class="tbl-aim-end" @click="$emit(\'drag\', { phase: \'end\', dx: 90, dy: 90, point: { x: 0.1, y: 0.1 } })" />' +
      '</div>',
  },
  // 力度条那一下要能真的出杆：出杆之后页面会在本地算整杆并开始播，
  // 「动画播完再放消息」这条路径才有东西可播。
  // 真实滑块是先 update:value 再 shoot，这里按同样的先后发（先改力度再出杆），
  // 少发那一下的话 shoot() 会因为力度不足直接返回。
  PowerSlider: {
    props: ['value', 'disabled'],
    emits: ['update:value', 'shoot'],
    template:
      '<button class="pw" :disabled="disabled" @click="$emit(\'update:value\', 0.6); $emit(\'shoot\')" />',
  },
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
    place_zone: 'anywhere',
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
  // shootOnce 点一下假滑块出杆：页面会先在本地算完这一杆并开始播，
  // 这一段播放就是后面「球还在动」的那个状态。
  const shootOnce = async () => {
    await wrapper.find('.pw').trigger('click')
    await flushPromises()
  }
  return { wrapper, push, finishShot, shootOnce }
}

beforeEach(() => {
  mocks.requestRematch.mockReset()
  mocks.options.length = 0
  mocks.sends.length = 0
  mocks.playCalls.length = 0
  mocks.replace.mockReset()
  // 页面挂载时先看有没有登录态，没有就直接跳登录页、什么都不做。
  localStorage.setItem('forum_token', 'test-token')
})

describe('对局页的回合提示', () => {
  it('进了球回合没换人：顶栏与横幅都写「继续击打」', async () => {
    const { wrapper, push, finishShot, shootOnce } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    await shootOnce()

    // 这一杆还是我打的，打完 turn 帧里的座位没变。
    push({ type: 'shot', ts: 0, data: shotFrame(0) })
    await flushPromises()
    push({ type: 'turn', ts: 0, data: { seat: 0, deadline_ms: 20000 } })
    await finishShot()

    // 横幅说的是连击，不是「你的回合」——玩家要能分辨这两种情况。
    expect(wrapper.find('.tb').text()).toContain('继续击打')
    // 顶栏中间那一格还要一直挂着，横幅滑走之后也看得到。
    expect(wrapper.find('.pm__cont').text()).toBe('继续击打')
  })

  it('换人了就不挂连击标记，横幅说「对手回合」', async () => {
    const { wrapper, push, finishShot, shootOnce } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    await shootOnce()
    push({ type: 'shot', ts: 0, data: shotFrame(0) })
    await flushPromises()
    push({ type: 'turn', ts: 0, data: { seat: 1, deadline_ms: 20000 } })
    await finishShot()

    expect(wrapper.find('.pm__cont').exists()).toBe(false)
    expect(wrapper.find('.tb').text()).toContain('对手回合')
  })
})

describe('对局页的回合强调', () => {
  it('轮到谁，谁的头像上就有金环', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    const cards = wrapper.findAll('.sc')
    expect(cards).toHaveLength(2)
    // 我在座位 0、又轮到座 0：左上那张（永远是我）亮着。
    expect(cards[0].find('.sc__avatar.is-active').exists()).toBe(true)
    expect(cards[1].find('.sc__avatar.is-active').exists()).toBe(false)

    // 换人之后高亮要跟着走，不能两张都亮或都不亮。
    push({ type: 'turn', ts: 0, data: { seat: 1, deadline_ms: 20000 } })
    await flushPromises()
    const after = wrapper.findAll('.sc')
    expect(after[0].find('.sc__avatar.is-active').exists()).toBe(false)
    expect(after[1].find('.sc__avatar.is-active').exists()).toBe(true)
  })

  it('燃烧条的长度就是剩下的读秒比例', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ deadline_ms: 20000 }) })
    await flushPromises()

    const fill = wrapper.find('.pm__fuse-fill')
    expect(fill.exists()).toBe(true)
    const pct = Number.parseFloat((fill.element as HTMLElement).style.width)
    // 一杆 40 秒、还剩 20 秒：正好一半。
    // 允许 1% 的浮动：nowTick 是 tick 那一刻的时间戳，比 deadline 的起点早几毫秒，
    // 算出来的比例会比一半略高一点，这在真实运行里同样是几毫秒的误差。
    expect(Math.abs(pct - 50)).toBeLessThan(1)
  })

  it('还没开局就不画燃烧条：那时服务端没有在读秒', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ status: 'waiting' }) })
    await flushPromises()
    // 元素一直留在 DOM 里（位置要占住，撤掉球台会重新缩放），只是藏起来。
    expect(wrapper.find('.pm__fuse').classes()).toContain('is-hidden')
  })
})

describe('对局页的再约一局', () => {
  // 结算快照：我（座位 0）赢了，原因码与真实结算一致。
  const finished = () => snapshot({ status: 'finished', winner: 0, win_reason: 'eight_legal' })

  it('对方先点了：按钮换成「开局」，并写明是谁想再打', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: finished() })
    await flushPromises()

    expect(wrapper.find('.rs__btn--primary').text()).toBe('再约一局')

    usePoolStore().rematchFrom = {
      match_id: 7,
      from: { user_id: 9, nickname: '小满', avatar: '' },
    }
    await flushPromises()

    expect(wrapper.text()).toContain('小满 想再打一局')
    expect(wrapper.find('.rs__btn--primary').text()).toBe('开局')
  })

  // 推送是全局的，上一局留下的那条不该在新一局的结算卡上出现。
  it('不是这一局的再约请求不显示', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: finished() })
    await flushPromises()

    usePoolStore().rematchFrom = {
      match_id: 88,
      from: { user_id: 9, nickname: '小满', avatar: '' },
    }
    await flushPromises()

    expect(wrapper.text()).not.toContain('想再打一局')
    expect(wrapper.find('.rs__btn--primary').text()).toBe('再约一局')
  })

  // 两个人都点了：新局开起来，这一页自己换过去。
  // 对局页不套全局布局，邀请弹层里那个跳转在这里不存在。
  it('开局信号到达时直接换到新的一局', async () => {
    const { push } = await mountPage()
    push({ type: 'state', ts: 0, data: finished() })
    await flushPromises()

    usePoolStore().rematchStarted = { match_id: 99, room_code: '123456', breaker: 1 }
    await flushPromises()

    expect(mocks.replace).toHaveBeenCalledWith('/pool/match/99')
  })

  it('点过之后进入等待：按钮锁住，提示改成等对方', async () => {
    mocks.requestRematch.mockResolvedValue({ waiting: true })
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: finished() })
    await flushPromises()

    await wrapper.find('.rs__btn--primary').trigger('click')
    await flushPromises()

    expect(mocks.requestRematch).toHaveBeenCalledWith(7)
    expect(wrapper.text()).toContain('已经告诉对方了')
    const button = wrapper.find('.rs__btn--primary')
    expect(button.text()).toBe('等对方回应…')
    expect(button.attributes('disabled')).toBeDefined()

    // 再点一次不该重复发请求：按钮已经锁了，这一条是兜底。
    await button.trigger('click')
    await flushPromises()
    expect(mocks.requestRematch).toHaveBeenCalledTimes(1)
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

  it('犯规但没摔袋，也直接把母球收进手里：指针带着它走，不用先点重摆', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ ball_in_hand: true }) })
    await flushPromises()

    // 只在原地留一颗实球的话，指针移动只是改瞄准方向，玩家会以为白球被钉住了，
    // 必须先点一次「重摆」才能挪——同一件事做两遍。所以这里也要进摆球模式，
    // 而且虚影就落在母球现在的位置，不会凭空跳到台面中央。
    expect(wrapper.find('.tbl-wrap').attributes('data-ghost')).toBe('0.5,0.5')
    expect(wrapper.find('.pm__place').exists()).toBe(true)
    expect(wrapper.find('.pm__place-tag').text()).toBe('自由球')
    // 母球还实实在在在台上，可以直接放弃挪球回瞄准。
    expect(wrapper.find('.pm__place-btn').text()).toContain('不挪了')
    expect(wrapper.find('.pm__place-btn').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('.ft').exists()).toBe(false)

    // 点「不挪了」：虚影收起，母球回到台面，微调尺回来。
    await wrapper.find('.pm__place-btn').trigger('click')
    await flushPromises()
    expect(wrapper.find('.tbl-wrap').attributes('data-ghost')).toBe('')
    expect(wrapper.find('.ft').exists()).toBe(true)
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

describe('对局页的瞄准拖动', () => {
  // 手机上瞄准是「按住 → 拖到目标 → 松手」。松手那一刻的坐标只是机械上的
  // 落点，手指抬起时常带几像素侧向漂移（个别移动端浏览器在 pointercancel
  // 时甚至给 0,0）。拿它再算一次角度，玩家看到的就是「瞄准好了，一松手跳一下」。
  it('抬手不改变方向：方向停在最后一次拖动的位置', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    // 母球在 (0.5, 0.5)，拖到 (0.7, 0.8)：方向是 atan2(0.3, 0.2)。
    await wrapper.find('.tbl-aim-move').trigger('click')
    await flushPromises()
    const moved = Number(wrapper.find('.tbl-wrap').attributes('data-phi'))
    expect(moved).toBeCloseTo(Math.atan2(0.3, 0.2), 4)

    // 抬手落在完全相反的方向（0.1, 0.1）。方向必须一点不动。
    await wrapper.find('.tbl-aim-end').trigger('click')
    await flushPromises()
    expect(Number(wrapper.find('.tbl-wrap').attributes('data-phi'))).toBeCloseTo(moved, 4)
  })
})

describe('对局页的开球摆球', () => {
  // 开球时服务端给的是「开球线后」的手中球：虚影能跟着指针走，
  // 但越线的点必须被夹回线内，否则线上就是画着好看。
  it('虚影落在线内，越线的落点被夹回开球线', async () => {
    const { wrapper, push } = await mountPage()
    push({
      type: 'state',
      ts: 0,
      data: snapshot({ ball_in_hand: true, place_zone: 'behind_head' }),
    })
    await flushPromises()

    // 文案说的是开球，不是犯规判罚的自由球。
    expect(wrapper.find('.pm__place-tag').text()).toBe('开球')
    expect(wrapper.find('.pm__place-tip').text()).toContain('开球线')
    // 台面上要画出那条线：它是「能摆到哪」的唯一提示。
    expect(wrapper.find('.tbl-wrap').attributes('data-head')).toBe('1')

    // 点球堆那一侧（用例的 stub 固定发 y=0.6，越过 0.4953 的开球线）。
    await wrapper.find('.tbl').trigger('click')
    await flushPromises()
    expect(mocks.placeAt.y).toBeCloseTo(CUE_HEAD_STRING, 4)
  })

  it('开球打完就收回限制：下一次手中球是整张台面', async () => {
    const { wrapper, push } = await mountPage()
    push({
      type: 'state',
      ts: 0,
      data: snapshot({ ball_in_hand: true, place_zone: 'behind_head' }),
    })
    await flushPromises()

    push({ type: 'state', ts: 0, data: snapshot({ ball_in_hand: true }) })
    await flushPromises()

    await wrapper.find('.tbl').trigger('click')
    await flushPromises()
    // 同一个点（y=0.6）这时候不该再被夹。
    expect(mocks.placeAt.y).toBeCloseTo(0.6, 4)
    // 台面上也不该再画开球线。
    expect(wrapper.find('.tbl-wrap').attributes('data-head')).toBe('')
  })
})
