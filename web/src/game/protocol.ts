import type { MatchSnapshot, Seat, ShotParams, Viewer } from './types'
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
  // seat 是 Viewer 而不是 Seat：观战进房时服务端给 -1，
  // 对局页拿它当座位用之前必须先收窄（见 match.vue 的 seatOfViewer）。
  seat: Viewer
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
  // 服务端仍会带这两个积分增量（库里的 rating 还在算），界面已经不显示了。
  // 留着字段是因为它属于线上协议：删掉类型不会让服务端少发这两个键。
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

// 服务端在升级之后拒绝时用的关闭码，与 internal/pool/protocol.go 一致。
//
// 握手前的 4xx 在浏览器里读不到（一律表现为一次连接失败），
// 所以「对局没了」「观战名额满了」这类业务性拒绝都改成升级后用关闭码说，
// 客户端读得到就能立刻停，而不是退避重试四十七秒再给一个必然失败的按钮。
export const CLOSE_SPECTATOR_FULL = 4001
export const CLOSE_MATCH_GONE = 4004

// 4000 以上归应用自己用（1000-2999 协议保留，3000-3999 留给库）。
// 换句话说：凡是从这个区间来的关闭码，都不是网络问题，重连没有意义。
const CLOSE_FATAL_BASE = 4000

export interface GameSocketOptions {
  matchId: number | string
  // token 是个 getter 不是字符串：access 只有 15 分钟，断线重连往往发生在
  // 很久以后，建连时就把 token 定死会让重连一直用过期凭证被服务端拒掉。
  token: () => string
  // onMessage 每收到一条合法下行消息调用一次。
  onMessage: (msg: DownMessage) => void
  // onState 连接状态变化。gaveup 时页面应该提示「对局已结束」并回大厅。
  onState: (state: ConnectionState) => void
  // onFatal 收到业务关闭码时调用，之后不会再重连。
  // 页面据此显示「为什么看不了」，而不是挂一个必然失败的重试按钮。
  onFatal?: (code: number) => void
}

// 重连上限。超过就放弃，等用户点「重新连接」。
//
// 取 8 次而不是三五次，是为了盖住「拔网半分钟」这种真实情况：
// 退避序列是 1+2+4+8+8+8+8+8 ≈ 47 秒（另有 0~500ms 抖动），
// 断网三十秒的人不用做任何事就能自己接回来。
const MAX_RETRY = 8

// 退避基数，实际间隔是 base * 2^n，上限 8 秒，再叠 0~500ms 抖动。
const RETRY_BASE = 1000
const RETRY_CAP = 8000

// 连接要稳定这么久才算「这次重连真的成功了」，才把重试预算清零。
//
// 不在 onopen 里立刻清零：服务端或前置代理接受升级后马上关（1006、1011，
// 或者库自己的 3xxx 码）时，立刻清零会让退避永远停在 1 秒一档，
// 页面无限重连、永远走不到 gaveup，用户也就永远等不到「重新连接」。
const STABLE_MS = 5000

// GameSocket 管理一条对局连接。
//
// 用法：new GameSocket(opts) → connect() → 用 shoot / placeCue 发命令 → close()。
// 组件卸载时必须 close()，否则连接会一直挂着直到服务端读超时。
export class GameSocket {
  private opts: GameSocketOptions
  private socket: WebSocket | null = null
  private retryCount = 0
  private retryTimer: ReturnType<typeof setTimeout> | null = null
  // stableTimer 是「连接已经稳定」的计时器，见 STABLE_MS。
  private stableTimer: ReturnType<typeof setTimeout> | null = null
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
    // 计数必须复位：gaveup 之后再调 connect 是一次全新的人工重试，
    // 不复位的话第一次握手失败就会立刻再判一次 gaveup（计数还停在上限）。
    this.retryCount = 0
    this.open()
  }

  // close 主动断开且不再重连。
  close() {
    this.manualClose = true
    this.clearStableTimer()
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

  // clearStableTimer 清掉「连接已稳定」的计时器。
  private clearStableTimer() {
    if (this.stableTimer) {
      clearTimeout(this.stableTimer)
      this.stableTimer = null
    }
  }

  // open 真正发起一次握手。
  private open() {
    const token = this.opts.token()
    if (!token) {
      // 已经登出（另一个标签页退出、刷新令牌失败）就别再重连了，
      // 继续重试只会拿着空 token 在服务端门口转圈。
      this.manualClose = true
      this.opts.onState('gaveup')
      return
    }
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
      this.opts.onState('open')
      this.clearStableTimer()
      this.stableTimer = setTimeout(() => {
        this.retryCount = 0
        this.stableTimer = null
      }, STABLE_MS)
    }

    ws.onmessage = (event) => {
      const msg = decode(event.data)
      if (!msg) {
        return
      }
      this.opts.onMessage(msg)
    }

    ws.onclose = (event) => {
      // 旧连接的回调可能在新连接建好之后才到（浏览器关连接是异步的）。
      // 不认身份的话它会把新连接从字段上清掉——之后 send 全被丢掉、
      // 界面显示「重连中」而其实连着，还会多排一次退避。
      if (this.socket !== ws) {
        return
      }
      this.socket = null
      this.clearStableTimer()
      // 业务关闭码：服务端已经明确说了「这条路走不通」，
      // 再退避重试只是让人对着一个必然失败的页面等四十七秒。
      if (event.code >= CLOSE_FATAL_BASE) {
        this.manualClose = true
        this.opts.onFatal?.(event.code)
        // 先要知道「为什么」，页面才谈得上显示什么；同时置成终态，
        // 否则界面会停在「已连接」而其实一条消息都收不到了。
        this.opts.onState('gaveup')
        return
      }
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
