import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { usePoolStore } from '@/store/modules/pool'
import type { DownMessage, GameSocketOptions } from '@/game/protocol'
import { BALL_RADIUS, CUE_HEAD_STRING } from '@/game/table'
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
  // aimArgs 记下每一帧瞄准上报的四个参数（角度、力度、左右塞、高低杆）。
  aimArgs: [] as Array<[number, number, number, number]>,
  // chats 记下聊天上行：这一页只验「点了发送有没有发出去」。
  chats: [] as string[],
  replace: vi.fn(),
  // 人机局重开：结算卡上那颗「再打一局」走它。
  startBotMatch: vi.fn(),
  playCalls: [] as Array<{
    options: { onSample: (s: never[]) => void; onDone?: () => void; speed?: number }
  }>,
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

      sendAim(phi: number, power: number, a: number, b: number) {
        mocks.sends.push('aim')
        mocks.aimArgs.push([phi, power, a, b])
      }

      sendChat(text: string) {
        mocks.sends.push('chat')
        mocks.chats.push(text)
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
  // 人机局的「再打一局」走这条：结算卡上那颗按钮点下去要开一局新的。
  startBotMatch: () => mocks.startBotMatch(),
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
    // data-peer 把「对家的杆画没画」暴露到 DOM 上：
    // 这件事在画布里看不见，用例只能靠这一份映射来断言。
    props: ['balls', 'aim', 'peerAim', 'ghost', 'headString'],
    emits: ['tap', 'drag', 'hover'],
    template:
      '<div class="tbl-wrap" :data-ghost="ghost ? ghost.x + \',\' + ghost.y : \'\'"' +
      ' :data-head="headString ? \'1\' : \'\'"' +
      ' :data-peer="peerAim ? peerAim.phi : \'\'"' +
      ' :data-phi="aim ? aim.phi : \'\'">' +
      '<button class="tbl" @click="$emit(\'tap\', { x: 0.5, y: 0.6 })" />' +
      '<button class="tbl-drag" @click="$emit(\'drag\', { phase: \'end\', dx: 30, dy: 10, point: { x: 0.7, y: 0.8 } })" />' +
      '<button class="tbl-aim-move" @click="$emit(\'drag\', { phase: \'move\', dx: 20, dy: 10, point: { x: 0.7, y: 0.8 } })" />' +
      '<button class="tbl-aim-end" @click="$emit(\'drag\', { phase: \'end\', dx: 90, dy: 90, point: { x: 0.1, y: 0.1 } })" />' +
      '<button class="tbl-hover" @click="$emit(\'hover\', { x: 0.9, y: 0.2 })" />' +
      '<button class="tbl-hover-far" @click="$emit(\'hover\', { x: 0.1, y: 0.9 })" />' +
      '</div>',
  },
  // 力度条那一下要能真的出杆：出杆之后页面会在本地算整杆并开始播，
  // 「动画播完再放消息」这条路径才有东西可播。
  // 真实滑块是先 update:value 再 shoot，这里按同样的先后发（先改力度再出杆），
  // 少发那一下的话 shoot() 会因为力度不足直接返回。
  PowerSlider: {
    name: 'PowerSlider',
    props: ['value', 'disabled', 'readonly'],
    emits: ['update:value', 'shoot'],
    // data-value 让用例能读到「这一条现在显示谁的力度」；
    // disabled 的按钮点不动，正好也是对手回合该有的行为。
    template:
      '<button class="pw" :data-value="value" :data-readonly="readonly ? \'1\' : \'\'" :disabled="disabled"' +
      ' @click="$emit(\'update:value\', 0.6); $emit(\'shoot\')" />',
  },
  'el-avatar': { template: '<span class="el-avatar"><slot /></span>' },
  // 聊天输入框：Element Plus 没装（这一页其余部分没用到），给一个最小的 textarea。
  // 用 textarea 而不是 input：真实组件在 type="textarea" 时渲染的就是它，
  // 用例里的选择器与线上一致。
  'el-input': {
    props: ['modelValue'],
    emits: ['update:modelValue'],
    template:
      '<textarea class="el-input-ta" :value="modelValue"' +
      ' @input="$emit(\'update:modelValue\', $event.target.value)" />',
  },
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
    bot: -1,
    series: [0, 0],
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
  //
  // onDone 之后页面还有一段「帧尾 → 权威球位」的收尾补间（约 160~520ms，
  // 按 rAF 推进）。它不是同步的，所以这里要等它真的走完，
  // 否则压在后面的那一杆还没开始播，断言会看到一个半成品状态。
  const finishShot = async () => {
    const call = mocks.playCalls[mocks.playCalls.length - 1]
    call.options.onDone?.()
    await new Promise((resolve) => setTimeout(resolve, 560))
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
  // 有两条用例会接管定时器（聊天提示的 4 秒生命周期），
  // 上一条要是中途失败没交还，后面所有用例的 flushPromises 都会卡死。
  vi.useRealTimers()
  mocks.requestRematch.mockReset()
  mocks.options.length = 0
  mocks.sends.length = 0
  mocks.playCalls.length = 0
  mocks.aimArgs.length = 0
  mocks.chats.length = 0
  mocks.replace.mockReset()
  mocks.startBotMatch.mockReset()
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

  // 人机局：那边没有第二个人可等，点下去应当直接开一局新的，而不是
  // 去请求一个「对方也想再打」的表态（服务端会回一句「对手不在了」）。
  it('人机局点再来一局直接开新局', async () => {
    mocks.startBotMatch.mockResolvedValue({ match_id: 66, status: 'playing' })
    const { wrapper, push } = await mountPage()
    push({
      type: 'state',
      ts: 0,
      data: snapshot({ status: 'finished', winner: 0, win_reason: 'eight_legal', bot: 1 }),
    })
    await flushPromises()

    expect(wrapper.find('.rs__btn--primary').text()).toBe('再打一局')
    await wrapper.find('.rs__btn--primary').trigger('click')
    await flushPromises()

    expect(mocks.startBotMatch).toHaveBeenCalledTimes(1)
    expect(mocks.requestRematch).not.toHaveBeenCalled()
    expect(mocks.replace).toHaveBeenCalledWith('/pool/match/66')
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

  // 摆球时落点压在别的球上，落位要被顶开到相切——不然一松手就是
  // 「白球跟另一颗球叠在一起」，看着像渲染坏了，而它确实是个非法球位。
  it('落点压在别的球上时被推开，落位不会跟那颗球重叠', async () => {
    const { wrapper, push } = await mountPage()
    push({
      type: 'state',
      ts: 0,
      data: snapshot({
        ball_in_hand: true,
        balls: [
          { id: 'cue', x: 0.2, y: 0.1, z: -0.05, on_table: false },
          { id: '3', x: 0.5, y: 0.6, z: 0, on_table: true },
        ],
      }),
    })
    await flushPromises()

    // 点台面这一下正好落在 3 号球身上（壳固定发 0.5,0.6）。
    await wrapper.find('.tbl').trigger('click')
    await flushPromises()

    // 正好压在球心上，没有方向可推，约定往台面中线（+y）让开一个球径。
    const gap = Math.hypot(mocks.placeAt.x - 0.5, mocks.placeAt.y - 0.6)
    expect(gap).toBeCloseTo(BALL_RADIUS * 2, 4)
  })

  it('手机上拖动摆球：抬手那一下落位', async () => {    const { wrapper, push } = await mountPage()
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

  // 自己犯规不能顺手把「手中球」打开：快照里的轮次还是自己，
  // 一开就进摆球模式，而服务端已经把这一杆记成自己的犯规、手中球给了对手，
  // 一摆就是 ErrNotBallInHand（「这一杆不能自由摆放母球」）。
  it('自己犯规不会让界面以为拿到了手中球', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    push({
      type: 'foul',
      ts: 0,
      data: { code: 'cue_pocketed', reason: '母球落袋', ball_in_hand: true, seat: 0 },
    })
    await flushPromises()

    expect(wrapper.find('.pm__place').exists()).toBe(false)
    expect(wrapper.find('.pm__reopen').exists()).toBe(false)
  })

  // 对手犯规是另一条路：下一杆是我，foul 帧里的手中球要留住——
  // state 恰好丢了的时候它就是唯一来源。
  it('对手犯规时采纳 foul 帧里的手中球', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    push({
      type: 'foul',
      ts: 0,
      data: { code: 'no_contact', reason: '母球没有碰到任何球', ball_in_hand: true, seat: 1 },
    })
    await flushPromises()

    expect(wrapper.find('.pm__place').exists()).toBe(true)
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

// 加塞面板展开时，指针怎么动都不能改方向。
//
// 玩家的实际动作是：瞄好 → 点开面板调塞 → 点面板外面关掉。从面板挪到台面的
// 这一段路上鼠标会一路发悬停，真机上看到的就是「点外面关面板，杆跳了一下」。
// 这条用例锁住「开着不动」与「关掉之后的第一下也不动」。
describe('对局页的加塞面板', () => {
  it('面板展开时不跟随悬停，关掉之后的第一下也不会带偏方向', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    expect(wrapper.find('.sp__panel').exists()).toBe(false)
    await wrapper.find('.sp__toggle').trigger('click')
    expect(wrapper.find('.sp__panel').exists()).toBe(true)

    const before = wrapper.find('.tbl-wrap').attributes('data-phi')

    // 面板开着：悬停到 (0.9, 0.2) 一点都不能动。
    await wrapper.find('.tbl-hover').trigger('click')
    await flushPromises()
    expect(wrapper.find('.tbl-wrap').attributes('data-phi')).toBe(before)

    // 点面板外面：面板收起，而且那一下被吃掉，不再往下传。
    const outside = new Event('pointerdown', { bubbles: true, cancelable: true })
    document.body.dispatchEvent(outside)
    await flushPromises()
    expect(wrapper.find('.sp__panel').exists()).toBe(false)
    expect(outside.defaultPrevented).toBe(true)

    // 关掉之后指针还停在原地：第一下悬停吞掉，方向仍然不动。
    await wrapper.find('.tbl-hover').trigger('click')
    await flushPromises()
    expect(wrapper.find('.tbl-wrap').attributes('data-phi')).toBe(before)

    // 真的走开了才恢复跟随（母球在 0.5,0.5，指到 0.1,0.9）。
    await wrapper.find('.tbl-hover-far').trigger('click')
    await flushPromises()
    expect(Number(wrapper.find('.tbl-wrap').attributes('data-phi'))).toBeCloseTo(
      Math.atan2(0.4, -0.4),
      4,
    )
  })
})

describe('对局页的开球摆球', () => {  // 开球时服务端给的是「开球线后」的手中球：虚影能跟着指针走，
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

describe('连续对局的大比分', () => {
  it('顶栏按「我 : 对手」显示，座位 0 就是我', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ series: [3, 2] }) })
    await flushPromises()

    expect(wrapper.find('.pm__series').text().replace(/\s/g, '')).toBe('3:2')
  })

  it('坐在 1 号位时左右换过来', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ seat: 1, series: [3, 2] }) })
    await flushPromises()

    // 顶栏的规则是「我在左」，所以 1 号位看到的是 2:3。
    expect(wrapper.find('.pm__series').text().replace(/\s/g, '')).toBe('2:3')
  })

  it('结算帧带来的比分盖过旧快照', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ series: [3, 2] }) })
    await flushPromises()

    // 判负、离场这两条路径之后不会有新快照，比分只在结果帧里。
    push({
      type: 'result',
      ts: 0,
      data: { winner: 0, reason: 'leave', series: [4, 2], elo_delta_0: 0, elo_delta_1: 0 },
    })
    await flushPromises()

    expect(wrapper.find('.pm__series').text().replace(/\s/g, '')).toBe('4:2')
  })

  it('再约之后进入新的一局，旧比分让位给新快照', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ series: [3, 2] }) })
    await flushPromises()
    push({
      type: 'result',
      ts: 0,
      data: { winner: 0, reason: 'leave', series: [4, 2], elo_delta_0: 0, elo_delta_1: 0 },
    })
    await flushPromises()

    push({ type: 'state', ts: 0, data: snapshot({ series: [4, 2] }) })
    await flushPromises()

    expect(wrapper.find('.pm__series').text().replace(/\s/g, '')).toBe('4:2')
  })
})

