import type { MatchSnapshot, Seat, ShotParams } from './types'
import type { ShotFrame } from './animator'
import { wsURL } from '@/utils/ws'

// 对局 WebSocket 的客户端。
//
// 与 utils/ws.ts 的通知连接是两条独立的连接，不共用：
//   - 通知是「一个用户一条」，跟人在哪个页面无关；
//   - 对局是「一个页面一局」，地址里带 match_id，进房与退房都要新建/关闭。
// 混在一条连接上的话，切换对局要复用连接并改订阅，出错时很难说清是哪一层的问题。

// ===== 下行消息 =====
//
// 字段与后端 internal/pool/protocol.go 逐字对齐。
// 这里不做校验也不补默认值：服务端是唯一权威，缺字段就是协议不一致，
// 应该在开发期立刻报出来，而不是被一层宽松解析吞掉。

export interface JoinedData {
  match_id: number
  room_code: string
  seat: Seat
  practice: boolean
}

export interface TurnData {
  seat: Seat
  deadline_ms: number
}

// ShotEvent 是一条击球事件。type 是已归类的四类之一
// （stick_ball / ball_ball / cushion / ball_pocket），
// a / b 是参与的两个球号，非球球事件里 b 为空串。
export interface ShotEvent {
  t: number
  type: string
  a: string
  b: string
}

// ShotData 是一杆的结果。frames 直接就是 animator 要的形状：
// [球号, x, y, z] 的混合数组，服务端原样透传 pool-sim 的输出。
export interface ShotData {
  seq: number
  seat: Seat
  params: ShotParams
  duration: number
  events: ShotEvent[]
  frames: ShotFrame[]
  final: Array<{ id: string; x: number; y: number; z: number; on_table: boolean }>
  sim_ms: number
}

export interface FoulData {
  code: string
  reason: string
  ball_in_hand: boolean
  seat: Seat
}

export interface ResultData {
  winner: Seat
  reason: string
  elo_delta_0: number
  elo_delta_1: number
}

export interface PeerData {
  event: 'joined' | 'left' | 'offline'
  seat: Seat
}

export interface ErrorData {
  code: string
  message: string
}

// DownMessage 是下行信封的联合类型，按 type 收窄。
// 用联合而不是「一堆可选字段」：页面里写 switch (msg.type) 就能拿到对应 data，
// 写错分支名编译期就报。
export type DownMessage =
  | { type: 'joined'; seq?: number; ts: number; data: JoinedData }
  | { type: 'state'; seq?: number; ts: number; data: MatchSnapshot }
  | { type: 'turn'; seq?: number; ts: number; data: TurnData }
  | { type: 'shot'; seq?: number; ts: number; data: ShotData }
  | { type: 'foul'; seq?: number; ts: number; data: FoulData }
  | { type: 'result'; seq?: number; ts: number; data: ResultData }
  | { type: 'peer'; seq?: number; ts: number; data: PeerData }
  | { type: 'error'; seq?: number; ts: number; data: ErrorData }

// ConnectionState 是连接状态，页面据此渲染顶部提示条。
// gaveup 与 closed 分开：closed 还会自动重连，gaveup 不会。
export type ConnectionState = 'connecting' | 'open' | 'closed' | 'gaveup'

export interface GameSocketOptions {
  matchId: number | string
  token: string
  // onMessage 每收到一条合法下行消息调用一次。
  onMessage: (msg: DownMessage) => void
  // onState 连接状态变化。gaveup 时页面应该提示「对局已结束」并回大厅。
  onState: (state: ConnectionState) => void
}

// 重连上限。超过就放弃：对局已经从服务端内存里回收时，
// 重连会一直拿到 404，无限重试只会让页面永远卡在「连接中」。
const MAX_RETRY = 5

// 退避基数，实际间隔是 base * 2^n，上限 8 秒，再叠 0~500ms 抖动。
const RETRY_BASE = 1000
const RETRY_CAP = 8000

// GameSocket 管理一条对局连接。
//
// 用法：new GameSocket(opts) → connect() → 用 shoot / placeCue 发命令 → close()。
// 组件卸载时必须 close()，否则连接会一直挂着直到服务端读超时。
export class GameSocket {
  private opts: GameSocketOptions
  private socket: WebSocket | null = null
  private retryCount = 0
  private retryTimer: ReturnType<typeof setTimeout> | null = null
  // manualClose 区分「我们自己关的」与「网络断了」：
  // 主动离开对局后不该再重连，否则会在服务端一直挂着一条幽灵连接。
  private manualClose = false
  private seq = 0

