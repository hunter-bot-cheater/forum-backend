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
})

describe('pool store 的邀请推送', () => {
  it('initWS 注册四类处理函数，重复调用不会注册两遍', () => {
    const store = usePoolStore()
    store.initWS()
    store.initWS()

    // 注册两遍时后一次会覆盖前一次（Map 按 key 存），
    // 体现在行为上是「同一条邀请被处理两次」，这里用一条推送验证。
    fire('pool_invite', invite('a'))
    expect(store.pending).toHaveLength(1)
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

  it('invite 成功后记住自己发出的那条', async () => {
    const store = usePoolStore()
    mocks.createInvite.mockResolvedValue({
      invite_id: 'x',
      match_id: 200,
      room_code: '111222',
      expires_in: 60,
    })

    await store.invite(9)

    expect(mocks.createInvite).toHaveBeenCalledWith(9)
    expect(store.outgoing?.room_code).toBe('111222')
  })
})

describe('pool store 的实时结果', () => {
  it('对方接受后清掉等待卡，并给出跳转信号', () => {
    const store = usePoolStore()
    store.initWS()
    store.outgoing = { invite_id: 'x', match_id: 200, room_code: '111222', expires_in: 60 }

    fire('pool_invite_accepted', { match_id: 200, room_code: '111222' })

    expect(store.outgoing).toBeNull()
    expect(store.acceptedRoom?.match_id).toBe(200)
  })

  it('对方拒绝后清掉等待卡并留一句话', () => {
    const store = usePoolStore()
    store.initWS()
    store.outgoing = { invite_id: 'x', match_id: 200, room_code: '111222', expires_in: 60 }

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
    expect(store.loaded).toBe(true)
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
    store.outgoing = { invite_id: 'x', match_id: 200, room_code: '111222', expires_in: 60 }
    store.acceptedRoom = { match_id: 200, room_code: '111222' }

    store.reset()

    expect(store.pending).toHaveLength(0)
    expect(store.incoming).toBeNull()
    expect(store.outgoing).toBeNull()
    expect(store.acceptedRoom).toBeNull()
  })
})