describe('对手的力度与击球点', () => {
  it('对手回合时，右侧力度条显示他推到了哪', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ turn: 1 }) })
    await flushPromises()

    push({
      type: 'aim',
      ts: 0,
      data: { seat: 1, phi: 1.2, power: 0.66, a: 0.5, b: -0.3 },
    })
    await flushPromises()

    const slider = wrapper.findComponent({ name: 'PowerSlider' })
    expect(slider.props('value')).toBeCloseTo(0.66, 6)
    // 只读镜像：能看不能拖，也不压暗。
    expect(slider.props('readonly')).toBe(true)
  })

  it('对手的击球点跟着帧挪到球面上那个位置', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ turn: 1 }) })
    await flushPromises()

    push({
      type: 'aim',
      ts: 0,
      data: { seat: 1, phi: 1.2, power: 0.66, a: 0.5, b: -0.3 },
    })
    await flushPromises()

    // 0.5 的右塞、-0.3 的低杆 → 红点在球心右侧偏下。
    const dot = wrapper.find('.sp__mini-dot').element as HTMLElement
    expect(dot.style.left).toBe('73%')
    expect(parseFloat(dot.style.top)).toBeGreaterThan(50)
  })

  it('自己回合时这条显示自己的力度，不跟着对手的帧变', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    push({
      type: 'aim',
      ts: 0,
      data: { seat: 1, phi: 1.2, power: 0.66, a: 0.5, b: -0.3 },
    })
    await flushPromises()

    const slider = wrapper.findComponent({ name: 'PowerSlider' })
    expect(slider.props('value')).toBe(0)
    expect(slider.props('readonly')).toBe(false)
  })
})

