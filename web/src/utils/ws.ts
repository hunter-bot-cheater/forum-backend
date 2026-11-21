import { reactive } from 'vue'
import { getToken } from '@/utils/auth'

// 全局唯一的 WebSocket 连接。
//
// 为什么不每页各建一条：后端 Hub 是「一个用户挂多条连接」，多开标签页没问题，
// 但同一个页面里会话列表、聊天页、角标各建一条会白白耗三份连接和三次握手。
// 而且连接一多，退出登录时更容易漏关，服务端要等读超时才回收。
//
// 这个模块只做三件事：建连、断线重连、按帧类型分发。
// 它不碰业务状态——收到 message.created 该更新什么，由订阅方自己决定。

export interface Frame {
  type: string
  data: unknown
}

type FrameHandler = (data: any) => void
type LifecycleHandler = () => void

// 帧类型 -> 处理函数集合。用 Set 是因为同一类型可以被多处订阅
// （聊天页要追加气泡，会话列表要更新摘要，角标要改数字），
// 退订时按函数引用精确删除。
const handlers = new Map<string, Set<FrameHandler>>()

// 重连成功后的回调。断线期间服务端推的帧是丢掉的（推送是尽力而为），
// 页面要靠这个回调主动补拉一次，否则会一直缺那几条。
const reconnectHandlers = new Set<LifecycleHandler>()

// connected 给界面读：断线时聊天页顶部显示「连接中断，正在重连」，
// 用户才知道为什么对方半天没回（其实是自己掉线了）。
export const wsState = reactive({ connected: false })

let socket: WebSocket | null = null
let currentToken = ''
let retryCount = 0
let retryTimer: ReturnType<typeof setTimeout> | null = null
// manualClose 区分「我们自己关的」和「网络断了」：
// 主动登出后不该自动重连，否则会拿失效 token 反复打服务端。
let manualClose = false
// everConnected 让「首次连上」不触发补拉：首屏本来就在拉数据，补拉纯属重复请求。
let everConnected = false

// wsURL 把 REST 的 baseURL 翻译成某个 WebSocket 端点的完整地址。
//
// 两个坑：
//   1. 浏览器不接受相对路径，必须补上协议和 host；
//   2. 走 vite 代理时 baseURL 是 '/api'，走独立域名时是 'https://x.com/api'，
//      后者要把 http(s) 换成 ws(s)，否则握手直接被浏览器拒绝。
//
// 抽出来给通知与对局两处共用：两边各自拼一遍地址，
// 迟早出现「通知连得上、对局连不上」这种只在生产域名下才暴露的问题。
// path 传 '/ws/game' 这种以斜杠开头的相对路径。
export function wsURL(path: string, params: Record<string, string>): string {
  const base = import.meta.env.VITE_API_BASE_URL || '/api'
  let prefix = base
  if (/^https?:\/\//i.test(base)) {
    const u = new URL(base)
    const proto = u.protocol === 'https:' ? 'wss:' : 'ws:'
    prefix = proto + '//' + u.host + u.pathname
  } else {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    prefix = proto + '//' + location.host + base
  }

  const query: string[] = []
  for (const key of Object.keys(params)) {
    query.push(encodeURIComponent(key) + '=' + encodeURIComponent(params[key]))
  }
  const tail = query.length > 0 ? '?' + query.join('&') : ''
  return prefix.replace(/\/+$/, '') + path + tail
}

// buildURL 是通知连接的地址。
function buildURL(token: string): string {
  return wsURL('/ws/notifications', { token })
}

// connectWS 建立连接，幂等：同一个 token 重复调用不会建第二条。
// 由布局组件在挂载时调用一次，登录成功后也会调一次（token 变了要重建）。
export function connectWS(token: string) {
  if (!token) {
    return
  }
  // token 没变且连接还在（或正在重连），什么都不做。
  if (token === currentToken && (socket || retryTimer)) {
    return
  }
  // token 变了：先把旧连接关掉，避免用旧身份继续收推送。
  if (socket) {
    manualClose = true
    socket.close()
    socket = null
  }
  currentToken = token
  manualClose = false
  retryCount = 0
  open()
}

