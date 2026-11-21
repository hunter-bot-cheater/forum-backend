import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { decode, GameSocket, type DownMessage } from '@/game/protocol'

// 对局连接的用例全部跑在假 WebSocket 上。
//
// 不连真服务端：这里要验的是「信封拼得对不对、认不出的帧会不会把连接搞死、
// 断线几次后会不会放弃」，这些都只和客户端逻辑有关，
// 真连一次反而要等握手、又测不到重连路径。

// FakeWS 是最小可编程的 WebSocket。
// 只实现 GameSocket 用到的四个字段与两个方法，别的一律不提供：
// 多实现的接口会让人误以为那些行为也被测到了。
class FakeWS {
  static OPEN = 1
  static CONNECTING = 0
  static CLOSED = 3

  static instances: FakeWS[] = []

  url: string
  readyState = FakeWS.CONNECTING
  sent: string[] = []
  onopen: (() => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null
  // onclose 必须带 code：真实的 CloseEvent 永远有 code，
  // 不传的话被测代码读 event.code 会炸，测出来的就不是它的行为。
  onclose: ((event: { code: number }) => void) | null = null
  onerror: (() => void) | null = null

  constructor(url: string) {
    this.url = url
    FakeWS.instances.push(this)
  }

  send(data: string) {
    this.sent.push(data)
  }

  close() {
    this.readyState = FakeWS.CLOSED
    if (this.onclose) {
      this.onclose({ code: 1000 })
    }
  }

  // open 让连接变成可用状态，之后 send 才有效。
  open() {
    this.readyState = FakeWS.OPEN
    if (this.onopen) {
      this.onopen()
    }
  }

  // serverClose 模拟服务端断开（手动 close 之外的那条路径）。
  // 不传 code 时按 1006（异常关闭）算，这是拔网线最常见的那种。
  serverClose(code = 1006) {
    this.readyState = FakeWS.CLOSED
    if (this.onclose) {
      this.onclose({ code })
    }
  }

  // receive 推一帧下行数据。
  receive(data: unknown) {
    if (this.onmessage) {
      this.onmessage({ data })
    }
  }
}

// last 取最近一条发出的帧并解成对象，方便断言字段。
function last(ws: FakeWS): { type: string; seq: number; data: Record<string, unknown> } {
  return JSON.parse(ws.sent[ws.sent.length - 1])
}

// newSocket 建一个带记录回调的连接。
function newSocket(tokenValue = 'tok') {
  const messages: DownMessage[] = []
  const states: string[] = []
  const fatals: number[] = []
  const socket = new GameSocket({
    matchId: 42,
    token: () => tokenValue,
    onMessage: (msg) => messages.push(msg),
    onState: (state) => states.push(state),
    onFatal: (code) => fatals.push(code),
  })
  return { socket, messages, states, fatals }
}

beforeEach(() => {
  FakeWS.instances = []
  vi.stubGlobal('WebSocket', FakeWS)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('decode', () => {
  it('非字符串一律丢弃', () => {
    expect(decode(new ArrayBuffer(4))).toBeNull()
    expect(decode(undefined)).toBeNull()
  })

  it('坏 JSON 丢弃而不是抛异常', () => {
    expect(decode('{oops')).toBeNull()
  })

  it('没有 type 字段的帧丢弃', () => {
    expect(decode(JSON.stringify({ data: {} }))).toBeNull()
  })

  it('认不出的类型丢弃，老客户端不该因为服务端加了新消息就断连', () => {
    expect(decode(JSON.stringify({ type: 'emote', data: {} }))).toBeNull()
  })

  it('认识的类型原样返回，data 不做任何加工', () => {
    const msg = decode(JSON.stringify({ type: 'turn', ts: 1, data: { seat: 1, deadline_ms: 40000 } }))
    expect(msg).not.toBeNull()
    expect(msg?.type).toBe('turn')
  })
})

describe('GameSocket', () => {
  it('连接地址带 token 与 match_id', () => {
    const { socket } = newSocket()
    socket.connect()

    expect(FakeWS.instances).toHaveLength(1)
    const url = FakeWS.instances[0].url
    expect(url).toContain('/ws/game')
    expect(url).toContain('token=tok')
    expect(url).toContain('match_id=42')
  })

  it('重复 connect 不会建第二条连接', () => {
    const { socket } = newSocket()
    socket.connect()
    socket.connect()

    expect(FakeWS.instances).toHaveLength(1)
  })

  it('连接没打开时出杆返回 0 且不发帧', () => {
    const { socket } = newSocket()
    socket.connect()

    const seq = socket.shoot({ phi: 0, power: 0.5, theta: 0, a: 0, b: 0 })

    expect(seq).toBe(0)
    expect(FakeWS.instances[0].sent).toHaveLength(0)
  })

  it('出杆发的是 shoot 帧，五个参数齐全，seq 从 1 递增', () => {
    const { socket } = newSocket()
    socket.connect()
    const ws = FakeWS.instances[0]
    ws.open()

    const first = socket.shoot({ phi: Math.PI / 2, power: 0.8, theta: 0.02, a: 0.1, b: -0.2 })
    const second = socket.shoot({ phi: 0, power: 0.1, theta: 0, a: 0, b: 0 })

    expect(first).toBe(1)
    expect(second).toBe(2)

    const frame = JSON.parse(ws.sent[0])
    expect(frame.type).toBe('shoot')
    expect(frame.seq).toBe(1)
    expect(frame.data.phi).toBeCloseTo(Math.PI / 2, 6)
    expect(frame.data.power).toBe(0.8)
    expect(frame.data.theta).toBe(0.02)
    expect(frame.data.a).toBe(0.1)
    expect(frame.data.b).toBe(-0.2)
  })

  it('落位母球发的是 place_cue 帧', () => {
    const { socket } = newSocket()
    socket.connect()
    const ws = FakeWS.instances[0]
    ws.open()

    socket.placeCue(0.5, 1.2)

    const frame = last(ws)
    expect(frame.type).toBe('place_cue')
    expect(frame.data.x).toBe(0.5)
    expect(frame.data.y).toBe(1.2)
  })

  it('收到的帧交给 onMessage', () => {
    const { socket, messages } = newSocket()
    socket.connect()
    const ws = FakeWS.instances[0]
    ws.open()

    ws.receive(JSON.stringify({ type: 'peer', ts: 1, data: { event: 'left', seat: 0 } }))

    expect(messages).toHaveLength(1)
    expect(messages[0].type).toBe('peer')
  })

  it('收到脏帧不断连', () => {
    const { socket, messages, states } = newSocket()
    socket.connect()
    const ws = FakeWS.instances[0]
    ws.open()

    ws.receive('not json')
    ws.receive(JSON.stringify({ type: 'turn', ts: 1, data: { seat: 1, deadline_ms: 0 } }))

    expect(messages).toHaveLength(1)
    expect(states).toContain('open')
  })

  it('服务端断开后按退避重连', () => {
    vi.useFakeTimers()
    const { socket } = newSocket()
    socket.connect()
    FakeWS.instances[0].open()
    FakeWS.instances[0].serverClose()

    expect(FakeWS.instances).toHaveLength(1)
    vi.advanceTimersByTime(2000)
    expect(FakeWS.instances).toHaveLength(2)
  })

  it('连续重连失败后放弃，把状态报成 gaveup', () => {
    vi.useFakeTimers()
    const { socket, states } = newSocket()
    socket.connect()

    // 每次断开都会排下一次重连，轮数按上限（8 次）再多跑两轮，确保走到放弃。
    for (let i = 0; i < 10; i += 1) {
      FakeWS.instances[FakeWS.instances.length - 1].serverClose()
      vi.advanceTimersByTime(20000)
    }

    expect(states[states.length - 1]).toBe('gaveup')
  })

  // 断网半分钟再回来，不该要求用户做任何操作。
  it('重连窗口盖得住三十秒断网', () => {
    vi.useFakeTimers()
    const { socket, states } = newSocket()
    socket.connect()
    FakeWS.instances[0].open()
    FakeWS.instances[0].serverClose()

    // 三十秒里一直是「连不上又断」：每次握手失败后都还能再排下一轮。
    let elapsed = 0
    while (elapsed < 30000) {
      vi.advanceTimersByTime(1000)
      elapsed += 1000
      const latest = FakeWS.instances[FakeWS.instances.length - 1]
      if (latest.readyState === FakeWS.CONNECTING) {
        latest.serverClose()
      }
    }

    expect(states[states.length - 1]).not.toBe('gaveup')
  })

  it('重连时重新取一次 token，不沿用建连时那个', () => {
    vi.useFakeTimers()
    // 第一次用旧 token，之后换成新的：真实场景是 access 过期后被刷新换掉了。
    let current = 'old'
    const states: string[] = []
    const socket = new GameSocket({
      matchId: 42,
      token: () => current,
      onMessage: () => {},
      onState: (state) => states.push(state),
    })
    socket.connect()
    expect(FakeWS.instances[0].url).toContain('token=old')

    FakeWS.instances[0].open()
    current = 'new'
    FakeWS.instances[0].serverClose()
    vi.advanceTimersByTime(2000)

    expect(FakeWS.instances).toHaveLength(2)
    expect(FakeWS.instances[1].url).toContain('token=new')
  })

  it('token 取不到时不再重连，直接宣告放弃', () => {
    const states: string[] = []
    const socket = new GameSocket({
      matchId: 42,
      token: () => '',
      onMessage: () => {},
      onState: (state) => states.push(state),
    })
    socket.connect()

    expect(FakeWS.instances).toHaveLength(0)
    expect(states[states.length - 1]).toBe('gaveup')
  })

  it('主动 close 之后不再重连', () => {
    vi.useFakeTimers()
    const { socket } = newSocket()
    socket.connect()
    const ws = FakeWS.instances[0]
    ws.open()

    socket.close()
    vi.advanceTimersByTime(20000)

    expect(FakeWS.instances).toHaveLength(1)
  })

  // close 会把 manualClose 置位（不再自动重连），重新 connect 必须把它解掉，
  // 否则「离开页面又回来」的连接是一次性的，第二次断线就永远连不回去。
  it('close 之后再 connect 能重新建一条连接', () => {
    const { socket } = newSocket()
    socket.connect()
    FakeWS.instances[0].open()
    socket.close()

    socket.connect()

    expect(FakeWS.instances).toHaveLength(2)
  })

  // gaveup 之后的人工重试必须是一次全新计数。
  // 不复位的话第一次握手失败就会立刻再判 gaveup，页面再也连不回来。
  it('gaveup 之后再 connect 不会立刻又放弃', () => {
    vi.useFakeTimers()
    const { socket, states } = newSocket()
    socket.connect()
    for (let i = 0; i < 10; i += 1) {
      FakeWS.instances[FakeWS.instances.length - 1].serverClose()
      vi.advanceTimersByTime(20000)
    }
    expect(states[states.length - 1]).toBe('gaveup')
    const before = FakeWS.instances.length

    socket.connect()
    expect(FakeWS.instances).toHaveLength(before + 1)

    FakeWS.instances[before].serverClose()
    vi.advanceTimersByTime(2000)

    expect(FakeWS.instances).toHaveLength(before + 2)
    expect(states[states.length - 1]).not.toBe('gaveup')
  })

  // 4001 是服务端在握手之后才说得清的那一类拒绝（观战名额被抢）。
  // 这种情况重连必然还是同一个结果，必须直接停：不然观战页会对着
  // 一个永远进不去的房间空转完 47 秒的退避窗口。
  it('服务端用 4001 关门时直接放弃，并说明原因', () => {
    vi.useFakeTimers()
    const { socket, states, fatals } = newSocket()
    socket.connect()
    FakeWS.instances[0].serverClose(4001)
    vi.advanceTimersByTime(20000)

    expect(fatals).toEqual([4001])
    expect(states[states.length - 1]).toBe('gaveup')
    expect(FakeWS.instances).toHaveLength(1)
  })

  // 退避重连走的仍是 1006 那条路，别把业务关闭码的判断写成兜底分支。
  it('普通断线（1006）照旧退避重连，不算致命', () => {
    vi.useFakeTimers()
    const { socket, fatals } = newSocket()
    socket.connect()
    FakeWS.instances[0].open()
    FakeWS.instances[0].serverClose(1006)
    vi.advanceTimersByTime(2000)

    expect(fatals).toEqual([])
    expect(FakeWS.instances).toHaveLength(2)
  })
})