describe('瞄准上报', () => {
  it('调加塞会把左右塞与高低杆一起报给服务端', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    mocks.aimArgs.length = 0
    // 直接改页面状态，等价于在面板上把红点拖过去。
    // script setup 的绑定挂在内部 setupState 上，wrapper.vm 的顶层拿不到。
    const setup = (wrapper.vm as unknown as { $: { setupState: Record<string, unknown> } }).$.setupState
    setup.spinA = 0.4
    setup.spinB = -0.2
    await flushPromises()
    // 上报有 60ms 节流，窗口内的最后一帧是挂定时器补发的，
    // 所以要真等一格（flushPromises 不推进真实定时器）。
    await new Promise((resolve) => setTimeout(resolve, 90))
    await flushPromises()

    const last = mocks.aimArgs[mocks.aimArgs.length - 1]
    expect(last).toBeDefined()
    expect(last[2]).toBeCloseTo(0.4, 6)
    expect(last[3]).toBeCloseTo(-0.2, 6)
  })
})

describe('对家的杆', () => {
  it('球还在滚的时候收到对面的瞄准帧，先不画他的杆', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ turn: 1 }) })
    await flushPromises()

    push({ type: 'shot', ts: 0, data: shotFrame(1) })
    await flushPromises()
    // 对面先播完了、已经开始瞄下一杆：这一帧在我这边球还在滚的时候就到了。
    push({ type: 'aim', ts: 0, data: { seat: 1, phi: 1.2, power: 0.5, a: 0, b: 0 } })
    await flushPromises()

    expect(wrapper.find('.tbl-wrap').attributes('data-peer')).toBe('')
  })

  it('这一杆播完，那根杆立刻出现', async () => {
    const { wrapper, push, finishShot } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ turn: 1 }) })
    await flushPromises()
    push({ type: 'shot', ts: 0, data: shotFrame(1) })
    await flushPromises()
    push({ type: 'aim', ts: 0, data: { seat: 1, phi: 1.2, power: 0.5, a: 0, b: 0 } })
    await flushPromises()

    await finishShot()

    // 帧早就收下了，动画一结束就得出来，不能等他再动一下鼠标。
    expect(wrapper.find('.tbl-wrap').attributes('data-peer')).toBe('1.2')
  })
})

