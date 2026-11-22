import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { PendingInvite } from '@/api/pool'

// pool store 管的是「邀请这一层」：谁能弹出来、处理完从列表里摘掉、对方接受后去哪。
// 接口与 WebSocket 都换成可编程的假实现，用例只关心状态怎么变。
//
// vi.mock 的工厂会被提升到文件顶部，工厂里引用普通 const 会报
// 「Cannot access before initialization」，所以假函数与记录表一律用 vi.hoisted 定义。
const mocks = vi.hoisted(() => {
  const frames = new Map<string, (data: unknown) => void>()
  const reconnects: Array<() => void> = []
  return {
    frames,
    reconnects,
    listPendingInvites: vi.fn(),
    createInvite: vi.fn(),
    acceptInvite: vi.fn(),
    declineInvite: vi.fn(),
    onLogout: vi.fn(),
    // frameRegistrations 记录 onFrame 被调用了几次。
    // 「注册两次会不会重复处理」不能靠推送结果去验：Map 按 key 存，
    // 第二次注册是覆盖而不是追加，推一条照样只处理一遍，断言永远绿。
    frameRegistrations: 0,
  }
})

vi.mock('@/api/pool', () => ({
  listPendingInvites: () => mocks.listPendingInvites(),
  createInvite: (id: number) => mocks.createInvite(id),
  acceptInvite: (id: string) => mocks.acceptInvite(id),
  declineInvite: (id: string) => mocks.declineInvite(id),
}))

vi.mock('@/utils/auth', () => ({
  onLogout: () => mocks.onLogout(),
}))

vi.mock('@/utils/ws', () => ({
  onFrame: (type: string, fn: (data: unknown) => void) => {
    mocks.frameRegistrations += 1
    mocks.frames.set(type, fn)
    return () => {
      mocks.frames.delete(type)
    }
  },
  onReconnect: (fn: () => void) => {
    mocks.reconnects.push(fn)
    return () => {}
  },
}))

const { usePoolStore } = await import('@/store/modules/pool')

// invite 造一条待接受的邀请。
function invite(id: string, fromId = 7, seconds = 60): PendingInvite {
  return {
    invite_id: id,
    match_id: 100,
    room_code: '654321',
    from: { user_id: fromId, nickname: '对手 ' + String(fromId), avatar: '' },
    expires_in: seconds,
  }
}

// fire 触发一条推送帧。
function fire(type: string, data: unknown) {
  const fn = mocks.frames.get(type)
  if (!fn) {
    throw new Error('没有注册 ' + type + ' 的处理函数')
  }
  fn(data)
}

beforeEach(() => {
  setActivePinia(createPinia())
  mocks.frames.clear()
  mocks.reconnects.length = 0
  mocks.listPendingInvites.mockReset()
  mocks.createInvite.mockReset()
  mocks.acceptInvite.mockReset()
  mocks.declineInvite.mockReset()
  mocks.onLogout.mockReset()
  mocks.frameRegistrations = 0
})

describe('pool store 的邀请推送', () => {
  it('四个推送帧各注册一次，重复调用不会注册两遍', () => {
    const store = usePoolStore()
    store.initWS()
    store.initWS()

    // 注册次数与「注册了哪几个」都要验：
    // 只数次数的话，把 pool_invite_declined 换成 pool_invite_xxx 也照样是 4。
    expect(mocks.frameRegistrations).toBe(4)
    expect([...mocks.frames.keys()].sort()).toEqual([
      'pool_invite',
      'pool_invite_accepted',
      'pool_invite_declined',
      'pool_room_joined',
    ])
  })

  // 有人进我的房间：roomTick 要涨一次，大厅据此立刻重拉快照。
  //
  // 这是「只有自己时大厅可以十秒才轮询一次」的前提：慢轮询 + 推送补位，
  // 对面进人照样是秒级可见。roomTick 不涨的话，对面进来后大厅最多要等十秒。
  it('有人进房时把 roomTick 加一', () => {
    const store = usePoolStore()
    store.initWS()
    expect(store.roomTick).toBe(0)

    fire('pool_room_joined', {
      match_id: 5,
      room_code: '123456',
      player: { user_id: 8, nickname: '小满', avatar: '' },
    })
    fire('pool_room_joined', {
      match_id: 5,
      room_code: '123456',
      player: { user_id: 9, nickname: '陆陆', avatar: '' },
    })

    expect(store.roomTick).toBe(2)
  })

  // expires_in 是「服务端发这一帧时还剩多少秒」，必须换成本地截止时刻：
  // 排在后面的邀请轮到时早已过时，按剩余量起表会显示一个假的倒计时。
  it('入库时把剩余秒数换成本地截止时刻', () => {
    const store = usePoolStore()
    store.initWS()

    const before = Date.now()
    fire('pool_invite', invite('a', 7, 30))

    const first = store.pending[0]
    expect(first.expires_at).toBeGreaterThanOrEqual(before + 30 * 1000)
    expect(first.expires_at).toBeLessThanOrEqual(Date.now() + 30 * 1000)
  })

  it('同一条邀请被推两次只留一条', () => {
    const store = usePoolStore()
    store.initWS()

    fire('pool_invite', invite('a'))
    fire('pool_invite', invite('a'))

    expect(store.pending).toHaveLength(1)
  })

  it('新邀请插到最前面，先弹最新那条', () => {
    const store = usePoolStore()
    store.initWS()

    fire('pool_invite', invite('a'))
    fire('pool_invite', invite('b'))

    expect(store.incoming?.invite_id).toBe('b')
  })

  it('忽略过的邀请不再弹，但仍在列表里', () => {
    const store = usePoolStore()
    store.initWS()
    fire('pool_invite', invite('a'))
    fire('pool_invite', invite('b'))

    store.dismiss('b')

    expect(store.incoming?.invite_id).toBe('a')
    expect(store.pending).toHaveLength(2)
  })

  it('全部忽略之后没有可弹的邀请', () => {
    const store = usePoolStore()
    store.initWS()
    fire('pool_invite', invite('a'))

    store.dismiss('a')

    expect(store.incoming).toBeNull()
  })
})

