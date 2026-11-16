import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  getUnreadSummary,
  listNotifications,
  readAllNotifications,
  readNotification,
  type NotificationKind,
  type NotificationResp,
} from '@/api/notification'
import { onLogout } from '@/utils/auth'
import { onFrame, onReconnect } from '@/utils/ws'

// 消息中心的全局状态：当前分类的列表 + 各分类未读数。
//
// 为什么要分两层计数：
//   - summary 是「全部未读 + 每个分类未读」，给侧边栏角标和顶部 tab 红点用；
//   - unread 是「当前分类未读」，跟着列表一起回来，给标题旁的数字用。
// 两者口径不同，混用一个会出现「切到评论 tab，角标也跟着变」的怪现象。

// NotificationPushPayload 对应后端 notification/pushPayload。
// 推送里只有 id 和事件类型，没有 actor 资料：消费者是热路径，
// 为了一条推送多查一次 users 不划算，前端收到后回列表接口补齐。
interface NotificationPushPayload {
  id: number
  type: string
  article_id: number
  actor_id: number
  target_type: string
  target_id: number
  created_at: string
}

// 一页 20 条：通知条目比私信矮，20 条正好填满一屏多一点。
const PAGE_SIZE = 20

// 推送合并窗口：短时间内多条通知只刷新一次列表。
// 没有这个窗口的话，一条热门评论带来的连赞会让前端按帧发请求，
// 既浪费令牌（限流按 IP 分桶），也会让列表反复重排。
const REFRESH_DELAY = 300

const EMPTY_SUMMARY: Record<string, number> = { total: 0, like: 0, comment: 0, follow: 0 }

// kindOfEventType 把事件类型归到分类，与后端 kindOfEventType 保持一致。
// 未登记的类型返回空串：它只影响「全部」的角标，不进任何 tab。
// 这里必须和后端 internal/notification/service.go 的 kindEventTypes 同步改，
// 两边不一致的表现是「tab 里的数字点了没有对应的列表」。
export function kindOfEventType(eventType: string): NotificationKind {
  if (eventType === 'like.created') {
    return 'like'
  }
  if (eventType === 'comment.created') {
    return 'comment'
  }
  if (eventType === 'follow.created') {
    return 'follow'
  }
  return ''
}