  constructor(opts: GameSocketOptions) {
    this.opts = opts
  }

  // connect 发起第一次握手。重复调用无副作用。
  connect() {
    if (this.socket || this.retryTimer) {
      return
    }
    this.manualClose = false
    this.open()
  }

  // close 主动断开且不再重连。
  close() {
    this.manualClose = true
    if (this.retryTimer) {
      clearTimeout(this.retryTimer)
      this.retryTimer = null
    }
    if (this.socket) {
      this.socket.close()
      this.socket = null
    }
    this.opts.onState('closed')
  }

  // shoot 出杆。返回这次请求的 seq，供页面把 error 帧对到具体哪一杆。
  shoot(params: ShotParams): number {
    return this.send('shoot', {
      phi: params.phi,
      power: params.power,
      theta: params.theta,
      a: params.a,
      b: params.b,
    })
  }

  // placeCue 把母球摆到指定位置（手中球时用）。
  placeCue(x: number, y: number): number {
    return this.send('place_cue', { x, y })
  }

  // leave 告知服务端自己退出。服务端据此判负并结算。
  leave(): number {
    return this.send('leave', {})
  }

  // send 统一出口：拼信封、发出去、返回 seq。
  //
  // 连接不在时返回 0 而不是抛异常：出杆按钮在断线期间本就该是禁用的，
  // 真的漏了一处也不值得让页面崩掉，丢一杆比白屏好。
  private send(type: string, data: Record<string, unknown>): number {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      return 0
    }
    this.seq += 1
    const frame = { type, seq: this.seq, ts: Date.now(), data }
    this.socket.send(JSON.stringify(frame))
    return this.seq
  }

  // open 真正发起一次握手。
  //
  // 每次握手都重新取 token：access 只有 15 分钟，
  // 断线重连往往发生在很久以后，沿用建连时那个会被服务端拒掉。
  private open() {
    const token = this.opts.token
    let ws: WebSocket
    try {
      ws = new WebSocket(
        wsURL('/ws/game', { token, match_id: String(this.opts.matchId) }),
      )
    } catch {
      this.scheduleRetry()
      return
    }
    this.socket = ws
    this.opts.onState('connecting')

    ws.onopen = () => {
      this.retryCount = 0
      this.opts.onState('open')
    }

    ws.onmessage = (event) => {
      const msg = decode(event.data)
      if (!msg) {
        return
      }
      this.opts.onMessage(msg)
    }

    ws.onclose = () => {
      this.socket = null
      if (this.manualClose) {
        return
      }
      this.scheduleRetry()
    }

    // onerror 之后一定会有 onclose，重连只写在 onclose 里：
    // 两处都写会排出两个定时器，最后连出两条连接。
    ws.onerror = () => {}
  }

  // scheduleRetry 安排下一次重连，超过上限就宣告放弃。
  private scheduleRetry() {
    if (this.manualClose || this.retryTimer) {
      return
    }
    if (this.retryCount >= MAX_RETRY) {
      this.opts.onState('gaveup')
      return
    }
    const base = Math.min(RETRY_CAP, RETRY_BASE * Math.pow(2, this.retryCount))
    const delay = base + Math.floor(Math.random() * 500)
    this.retryCount += 1
    this.opts.onState('closed')
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null
      this.open()
    }, delay)
  }
}

// decode 把一帧原始数据解析成 DownMessage。
//
// 认不出的类型返回 null：将来服务端加了新消息类型（观战、表情），
// 老客户端应该安静忽略而不是抛异常把整条连接搞乱。
export function decode(raw: unknown): DownMessage | null {
  if (typeof raw !== 'string') {
    return null
  }
  let parsed: { type?: unknown }
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  if (!parsed || typeof parsed.type !== 'string') {
    return null
  }
  switch (parsed.type) {
    case 'joined':
    case 'state':
    case 'turn':
    case 'shot':
    case 'foul':
    case 'result':
    case 'peer':
    case 'error':
      return parsed as DownMessage
    default:
      return null
  }
}
