import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  getUnreadTotal,
  listConversations,
  markConversationRead,
  type Brief,
  type ConversationResp,
  type MessageResp,
} from '@/api/message'
import { onLogout } from '@/utils/auth'
import { onFrame, onReconnect } from '@/utils/ws'

// 私信的全局状态：会话列表 + 总未读。
//
// 为什么提到 store 而不是留在页面里：
//   - 侧边栏的未读角标和聊天页要读同一份数字，各拉一次会不一致；
//   - WebSocket 推来的新消息要同时更新「左栏摘要」和「角标」，
//     放在页面里就得靠事件把两边串起来。
// 消息气泡本身留在聊天页组件里：只有当前会话需要它，进 store 反而是无谓的全局状态。

// MessagePushPayload 与后端 message/messagePushPayload 对应。
// sender 可能缺失（账号已删），此时只能刷新整个列表，无法在本地拼出会话行。
interface MessagePushPayload {
  message: MessageResp
  sender?: Brief
  unread: number
}

export const useImStore = defineStore('im', () => {
  // conversations 按 updated_at 倒序，最新的会话在最前（和微信/抖音一致）。
  const conversations = ref<ConversationResp[]>([])
  // totalUnread 用后端 /conversations/unread 的权威值，不拿列表里各行相加：
  // 列表只拉前 50 条，会话超过 50 个时相加会少算。
  const totalUnread = ref(0)
  const loading = ref(false)
  const loaded = ref(false)

  // wsBound 保证全局只注册一次帧处理。pinia store 与 app 同生命周期，
  // 注册多次会让同一条消息被处理多遍（角标加两次、摘要闪两下）。
  let wsBound = false

  // inflight 是正在进行的会话列表请求，用来合并并发调用（见 refresh 的注释）。
  let inflight: Promise<void> | null = null

  // requestSeq 是「同一类请求」的代次：只接受最后发出的那一次结果。
  // 没有它的话，两次并发 refresh 回来的顺序不定，旧的会覆盖新的。
  let requestSeq = 0

  // sessionSeq 是「登录会话」的代次：登出时加一，此后所有在途请求的结果全部作废。
  // 没有它的话：退出登录的瞬间正在飞的那个 GET /conversations 回来，
  // 会把上一个账号的会话列表写进已经清空的 store，下一个登录的人直接看到别人的私信。
  //
  // 为什么不用一个代次搞定两件事：一次正常的列表刷新会把它加一，
  // 同时在飞的「未读数刷新」就会被误判成过期，角标偶尔不更新。
  // 两个概念分开之后，刷新列表只影响列表，登出才影响全部。
  let sessionSeq = 0

  const hasUnread = computed(() => totalUnread.value > 0)

  function indexOfPeer(peerId: number): number {
    for (let i = 0; i < conversations.value.length; i++) {
      if (conversations.value[i].peer.id === peerId) {
        return i
      }
    }
    return -1
  }

  // ensureRow 确保某个人的会话行存在，返回它的下标。
  //
  // 「从别人主页点私信」时对方可能从没聊过，列表里没有这一行；
  // 不补一行的话左栏没有高亮项，看起来像没选中任何人。
  function ensureRow(peer: Brief): number {
    const idx = indexOfPeer(peer.id)
    if (idx >= 0) {
      return idx
    }
    conversations.value.unshift({
      peer,
      last_message: null,
      unread: 0,
      updated_at: new Date().toISOString(),
    })
    return 0
  }

  // moveToTop 把某个会话移到最前。
  // 用 splice 而不是 sort：只需要挪一行，排序要遍历整个列表。
  function moveToTop(idx: number) {
    if (idx <= 0) {
      return
    }
    const rows = conversations.value
    const row = rows[idx]
    rows.splice(idx, 1)
    rows.unshift(row)
  }

  // refresh 拉一页会话，并同步总未读。
  // 两个请求并发发出去，串行会白白多等一个往返。
  //
  // 用 inflight 做并发合并：布局组件和私信页几乎同时挂载，两边各自调一次
  // refresh 就会发两轮请求，第二次的结果和第一次一样，纯属浪费配额
  // （限流是按 IP 分桶的，多发的请求会挤掉别的接口的令牌）。
  async function refresh() {
    if (inflight) {
      return inflight
    }
    const task = loadConversations()
    inflight = task
    try {
      await task
    } finally {
      // 只清自己这一次的标记：reset 之后可能有新请求已经接管了 inflight，
      // 无条件置空会把新请求的合并标记提前清掉。
      if (inflight === task) {
        inflight = null
      }
    }
  }

  async function loadConversations() {
    requestSeq += 1
    const seq = requestSeq
    const session = sessionSeq
    loading.value = true
    try {
      const [list, unread] = await Promise.all([listConversations(1, 50), getUnreadTotal()])
      // 两个条件都要满足：自己是最后发出的那次请求，且期间没有登出。
      if (seq !== requestSeq || session !== sessionSeq) {
        return
      }
      conversations.value = list.list
      totalUnread.value = unread.unread
      loaded.value = true
    } catch {
      // 失败保留旧数据。请求拦截器已经弹过提示，这里再抛会让页面报错白屏。
    } finally {
      if (seq === requestSeq && session === sessionSeq) {
        loading.value = false
      }
    }
  }

  // refreshUnread 只刷总未读数，用于「本地算不准」的兜底场景。
  async function refreshUnread() {
    // 只看会话代次，不看请求代次：它和列表刷新是两件独立的事，
    // 同时进行时不该因为对方发了新请求就把自己的结果丢掉。
    const session = sessionSeq
    try {
      const data = await getUnreadTotal()
      if (session !== sessionSeq) {
        return
      }
      totalUnread.value = data.unread
    } catch {
      // 同上，角标短暂不准可以接受。
    }
  }

  // applyIncoming 处理一条 WebSocket 推来的新私信。
  //
  // unread 用 payload 里的值而不是本地 +1：同一个账号可能在手机和电脑同时在线，
  // 两台设备各自 +1 会让数字翻倍，服务端算好的值才是权威。
  function applyIncoming(payload: MessagePushPayload) {
    const msg = payload.message
    const sender = payload.sender
    if (!sender) {
      // 发送者资料缺失（账号已删），本地拼不出会话行，整体刷一次。
      void refresh()
      return
    }
    const idx = indexOfPeer(sender.id)
    if (idx >= 0) {
      const row = conversations.value[idx]
      const delta = payload.unread - row.unread
      row.unread = payload.unread
      row.last_message = msg
      row.updated_at = msg.created_at
      totalUnread.value = Math.max(0, totalUnread.value + delta)
      moveToTop(idx)
      return
    }
    // 列表里没有这个会话：可能是很久没聊、被挤出前 50 条，
    // 也可能是对方第一次发消息。插一行到最前，未读直接用 payload 的值。
    conversations.value.unshift({
      peer: sender,
      last_message: msg,
      unread: payload.unread,
      updated_at: msg.created_at,
    })
    totalUnread.value = Math.max(0, totalUnread.value + payload.unread)
  }

  // applySent 在「我发出一条消息」后更新左栏摘要。
  // 自己的消息不影响未读，只需要把会话顶上去并换掉摘要。
  function applySent(peer: Brief, msg: MessageResp) {
    const idx = ensureRow(peer)
    const row = conversations.value[idx]
    row.last_message = msg
    row.updated_at = msg.created_at
    moveToTop(idx)
  }

  // markRead 把某个会话标记为已读。
  //
  // 本地先清掉再发请求：点进去红点立刻消失，不用等一个往返。
  // 失败也不回滚——下一次刷新会话列表会拿到真实值。
  async function markRead(peerId: number) {
    const idx = indexOfPeer(peerId)
    if (idx < 0) {
      return
    }
    const row = conversations.value[idx]
    const before = row.unread
    if (before === 0) {
      return
    }
    row.unread = 0
    totalUnread.value = Math.max(0, totalUnread.value - before)
    try {
      await markConversationRead(peerId)
    } catch {
      // 标记已读失败最多是下次进来还有红点，不影响消息内容。
    }
  }

  // initWS 注册实时帧处理，幂等。
  // 断线重连后整体刷一次：断线期间推的帧是收不到的（服务端推送尽力而为），
  // 不补拉的话界面会一直缺那几条。
  function initWS() {
    if (wsBound) {
      return
    }
    wsBound = true
    onFrame('message.created', (data: MessagePushPayload) => {
      applyIncoming(data)
    })
    onReconnect(() => {
      void refresh()
    })
  }

  // reset 在登出时清空：不清理的话下一个人登录会先看到上一个人的会话列表。
  //
  // 两个代次各自加一：
  //   - sessionSeq 让所有在途请求的结果作废（这是本函数的主要目的）；
  //   - requestSeq 让 inflight 里那个旧任务的 finally 不再有资格清标记。
  function reset() {
    sessionSeq += 1
    requestSeq += 1
    inflight = null
    conversations.value = []
    totalUnread.value = 0
    loading.value = false
    loaded.value = false
  }

  // 登出（含 token 过期）时清空账号数据。
  // 注册一次即可：store 是单例，setup 只跑一次。
  onLogout(reset)

  return {
    conversations,
    totalUnread,
    hasUnread,
    loading,
    loaded,
    refresh,
    refreshUnread,
    applyIncoming,
    applySent,
    markRead,
    ensureRow,
    initWS,
    reset,
  }
})
