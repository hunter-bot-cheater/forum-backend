import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { PoolRoom } from '@/api/pool'

// 房间页的用例集中在三件事上：
//   1. 快照来了怎么摆（自己永远在左、准备状态在两张卡上）；
//   2. 点准备会不会真的发请求并把返回的状态换上去；
//   3. 轮询看到 playing 会不会跳对局页、看到房间没了会不会停手。
// WebSocket 与它无关（房间页不连 WS），所以这里不需要任何帧相关的假实现。

const mocks = vi.hoisted(() => ({
  getRoom: vi.fn(),
  setReady: vi.fn(),
  replace: vi.fn(),
}))

vi.mock('@/api/pool', () => ({
  getRoom: (id: number | string) => mocks.getRoom(id),
  setReady: (id: number | string, ready: boolean) => mocks.setReady(id, ready),
}))

vi.mock('vue-router', async (importOriginal) => {
  // 只替换 useRouter：组件里没有用到别的导出，
  // 但整体替换会让 vue-router 的其它导出消失，以后加一行 useRoute 就炸。
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    useRouter: () => ({ replace: mocks.replace, push: mocks.replace }),
  }
})

const RoomPage = (await import('@/views/pool/room.vue')).default

// room 造一份房间快照。默认是「我坐 0 号、双方都还没准备」。
//
// 注意双方资料在房间建立时就已经齐了：陌生人是被 403 挡在外面的，
// 所以快照里不存在「对手资料为空」这种状态，用例也不该照着它写。
function room(patch: Partial<PoolRoom> = {}): PoolRoom {
  const base: PoolRoom = {
    match_id: 7,
    room_code: '123456',
    status: 'waiting',
    seat: 0,
    ready: [false, false],
    players: [
      { user_id: 1, nickname: '甲', avatar: '' },
      { user_id: 2, nickname: '乙', avatar: '' },
    ],
  }
  return { ...base, ...patch }
}

// el-avatar 只是容器，用最小替身：这里不验 UI 库的行为。
const stubs = {
  'el-avatar': { template: '<span class="el-avatar"><slot /></span>' },
}

async function mountPage(initial: PoolRoom) {
  mocks.getRoom.mockResolvedValue(initial)
  const pinia = createPinia()
  setActivePinia(pinia)

  const wrapper = mount(RoomPage, {
    props: { id: '7' },
    global: { plugins: [pinia], stubs },
  })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  mocks.getRoom.mockReset()
  mocks.setReady.mockReset()
  mocks.replace.mockReset()
})

afterEach(() => {
  // 假时钟必须在每条用例后还原，否则后面的事件与 flushPromises 都会卡住。
  vi.useRealTimers()
})

describe('房间页', () => {
  it('显示房号与双方座位，都没准备时提示双方各点一次', async () => {
    const wrapper = await mountPage(room())

    expect(wrapper.find('.pr__code-value').text()).toBe('123456')
    const seats = wrapper.findAll('.pr__seat')
    expect(seats).toHaveLength(2)
    // 自己永远在左，不管服务端分配的座位号是 0 还是 1。
    expect(seats[0].classes()).toContain('is-me')
    expect(seats[0].text()).toContain('甲')
    expect(wrapper.find('.pr__hint').text()).toContain('双方都点准备')
  })

  it('座位号是 1 时自己同样排在左边，准备标记也跟着座位走', async () => {
    // ready 故意反过来：只有座位 1（我）准备了，座位 0（房主）没准备。
    // 如果代码按「界面上的第几项」去取 ready，这一条就会红——
    // 只验名字的话，把 ready 的下标写错也看不出来。
    const wrapper = await mountPage(
      room({
        seat: 1,
        ready: [false, true],
        players: [
          { user_id: 9, nickname: '房主', avatar: '' },
          { user_id: 1, nickname: '甲', avatar: '' },
        ],
      }),
    )

    const seats = wrapper.findAll('.pr__seat')
    expect(seats[0].classes()).toContain('is-me')
    expect(seats[0].text()).toContain('甲')
    expect(seats[0].classes()).toContain('is-ready')
    expect(seats[1].text()).toContain('房主')
    expect(seats[1].classes()).not.toContain('is-ready')
  })

  it('准备状态画在对应那一侧', async () => {
    const wrapper = await mountPage(
      room({
        ready: [false, true],
        players: [
          { user_id: 1, nickname: '甲', avatar: '' },
          { user_id: 2, nickname: '乙', avatar: '' },
        ],
      }),
    )

    const seats = wrapper.findAll('.pr__seat')
    expect(seats[0].classes()).not.toContain('is-ready')
    expect(seats[1].classes()).toContain('is-ready')
    expect(wrapper.find('.pr__hint').text()).toContain('对手已经准备好了')
  })

  it('点准备把请求发出去，并按返回的状态更新界面', async () => {
    const wrapper = await mountPage(
      room({
        players: [
          { user_id: 1, nickname: '甲', avatar: '' },
          { user_id: 2, nickname: '乙', avatar: '' },
        ],
      }),
    )
    mocks.setReady.mockResolvedValue(
      room({
        ready: [true, false],
        players: [
          { user_id: 1, nickname: '甲', avatar: '' },
          { user_id: 2, nickname: '乙', avatar: '' },
        ],
      }),
    )

    await wrapper.find('.pr__ready').trigger('click')
    await flushPromises()

    expect(mocks.setReady).toHaveBeenCalledWith('7', true)
    expect(wrapper.find('.pr__ready').text()).toBe('取消准备')
    expect(wrapper.findAll('.pr__seat')[0].classes()).toContain('is-ready')
  })

  it('返回的是 playing 时直接跳对局页，不再停在房间', async () => {
    await mountPage(room({ status: 'playing' }))

    expect(mocks.replace).toHaveBeenCalledWith('/pool/match/7')
  })

  it('房间不存在时停掉轮询并显示说明', async () => {
    // 用假时钟把「后续几轮轮询」也跑出来：不推进时间的话，
    // 就算停表逻辑坏了，这条用例也会因为「还没来得及发第二次」而通过。
    vi.useFakeTimers()
    mocks.getRoom.mockRejectedValue({ response: { status: 404 } })
    const pinia = createPinia()
    setActivePinia(pinia)

    const wrapper = mount(RoomPage, {
      props: { id: '7' },
      global: { plugins: [pinia], stubs },
    })
    await vi.advanceTimersByTimeAsync(5000)

    expect(wrapper.find('.pr__error-text').text()).toContain('解散')
    expect(mocks.getRoom).toHaveBeenCalledTimes(1)
  })

  it('房间还在时会一直轮询，对方的准备状态能自己出现', async () => {
    vi.useFakeTimers()
    mocks.getRoom
      .mockResolvedValueOnce(
        room({
          players: [
            { user_id: 1, nickname: '甲', avatar: '' },
            { user_id: 2, nickname: '乙', avatar: '' },
          ],
        }),
      )
      .mockResolvedValue(
        room({
          ready: [false, true],
          players: [
            { user_id: 1, nickname: '甲', avatar: '' },
            { user_id: 2, nickname: '乙', avatar: '' },
          ],
        }),
      )
    const pinia = createPinia()
    setActivePinia(pinia)

    const wrapper = mount(RoomPage, {
      props: { id: '7' },
      global: { plugins: [pinia], stubs },
    })
    await vi.advanceTimersByTimeAsync(2000)

    expect(mocks.getRoom.mock.calls.length).toBeGreaterThan(1)
    expect(wrapper.findAll('.pr__seat')[1].classes()).toContain('is-ready')
  })
})