describe('对局页的轨迹排队', () => {
  // 服务端会等这一杆在屏幕上播完再让机器人出杆，正常不会撞上。
  // 但客户端掉帧、标签页被降频时，下一杆的帧仍可能提前到；
  // 直接换动画会让两段动画同时写球位，球从半路跳到新起点。
  it('上一杆还在播时到的下一杆先排队，播完才起', async () => {
    const { push, finishShot } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot({ turn: 1 }) })
    await flushPromises()

    push({ type: 'shot', ts: 0, data: shotFrame(1) })
    await flushPromises()
    expect(mocks.playCalls).toHaveLength(1)

    push({ type: 'shot', ts: 0, data: shotFrame(1) })
    await flushPromises()
    expect(mocks.playCalls).toHaveLength(1)

    await finishShot()
    expect(mocks.playCalls).toHaveLength(2)
  })
})

describe('回放倍速', () => {
  it('自己出杆按 0.8 倍速播', async () => {
    const { push, shootOnce } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()
    await shootOnce()
    const call = mocks.playCalls[mocks.playCalls.length - 1]
    // 慢放这一档是给末段留的：1 倍速时球收得太利落，看着像被掐断。
    // 物理时间不受影响——playShot 回调出来的 elapsed 仍是真实秒数。
    expect(call.options.speed).toBeCloseTo(0.8, 6)
  })
})