export const useNotificationStore = defineStore('notification', () => {
  // items 是当前分类下的一页数据，切 tab 就整页替换。
  const items = ref<NotificationResp[]>([])
  const total = ref(0)
  // unread 是当前分类的未读数（接口跟着列表一起返回）。
  const unread = ref(0)
  const page = ref(1)
  // kind 是当前分类，空串为全部。
  const kind = ref<NotificationKind>('')
  // summary 是各分类未读，total 表示全站未读，用于侧边栏与 tab 红点。
  const summary = ref<Record<string, number>>({ ...EMPTY_SUMMARY })
  const loading = ref(false)
  const loaded = ref(false)

  // wsBound 保证全局只注册一次帧处理，重复注册会让角标加两次。
  let wsBound = false
  // inflight 只合并第一页的刷新：翻页请求各页不同，合并会把用户切页的操作吞掉。
  let inflight: Promise<void> | null = null
  // refreshTimer 是推送合并窗口的定时器，登出时要清掉，否则会拿着旧 token 发请求。
  let refreshTimer: number | null = null
  // 请求序号：切 tab 会让多个请求并发，只接受最后一次发出的结果。
  let requestSeq = 0
  // 会话代次：登出时加一，此后所有在途请求的结果作废。
  // 和 requestSeq 分开的原因见 im store：共用一个会误伤同时在飞的 summary 请求。
  let sessionSeq = 0

  const hasUnread = computed(() => (summary.value.total || 0) > 0)

  // refresh 拉指定分类的指定页，默认拉当前分类的第一页。
  // 切 tab 时必须显式传第一页：从第 3 页切到别的分类还停在第 3 页会看到空列表。
  async function refresh(nextKind: NotificationKind = kind.value, targetPage = 1) {
    if (nextKind === kind.value && targetPage === 1 && inflight) {
      return inflight
    }
    const task = load(nextKind, targetPage)
    if (targetPage === 1) {
      inflight = task
    }
    try {
      await task
    } finally {
      // 只清自己这次：reset 之后可能已经有新请求接管了 inflight。
      if (targetPage === 1 && inflight === task) {
        inflight = null
      }
    }
  }

  async function load(nextKind: NotificationKind, targetPage: number) {
    requestSeq += 1
    const seq = requestSeq
    const session = sessionSeq
    loading.value = true
    try {
      const data = await listNotifications(nextKind, targetPage, PAGE_SIZE)
      // 迟到的响应直接丢弃：它属于上一次点击的 tab（或上一个账号），
      // 写进来会让用户看到「点了评论却显示赞」。
      if (seq !== requestSeq || session !== sessionSeq) {
        return
      }
      items.value = data.list
      total.value = data.total
      unread.value = data.unread_count
      page.value = data.page
      kind.value = data.kind
      loaded.value = true
    } catch {
      // 失败保留旧数据。拦截器已经弹过提示，这里再抛会让页面白屏。
    } finally {
      if (seq === requestSeq && session === sessionSeq) {
        loading.value = false
      }
    }
  }

  // switchKind 切换分类：重置分页并拉第一页。
  function switchKind(nextKind: NotificationKind) {
    if (nextKind === kind.value) {
      return
    }
    kind.value = nextKind
    page.value = 1
    items.value = []
    void refresh(nextKind, 1)
  }

  // refreshSummary 刷新分类未读数，失败保留旧值（角标短暂不准可接受）。
  async function refreshSummary() {
    // 只看会话代次：它和切 tab 的列表请求互不影响。
    const session = sessionSeq
    try {
      const data = await getUnreadSummary()
      if (session !== sessionSeq) {
        return
      }
      summary.value = {
        total: data.total,
        like: data.by_kind.like || 0,
        comment: data.by_kind.comment || 0,
        follow: data.by_kind.follow || 0,
      }
    } catch {
      // 同 refresh 的 catch：不让拦截器的提示之外再抛一次。
    }
  }

  // markAllRead 全部已读：只清红点，历史记录继续留在列表里。
  //
  // 本地先清空红点再发请求：点下去立刻有反馈，不用等往返。
  // 失败不回滚，下一次刷新会拿到服务端的真实状态。
  //
  // 返回值：>=0 是受影响条数，-1 表示请求失败。
  // 不用 0 表示失败：0 本身是「本来就没有未读」的正常结果，
  // 两者混在一起时页面会把失败提示成「暂无未读通知」。
  async function markAllRead(): Promise<number> {
    const hadUnread = (summary.value.total || 0) > 0 || unread.value > 0
    if (!hadUnread) {
      return 0
    }
    items.value = items.value.map((item) => {
      // 新建对象而不是改字段：换数组只让列表更新一次，
      // 改字段会让所有依赖 items 的组件都重渲染。
      return { ...item, is_read: true }
    })
    unread.value = 0
    summary.value = { ...summary.value, total: 0, like: 0, comment: 0, follow: 0 }
    try {
      const res = await readAllNotifications()
      return res.affected
    } catch {
      // 失败时把状态拉回真实值。
      void refresh(kind.value, page.value)
      void refreshSummary()
      return -1
    }
  }

  // markRead 把单条通知标记为已读：点开哪条就清哪条的红点。
  //
  // 同样是乐观更新：点击后立刻变白，失败了再把列表与计数拉回真值。
  // 不在这里等待接口返回，是因为点击会紧接着跳转文章页，
  // 等往返会让用户看到「红点还在」的一瞬间。
  async function markRead(id: number) {
    const target = items.value.find((item) => item.id === id)
    if (!target || target.is_read) {
      return
    }

    items.value = items.value.map((item) => {
      if (item.id !== id) {
        return item
      }
      return { ...item, is_read: true }
    })
    // 计数用 max(0, ...) 兜底：推送到达与本地标记可能让数字已经偏小，
    // 减成负数会让角标显示 -1。
    unread.value = Math.max(0, unread.value - 1)
    const incomingKind = kindOfEventType(target.type)
    const nextSummary = { ...summary.value }
    nextSummary.total = Math.max(0, (nextSummary.total || 0) - 1)
    if (incomingKind !== '') {
      nextSummary[incomingKind] = Math.max(0, (nextSummary[incomingKind] || 0) - 1)
    }
    summary.value = nextSummary

    try {
      await readNotification(id)
    } catch {
      // 失败就把真实状态拉回来，避免本地和服务端越差越多。
      void refresh(kind.value, page.value)
      void refreshSummary()
    }
  }

  // scheduleRefresh 在合并窗口后刷新当前分类的第一页。
  // 只在第一页生效：用户翻到第 3 页时刷第一页，会把正在看的内容整页换掉。
  function scheduleRefresh() {
    if (page.value !== 1) {
      return
    }
    if (refreshTimer !== null) {
      return
    }
    refreshTimer = window.setTimeout(() => {
      refreshTimer = null
      void refresh(kind.value, 1)
    }, REFRESH_DELAY)
  }

  // applyIncoming 处理一条推送来的新通知。
  //
  // 计数本地 +1 而不是等接口：推送按 event_id 幂等，不会重复补发，单条 +1 是准的。
  // 列表则交给合并刷新去补——推送里没有昵称头像，本地拼不出可见的一行。
  function applyIncoming(payload: NotificationPushPayload) {
    const incomingKind = kindOfEventType(payload.type)

    // 先复制一份再改：直接改 summary.value 的字段会让所有读 summary 的组件重渲染，
    // 换对象只触发一次更新，也避免和「刷新 summary」的异步结果互相覆盖。
    const nextSummary = { ...summary.value }
    nextSummary.total = (nextSummary.total || 0) + 1
    if (incomingKind !== '') {
      nextSummary[incomingKind] = (nextSummary[incomingKind] || 0) + 1
    }
    summary.value = nextSummary

    // 当前正在看的分类包含这条，才需要刷新列表和标题数字；
    // 在「赞」tab 里收到评论通知，列表不该动，只要 tab 红点变。
    if (kind.value === '' || kind.value === incomingKind) {
      unread.value = unread.value + 1
      total.value = total.value + 1
      scheduleRefresh()
    }
  }

  // initWS 注册实时帧处理，幂等。
  // 断线重连后补一次：断线期间的推送收不到（服务端推送尽力而为）。
  function initWS() {
    if (wsBound) {
      return
    }
    wsBound = true
    onFrame('notification.created', (data: NotificationPushPayload) => {
      applyIncoming(data)
    })
    onReconnect(() => {
      void refreshSummary()
      if (loaded.value) {
        void refresh(kind.value, page.value)
      }
    })
  }

  // reset 在登出时清空，避免下一个登录的人看到上一个人的通知和红点。
  //
  // 两个代次各自加一：sessionSeq 让所有在途请求作废，
  // requestSeq 让 inflight 里那个旧任务不再有资格清标记。
  // refreshTimer 也要清掉，否则合并窗口到点后会拿着已失效的 token 再发一次请求。
  function reset() {
    sessionSeq += 1
    requestSeq += 1
    inflight = null
    if (refreshTimer !== null) {
      window.clearTimeout(refreshTimer)
      refreshTimer = null
    }
    items.value = []
    total.value = 0
    unread.value = 0
    page.value = 1
    kind.value = ''
    summary.value = { ...EMPTY_SUMMARY }
    loading.value = false
    loaded.value = false
  }

  // 登出（含 token 过期）时清空账号数据，注册一次即可。
  onLogout(reset)

  return {
    items,
    total,
    unread,
    page,
    kind,
    summary,
    hasUnread,
    pageSize: PAGE_SIZE,
    loading,
    loaded,
    refresh,
    switchKind,
    refreshSummary,
    markAllRead,
    markRead,
    applyIncoming,
    initWS,
    reset,
  }
})