// open 真正发起一次握手。
function open() {
  // 每次握手都从本地存储取一次 access，而不是沿用建连时那个：
  // access 只有 15 分钟，断线重连往往发生在很久以后，
  // 拿旧 token 握手会被后端拒掉，然后退避重试，永远连不上。
  const token = getToken() || currentToken
  if (!token) {
    return
  }
  currentToken = token
  let ws: WebSocket
  try {
    ws = new WebSocket(buildURL(token))
  } catch {
    // 构造失败通常是把 URL 拼错了（比如 baseURL 写成了非法值），
    // 这种情况重连也没用，但保持退避重试的成本很低，交给 scheduleRetry。
    scheduleRetry()
    return
  }
  socket = ws

  ws.onopen = () => {
    retryCount = 0
    wsState.connected = true
    if (everConnected) {
      // 重连成功：通知订阅方补拉断线期间可能漏掉的数据。
      for (const fn of reconnectHandlers) {
        fn()
      }
    }
    everConnected = true
  }

  ws.onmessage = (event) => {
    let frame: Frame
    try {
      frame = JSON.parse(event.data)
    } catch {
      // 解析失败只可能是服务端改了协议或中间设备插了东西，
      // 丢掉这一帧比抛异常中断后续所有帧要好。
      return
    }
    const set = handlers.get(frame.type)
    if (!set) {
      return
    }
    for (const fn of set) {
      try {
        fn(frame.data)
      } catch (err) {
        // 单个订阅方报错不该拖累其它订阅方（比如聊天页抛错导致角标不更新）。
        console.error('[ws] handler error for', frame.type, err)
      }
    }
  }

  ws.onclose = () => {
    wsState.connected = false
    socket = null
    if (manualClose) {
      return
    }
    scheduleRetry()
  }

  // onerror 之后一定会触发 onclose，重连逻辑只写在 onclose 里，
  // 两处都写会排出两个重连定时器，最后连出两条连接。
  ws.onerror = () => {
    wsState.connected = false
  }
}

// scheduleRetry 安排下一次重连。
//
// 退避 1s → 2s → 4s → … 上限 30s，再叠 0~1s 随机抖动：
// 后端重启时所有客户端会在同一秒断开，不加抖动就会在同一秒一起回来。
function scheduleRetry() {
  if (manualClose || retryTimer) {
    return
  }
  const base = Math.min(30000, 1000 * Math.pow(2, retryCount))
  const delay = base + Math.floor(Math.random() * 1000)
  retryCount += 1
  retryTimer = setTimeout(() => {
    retryTimer = null
    open()
  }, delay)
}

// disconnectWS 由登出流程调用：主动断开且不再重连。
export function disconnectWS() {
  manualClose = true
  currentToken = ''
  everConnected = false
  if (retryTimer) {
    clearTimeout(retryTimer)
    retryTimer = null
  }
  if (socket) {
    socket.close()
    socket = null
  }
  wsState.connected = false
}

// onFrame 订阅某类帧，返回退订函数。
// 组件在 onUnmounted 里调用返回的函数，避免页面切走后还往已销毁的 ref 上写数据。
export function onFrame(type: string, handler: FrameHandler): () => void {
  let set = handlers.get(type)
  if (!set) {
    set = new Set()
    handlers.set(type, set)
  }
  set.add(handler)
  return () => {
    set?.delete(handler)
  }
}

// onReconnect 订阅「重连成功」。同样返回退订函数。
export function onReconnect(handler: LifecycleHandler): () => void {
  reconnectHandlers.add(handler)
  return () => {
    reconnectHandlers.delete(handler)
  }
}