describe('音效开关', () => {
  it('按一下静音，选择会记住', async () => {
    const { wrapper } = await mountPage()
    const button = wrapper.find('.pm__sound')
    expect(button.attributes('aria-pressed')).toBe('true')

    await button.trigger('click')
    expect(button.attributes('aria-pressed')).toBe('false')
    expect(localStorage.getItem('forum_sound')).toBe('off')

    await button.trigger('click')
    expect(localStorage.getItem('forum_sound')).toBe('on')
  })
})

// ===== 对局内聊天 =====
//
// 这里只验「消息进来之后界面怎么变」与「点发送之后上行发了什么」：
// 转发、限流、截断都在服务端的 TestRelayChat* 里验，两边各管一段。
describe('对局聊天', () => {
  // openChat 先灌一份快照（页面要靠它认出自己坐哪），再点气泡把面板打开。
  async function openChat() {
    const page = await mountPage()
    // 假 socket 不会真的连上，这里替服务端宣告一次「已连接」：
    // canChat 靠连接状态放行，不宣告的话发送按钮点了也没反应。
    const options = mocks.options[mocks.options.length - 1] as GameSocketOptions
    options.onState('open')
    page.push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()
    await page.wrapper.find('.pm__chat-btn').trigger('click')
    await flushPromises()
    return page
  }

  it('收到对面消息：面板收起时算未读，打开就能看见', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    push({ type: 'chat', ts: 0, data: { seat: 1, text: '别急', id: 1, ts: Date.now() } })
    await flushPromises()

    // 收起状态下不该弹面板，只在气泡上留一个未读点：
    // 玩家正在瞄准，球台上飞过一条横幅正好挡住他要看的地方。
    expect(wrapper.find('.pm__chat').exists()).toBe(false)
    expect(wrapper.find('.pm__chat-badge').text()).toBe('1')

    await wrapper.find('.pm__chat-btn').trigger('click')
    await flushPromises()
    const bubbles = wrapper.findAll('.pm__chat-bubble')
    expect(bubbles).toHaveLength(1)
    expect(bubbles[0].text()).toBe('别急')
    // 打开就把未读清掉，否则那颗红点会一直挂着。
    expect(wrapper.find('.pm__chat-badge').exists()).toBe(false)
  })

  it('自己发的消息立刻落进列表，不靠回音', async () => {
    const { wrapper } = await openChat()
    await wrapper.find('.el-input-ta').setValue('这杆稳')
    await wrapper.find('.pm__chat-send').trigger('click')
    await flushPromises()

    expect(mocks.chats).toEqual(['这杆稳'])
    const bubbles = wrapper.findAll('.pm__chat-bubble')
    expect(bubbles).toHaveLength(1)
    expect(bubbles[0].text()).toBe('这杆稳')
    // 自己那条靠右（is-mine 同时决定左右与配色）。
    expect(wrapper.find('.pm__chat-row').classes()).toContain('is-mine')
    // 发完要把输入框清空，否则同一句话会被连发两次。
    expect((wrapper.find('.el-input-ta').element as HTMLTextAreaElement).value).toBe('')
  })

  it('点表情直接发出去，纯表情按贴纸画', async () => {
    const { wrapper } = await openChat()
    await wrapper.find('.pm__chat-emoji').trigger('click')
    await flushPromises()

    expect(mocks.chats).toHaveLength(1)
    expect(wrapper.find('.pm__chat-bubble').classes()).toContain('is-sticker')
    // 头像旁飘的那条也要认得出是贴纸（放大画），不是一句话。
    expect(wrapper.find('.pm__toast').classes()).toContain('is-sticker')
  })

  it('空白输入时发送按钮是禁用的', async () => {
    const { wrapper } = await openChat()
    expect(wrapper.find('.pm__chat-send').attributes('disabled')).toBeDefined()

    await wrapper.find('.el-input-ta').setValue('   ')
    await flushPromises()
    expect(wrapper.find('.pm__chat-send').attributes('disabled')).toBeDefined()
  })

  it('回车直接发出去，Shift+回车仍然是换行', async () => {
    const { wrapper } = await openChat()
    const ta = wrapper.find('.el-input-ta')

    // 先确认 Shift+回车不发：长句子偶尔要分行，按回车只会插一个换行。
    await ta.setValue('第一行')
    await ta.trigger('keydown', { key: 'Enter', shiftKey: true })
    await flushPromises()
    expect(mocks.chats).toEqual([])

    // 普通回车就发，并且发完把框清空。
    await ta.trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(mocks.chats).toEqual(['第一行'])
    expect((ta.element as HTMLTextAreaElement).value).toBe('')
  })

  it('输入法拼字中的回车不当成发送', async () => {
    const { wrapper } = await openChat()
    const ta = wrapper.find('.el-input-ta')

    // 中文候选词还没上屏时按回车是「选这个字」。放行的话，
    // 打「好球」按回车上屏，飞出去的就只是「好」。
    await ta.setValue('hao')
    await ta.trigger('keydown', { key: 'Enter', isComposing: true })
    await flushPromises()
    expect(mocks.chats).toEqual([])
  })

  // 谁说的就挂谁那一侧的头像下面。
  //
  // 以前两边的气泡都挂在右上，人机局里就成了「我发的话从机器人头像下面冒出来」，
  // 读起来像它在说话。座位卡恒定「我在左上、对手在右上」，所以按侧分两处就够，
  // 不用看座位号。
  it('自己发的提示挂在自己头像下面', async () => {
    const { wrapper } = await openChat()
    await wrapper.find('.el-input-ta').setValue('我这句话')
    await wrapper.find('.pm__chat-send').trigger('click')
    await flushPromises()

    const left = wrapper.find('.pm__toasts--left')
    expect(left.exists()).toBe(true)
    expect(left.findAll('.pm__toast')).toHaveLength(1)
    expect(left.text()).toBe('我这句话')
    // 右侧那块不该因为我发了一句就出现。
    expect(wrapper.find('.pm__toasts:not(.pm__toasts--left)').exists()).toBe(false)
  })

  it('对手发的提示只挂在对面那一侧', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    push({ type: 'chat', ts: 0, data: { seat: 1, text: '换个角度', id: 1, ts: Date.now() } })
    await flushPromises()

    const right = wrapper.find('.pm__toasts:not(.pm__toasts--left)')
    expect(right.exists()).toBe(true)
    expect(right.text()).toBe('换个角度')
    expect(wrapper.find('.pm__toasts--left').exists()).toBe(false)
  })

  // 两侧各有自己的三条上限：对方连发表情时，我刚发的那句不该被顶掉。
  it('两侧提示互不挤占', async () => {
    const { wrapper } = await openChat()
    for (const text of ['一', '二', '三']) {
      await wrapper.find('.el-input-ta').setValue(text)
      await wrapper.find('.pm__chat-send').trigger('click')
      await flushPromises()
    }
    expect(wrapper.findAll('.pm__toasts--left .pm__toast')).toHaveLength(3)

    // 再发一条，自己这一侧挤掉最早的一句。
    await wrapper.find('.el-input-ta').setValue('四')
    await wrapper.find('.pm__chat-send').trigger('click')
    await flushPromises()
    const mine = wrapper.findAll('.pm__toasts--left .pm__toast').map((t) => t.text())
    expect(mine).toEqual(['二', '三', '四'])
  })

  it('对面发来的消息在对手头像下面飘一条', async () => {
    const { wrapper, push } = await mountPage()
    push({ type: 'state', ts: 0, data: snapshot() })
    await flushPromises()

    push({ type: 'chat', ts: 0, data: { seat: 1, text: '别急', id: 1, ts: Date.now() } })
    await flushPromises()

    // 面板是收起的：内容只能靠这条提示看到，不能只在气泡上亮个红点。
    const toasts = wrapper.findAll('.pm__toast')
    expect(toasts).toHaveLength(1)
    expect(toasts[0].text()).toBe('别急')
    // 对手发的那条不是 is-mine，配色要能区分（自己发的是台呢绿）。
    expect(toasts[0].classes()).not.toContain('is-mine')
    // 未读点照旧亮着：提示会走，红点留到打开面板为止。
    expect(wrapper.find('.pm__chat-badge').text()).toBe('1')
  })

  it('自己发的消息也飘一条，4 秒后自己撤掉', async () => {
    const { wrapper } = await openChat()
    // 只在这条用例里接管定时器：提示条的生命周期靠它推进。
    // 注意此后不能用 flushPromises——它内部是 setTimeout，被假定时器接管后就永远不 resolve，
    // 要等下一帧一律用 nextTick。
    vi.useFakeTimers()
    try {
      await wrapper.find('.el-input-ta').setValue('这杆稳')
      await wrapper.find('.pm__chat-send').trigger('click')

      const toasts = wrapper.findAll('.pm__toast')
      expect(toasts).toHaveLength(1)
      expect(toasts[0].text()).toBe('这杆稳')
      // 自己发的是绿底那款，和对面发来的深色分开。
      expect(toasts[0].classes()).toContain('is-mine')

      // 还没到点，仍然挂着。
      vi.advanceTimersByTime(3000)
      await nextTick()
      expect(wrapper.findAll('.pm__toast')).toHaveLength(1)

      vi.advanceTimersByTime(1100)
      await nextTick()
      expect(wrapper.findAll('.pm__toast')).toHaveLength(0)
    } finally {
      vi.useRealTimers()
    }
  })

  it('连着发好几条时只留最新三条', async () => {
    const { wrapper } = await openChat()
    vi.useFakeTimers()
    try {
      for (const text of ['一', '二', '三', '四']) {
        await wrapper.find('.el-input-ta').setValue(text)
        await wrapper.find('.pm__chat-send').trigger('click')
      }

      // 头像下面最多三条：连发表情时不能堆成一列把球台挡一半。
      const toasts = wrapper.findAll('.pm__toast')
      expect(toasts).toHaveLength(3)
      expect(toasts.map((t) => t.text())).toEqual(['二', '三', '四'])
    } finally {
      vi.useRealTimers()
    }
  })
})