describe('pool store 的邀请处理', () => {
  it('接受成功后从列表里摘掉，并返回房间信息', async () => {
    const store = usePoolStore()
    store.initWS()
    fire('pool_invite', invite('a'))
    mocks.acceptInvite.mockResolvedValue({ match_id: 100, room_code: '654321' })

    const room = await store.accept('a')

    expect(room.match_id).toBe(100)
    expect(store.pending).toHaveLength(0)
  })

  it('拒绝接口失败也把本地这条去掉', async () => {
    const store = usePoolStore()
    store.initWS()
    fire('pool_invite', invite('a'))
    mocks.declineInvite.mockRejectedValue(new Error('network'))

    await store.decline('a')

    expect(store.pending).toHaveLength(0)
  })

  it('invite 成功后记住自己发出的那条、发给了谁，以及对方叫什么', async () => {
    const store = usePoolStore()
    mocks.createInvite.mockResolvedValue({
      invite_id: 'x',
      match_id: 200,
      room_code: '111222',
      expires_in: 60,
    })

    await store.invite(9, '小满')

    expect(mocks.createInvite).toHaveBeenCalledWith(9)
    expect(store.outgoing?.room_code).toBe('111222')
    // 响应里没有被邀请者的 id，大厅要靠 outgoingTo 把那一行标成「已发送」。
    expect(store.outgoingTo).toBe(9)
    // 昵称也一起记下来：回大厅时在线名单还没拉回来，
    // 现查的话等待卡会先显示成「已邀请 对方」。
    expect(store.outgoingToName).toBe('小满')
    expect(store.outgoing?.expires_at).toBeGreaterThan(Date.now())
  })

  it('点了不等之后，等待卡与「发给谁」一起清掉', async () => {
    const store = usePoolStore()
    mocks.createInvite.mockResolvedValue({
      invite_id: 'x',
      match_id: 200,
      room_code: '111222',
      expires_in: 60,
    })
    await store.invite(9)

    store.cancelOutgoing()

    expect(store.outgoing).toBeNull()
    // 不清的话大厅那一行会一直是「已发送」并禁用，用户再也约不到这个人。
    expect(store.outgoingTo).toBe(0)
    expect(store.outgoingToName).toBe('')
  })
})

describe('pool store 的实时结果', () => {
  it('对方接受后清掉等待卡，并给出跳转信号', () => {
    const store = usePoolStore()
    store.initWS()
    store.outgoing = { invite_id: 'x', match_id: 200, room_code: '111222', expires_in: 60, expires_at: Date.now() + 60000 }

    fire('pool_invite_accepted', { match_id: 200, room_code: '111222' })

    expect(store.outgoing).toBeNull()
    expect(store.acceptedRoom?.match_id).toBe(200)
  })

  it('对方拒绝后清掉等待卡并留一句话', () => {
    const store = usePoolStore()
    store.initWS()
    store.outgoing = { invite_id: 'x', match_id: 200, room_code: '111222', expires_in: 60, expires_at: Date.now() + 60000 }

    fire('pool_invite_declined', { match_id: 200, room_code: '111222' })

    expect(store.outgoing).toBeNull()
    expect(store.takeNotice()).toBe('对方拒绝了这次邀请')
    // 取走即清空，同一句话不该弹第二次。
    expect(store.takeNotice()).toBe('')
  })
})

describe('pool store 的刷新与清理', () => {
  it('refresh 用接口结果整份替换列表', async () => {
    const store = usePoolStore()
    mocks.listPendingInvites.mockResolvedValue([invite('a'), invite('b')])

    await store.refresh()

    expect(store.pending).toHaveLength(2)
  })

  it('refresh 失败时保留上一次的列表', async () => {
    const store = usePoolStore()
    mocks.listPendingInvites.mockResolvedValueOnce([invite('a')])
    await store.refresh()

    mocks.listPendingInvites.mockRejectedValueOnce(new Error('boom'))
    await store.refresh()

    expect(store.pending).toHaveLength(1)
  })

  it('重连成功后补拉一次，断线期间的邀请不会永久丢掉', async () => {
    const store = usePoolStore()
    store.initWS()
    mocks.listPendingInvites.mockResolvedValue([invite('a')])

    for (const fn of mocks.reconnects) {
      fn()
    }
    // 补拉是异步的，等一轮微任务。
    await Promise.resolve()

    expect(mocks.listPendingInvites).toHaveBeenCalled()
  })

  it('reset 清空全部状态', () => {
    const store = usePoolStore()
    store.initWS()
    fire('pool_invite', invite('a'))
    store.outgoing = { invite_id: 'x', match_id: 200, room_code: '111222', expires_in: 60, expires_at: Date.now() + 60000 }
    store.outgoingTo = 9
    store.acceptedRoom = { match_id: 200, room_code: '111222' }

    store.reset()

    expect(store.pending).toHaveLength(0)
    expect(store.incoming).toBeNull()
    expect(store.outgoing).toBeNull()
    expect(store.outgoingTo).toBe(0)
    expect(store.acceptedRoom).toBeNull()
  })
})
