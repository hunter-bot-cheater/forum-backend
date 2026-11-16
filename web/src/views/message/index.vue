<template>
  <!-- 整页用 el-container 布局：左栏会话、右栏聊天，高度由外层 el-main 决定。
       不再自己写 flex 骨架，Element Plus 的 container 已经处理了 aside/main 的伸缩。 -->
  <el-container class="im-page">
    <!-- 左栏：会话列表。窄屏进聊天后整栏让位，避免两栏挤成一条缝。 -->
    <el-aside class="im-aside" width="320px" :class="{ 'is-hidden-mobile': isMobile && activePeerId > 0 }">
      <div class="im-aside__head">
        <div class="im-aside__title">私信</div>
        <el-input
          v-model="keyword"
          class="im-aside__search"
          placeholder="搜索联系人"
          :prefix-icon="Search"
          clearable
        />
      </div>

      <!-- Conversations 是组件库的会话列表：选中态、hover、滚动、空态插槽都由它提供。
           每一行的内容用 #label 插槽定制成抖音那种「头像 + 昵称 + 摘要 + 时间」。 -->
      <Conversations
        class="im-conv"
        :items="convItems"
        :active="activePeerId > 0 ? activePeerId : undefined"
        row-key="id"
        label-key="label"
        :show-built-in-menu="false"
        :items-style="convItemStyle"
        :items-hover-style="convHoverStyle"
        :items-active-style="convActiveStyle"
        @change="onConvChange"
      >
        <template #label="{ item }">
          <div class="conv">
            <el-badge :is-dot="item.unread > 0" class="conv__badge">
              <el-avatar :size="44" :src="item.peer.avatar_url">
                {{ initials(item.peer.nickname) }}
              </el-avatar>
            </el-badge>
            <div class="conv__body">
              <div class="conv__line">
                <span class="conv__name">{{ item.peer.nickname }}</span>
                <span class="conv__time">{{ item.timeText }}</span>
              </div>
              <div class="conv__preview">{{ item.preview }}</div>
            </div>
          </div>
        </template>

        <template #footer>
          <div v-if="visibleRows.length === 0" class="im-aside__empty">
            <el-empty :image-size="64" :description="keyword ? '没找到匹配的联系人' : '还没有聊过天'" />
          </div>
        </template>
      </Conversations>
    </el-aside>

    <!-- 右栏：与某人的专属聊天区 -->
    <el-container class="im-chat" :class="{ 'is-hidden-mobile': isMobile && activePeerId === 0 }">
      <template v-if="peer">
        <el-header class="im-chat__head" height="60px">
          <el-button v-if="isMobile" text class="im-chat__back" @click="backToList">返回</el-button>
          <el-avatar :size="34" :src="peer.avatar_url">{{ initials(peer.nickname) }}</el-avatar>
          <div class="im-chat__who">
            <div class="im-chat__name">{{ peer.nickname }}</div>
            <div class="im-chat__hint">{{ connectionText }}</div>
          </div>
        </el-header>

        <!-- BubbleList 负责滚动、贴底跟随、上滑加载更早消息、回到最新按钮。
             这些行为以前是自己写的，现在交给组件库，页面只管数据和边界状态。 -->
        <el-main class="im-chat__body">
          <BubbleList
            ref="bubbleRef"
            class="im-bubbles"
            :list="bubbles"
            :item-key="bubbleKey"
            :auto-scroll="true"
            :top-status="topStatus"
            :load-more-top-threshold="80"
            @load-more-top="loadOlder"
          >
            <!-- 用 #item 整条自定义，而不是 Bubble 的 #header：
                 header 在气泡容器内部，宽度只到气泡那么宽，时间分隔会左对齐贴在气泡上方。
                 抖音的时间是横跨整个消息区居中的，只能在这一层画。 -->
            <template #item="{ item }">
              <div class="chat-row">
                <div v-if="item.showTime" class="chat-time">{{ item.timeText }}</div>
                <Bubble
                  :placement="item.placement"
                  :content="item.content"
                  :avatar="item.avatar"
                  :shape="item.shape"
                  :variant="item.variant"
                  :max-width="item.maxWidth"
                >
                  <template #content>
                    <span class="chat-text" :class="{ 'chat-text--pending': item.pending }">
                      {{ item.content }}
                    </span>
                    <div v-if="item.failed" class="chat-failed">
                      <span>发送失败</span>
                      <el-button link size="small" @click.stop="retry(item.msg)">重发</el-button>
                    </div>
                  </template>
                </Bubble>
              </div>
            </template>
          </BubbleList>
        </el-main>

        <el-footer class="im-chat__foot" height="auto">
          <XSender
            ref="senderRef"
            class="im-sender"
            placeholder="发消息…"
            :loading="sending"
            :max-length="1000"
            submit-type="enter"
            :clearable="true"
            :custom-style="senderStyle"
            @submit="submit"
          />
        </el-footer>
      </template>

      <el-empty
        v-else
        class="im-chat__placeholder"
        :image-size="80"
        description="选择左侧的联系人，或从别人的主页点「私信」开始聊天"
      />
    </el-container>
  </el-container>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Search } from '@element-plus/icons-vue'
import { Bubble, BubbleList, Conversations, XSender } from 'vue-element-plus-x'
import { listMessages, sendMessage, type Brief, type ConversationResp, type MessageResp } from '@/api/message'
import { useImStore } from '@/store/modules/im'
import { useUserStore } from '@/store/modules/user'
import { onFrame, onReconnect, wsState } from '@/utils/ws'
import { formatChatTime, formatListTime, initials, newClientMsgId } from '@/utils/format'

// 一条消息在界面上的形态。
// 在服务端返回的 MessageResp 上加了两个纯前端字段：
//   pending —— 本地已经画出来，还没等到服务端确认；
//   failed  —— 发送失败，允许用同一个 client_msg_id 重发。
type ChatMessage = MessageResp & { pending?: boolean; failed?: boolean }

// 会话列表的行。组件库只要求 id + label，其余字段由 #label 插槽自己用。
// 把 peer / unread / 时间都塞进同一个对象，插槽里就不用按 id 再查一遍。
interface ConvRow {
  id: number
  label: string
  peer: Brief
  unread: number
  timeText: string
  preview: string
}

// 气泡列表的项。BubbleList 会把 placement/avatar/content/shape 等字段透传给 Bubble，
// 所以这里的字段名不是我们随便起的，是组件库的约定。
// type 必须给一个非 "bubble"/"message" 的值：组件内部拿它区分「默认气泡」和
// 「自定义渲染」，只有后者才会走 #item 插槽。踩过这个坑——不给 type 时
// #item 完全不执行，自己写的结构和样式一条都渲染不出来，页面还看不出报错。
interface BubbleItem {
  type: 'im'
  key: string
  id: number
  placement: 'start' | 'end'
  content: string
  avatar: string
  shape: 'corner'
  variant: 'filled'
  maxWidth: string
  showTime: boolean
  timeText: string
  pending?: boolean
  failed?: boolean
  msg: ChatMessage
}

const PAGE_SIZE = 20 // 每页条数，和后端默认值保持一致
const TIME_GAP = 5 * 60 * 1000 // 相邻消息间隔超过 5 分钟就插一条时间分隔

// 会话行的三种状态样式。走组件暴露的 props 而不是 :deep 覆盖内部类名：
// 内部 DOM 结构会随版本变，props 是它承诺的接口，升级时不会静默失效。
// 高度写 auto：组件默认给固定高度，抖音那种两行文字的行会显得很挤。
const convItemStyle = {
  height: 'auto',
  padding: '8px 12px',
  margin: '2px 8px',
  borderRadius: '8px',
}
const convHoverStyle = { background: '#f7f7f8' }
const convActiveStyle = { background: '#f2f3f5' }

// XSender 的根元素样式。默认是 AI 对话框的白色大卡片，
// 这里压成抖音那种灰底圆角输入条，去掉描边。
const senderStyle = {
  background: '#f2f3f5',
  borderRadius: '20px',
  border: 'none',
}

const route = useRoute()
const router = useRouter()
const im = useImStore()
const me = useUserStore()

const keyword = ref('')
const messages = ref<ChatMessage[]>([])
const peer = ref<Brief | null>(null)
const activePeerId = ref(0)
const hasMore = ref(false)
const nextBeforeId = ref(0)
const loadingHistory = ref(false)
const loadingOlder = ref(false)
// 用计数而不是布尔：允许连按 Enter 发多条，只发完后才取消按钮的 loading。
// 用布尔的话第二条会被守卫挡住，用户会以为输入被吞了。
const sendingCount = ref(0)
const sending = computed(() => sendingCount.value > 0)
// 组件实例上只用到这两个方法：一个是把视口拉到底，一个是告诉组件「这一页加载完了」。
// 显式写出形状而不是 any：方法名写错时能在类型检查阶段暴露，而不是运行时静默无效。
const bubbleRef = ref<{
  loadMoreTopComplete: () => void
  scrollToBottom: (smooth?: boolean) => void
} | null>(null)
const senderRef = ref<{ getModelValue: () => { text: string }; clear: () => void } | null>(null)
const isMobile = ref(window.innerWidth < 768)

// 左栏列表：有关键字时按昵称/用户名过滤。
// 只过滤已加载的前 50 个会话，不做服务端搜索——私信会话数量级通常在几十以内。
const visibleRows = computed<ConvRow[]>(() => {
  const kw = keyword.value.trim().toLowerCase()
  const rows: ConvRow[] = []
  for (const row of im.conversations) {
    const name = row.peer.nickname.toLowerCase()
    const uname = row.peer.username.toLowerCase()
    if (kw && !name.includes(kw) && !uname.includes(kw)) {
      continue
    }
    rows.push({
      id: row.peer.id,
      label: row.peer.nickname,
      peer: row.peer,
      unread: row.unread,
      timeText: rowTime(row),
      preview: preview(row),
    })
  }
  return rows
})

// Conversations 的 items 与过滤后的行共用一份数据，避免两处过滤逻辑跑偏。
const convItems = computed(() => visibleRows.value)

// 气泡由 messages 派生，不在两处各存一份状态：
// 消息列表是唯一真相，组件只是换了个渲染形状。
// 时间分隔按「与上一条相隔超过 5 分钟」算，全在一条时间轴上判断。
const bubbles = computed<BubbleItem[]>(() => {
  const list: BubbleItem[] = []
  const myAvatar = me.profile ? me.profile.avatar_url : ''
  for (let i = 0; i < messages.value.length; i++) {
    const m = messages.value[i]
    let showTime = false
    if (i === 0) {
      showTime = true
    } else {
      const cur = new Date(m.created_at).getTime()
      const prev = new Date(messages.value[i - 1].created_at).getTime()
      if (!Number.isNaN(cur) && !Number.isNaN(prev) && cur - prev > TIME_GAP) {
        showTime = true
      }
    }
    let avatar = ''
    if (m.mine) {
      avatar = myAvatar
    } else if (peer.value) {
      avatar = peer.value.avatar_url
    }
    let key = 'msg-' + String(m.id)
    if (m.id === 0) {
      key = 'local-' + String(m.client_msg_id || i)
    }
    list.push({
      type: 'im',
      key,
      id: m.id,
      placement: m.mine ? 'end' : 'start',
      content: m.content,
      avatar,
      shape: 'corner',
      variant: 'filled',
      maxWidth: '62%',
      showTime,
      timeText: formatChatTime(m.created_at),
      pending: m.pending,
      failed: m.failed,
      msg: m,
    })
  }
  return list
})

// 顶部边界状态：加载中 / 还有更多 / 到底了。
// BubbleList 靠它决定顶部那行提示的文案与动画。
const topStatus = computed(() => {
  if (loadingOlder.value) {
    return { type: 'loading' as const, text: '加载中…' }
  }
  if (!hasMore.value && messages.value.length > 0) {
    return { type: 'no-more' as const, text: '没有更早的消息了' }
  }
  return null
})

// 连接状态文案。这里只说自己的连接，不能写「对方在线」——
// 后端没有下发对方的在线状态，编一个出来就是骗用户。
const connectionText = computed(() => {
  if (wsState.connected) {
    return ''
  }
  return '连接中断，正在重连…'
})

function bubbleKey(item: BubbleItem): string {
  return item.key
}

function rowTime(row: ConversationResp): string {
  if (!row.last_message) {
    return ''
  }
  return formatListTime(row.last_message.created_at)
}

// 会话摘要：自己的最后一条加「我：」前缀，和微信/抖音一致，
// 一眼能分清最后一句是谁说的。
function preview(row: ConversationResp): string {
  const last = row.last_message
  if (!last) {
    return '还没有消息'
  }
  const text = last.content.replace(/\s+/g, ' ')
  if (last.mine) {
    return '我：' + text
  }
  return text
}

// onConvChange 是「点会话列表某一行」。真正的加载交给路由参数的变化，
// 这样直接访问 /messages/123 和点列表走的是同一条路径，不会有两套逻辑。
function onConvChange(item: ConvRow) {
  if (!item || item.id === activePeerId.value) {
    return
  }
  router.push('/messages/' + String(item.id))
}

function backToList() {
  router.push('/messages')
}

// loadConversation 拉「与某人的第一页」。
//
// 三个细节：
//   1. 会话可能根本不存在（从主页点私信、从没聊过），后端返回空列表 + peer，正常渲染；
//   2. 请求期间用户可能切到别人，回来时要丢掉过期结果，否则会把 A 的消息画到 B 的窗口；
//   3. 进来就把未读清零，用户已经在看这些消息了，红点不该留着。
async function loadConversation(peerId: number) {
  if (peerId <= 0) {
    peer.value = null
    messages.value = []
    return
  }
  activePeerId.value = peerId
  peer.value = null
  messages.value = []
  hasMore.value = false
  nextBeforeId.value = 0
  loadingHistory.value = true
  try {
    const data = await listMessages(peerId, 0, PAGE_SIZE)
    if (activePeerId.value !== peerId) {
      return // 已经切走了
    }
    peer.value = data.peer
    messages.value = data.list.map((m) => ({ ...m }))
    hasMore.value = data.has_more
    nextBeforeId.value = data.next_before_id
    // 从主页进来的人可能还没会话，补一行让左栏有选中态。
    im.ensureRow(data.peer)
    void im.markRead(peerId)
    // 首屏直接落到底部：聊天窗口打开就该看到最新一句。
    await nextTick()
    bubbleRef.value?.loadMoreTopComplete()
    scrollToBottom(false)
  } catch {
    // 拦截器已经提示过，这里保持空态即可。
  } finally {
    if (activePeerId.value === peerId) {
      loadingHistory.value = false
    }
  }
}

// loadOlder 是 BubbleList 滚到顶部时触发的「往上翻一页」。
//
// 滚动位置由组件负责保持：它知道新内容插到了顶部，
// 会把视口按插入高度补回去。这里只管取数据与去重。
async function loadOlder() {
  if (loadingOlder.value || !hasMore.value || activePeerId.value <= 0) {
    bubbleRef.value?.loadMoreTopComplete()
    return
  }
  const peerId = activePeerId.value
  loadingOlder.value = true
  try {
    const data = await listMessages(peerId, nextBeforeId.value, PAGE_SIZE)
    if (activePeerId.value !== peerId) {
      return
    }
    // 后端保证游标之前的消息不会和其他页重叠，这里仍然按 id 去重一次：
    // 万一上一页之后又有新消息插入，重复的 id 会让列表 key 冲突。
    const seen = new Set<number>()
    for (const m of messages.value) {
      if (m.id > 0) {
        seen.add(m.id)
      }
    }
    const older: ChatMessage[] = []
    for (const m of data.list) {
      if (!seen.has(m.id)) {
        older.push({ ...m })
      }
    }
    messages.value = older.concat(messages.value)
    hasMore.value = data.has_more
    nextBeforeId.value = data.next_before_id
  } catch {
    // 失败保持原状，用户再滑一次就会重试。
  } finally {
    loadingOlder.value = false
    bubbleRef.value?.loadMoreTopComplete()
  }
}

function scrollToBottom(smooth: boolean) {
  bubbleRef.value?.scrollToBottom(smooth)
}

// syncLatest 在断线重连后补拉一次「最新一页」，把断线期间漏掉的消息接上。
// 只追加比当前最后一条更新的消息，不动已经加载的历史。
async function syncLatest() {
  const peerId = activePeerId.value
  if (peerId <= 0) {
    return
  }
  try {
    const data = await listMessages(peerId, 0, PAGE_SIZE)
    if (activePeerId.value !== peerId) {
      return
    }
    let lastId = 0
    for (const m of messages.value) {
      if (m.id > lastId) {
        lastId = m.id
      }
    }
    const extra: ChatMessage[] = []
    for (const m of data.list) {
      if (m.id > lastId) {
        extra.push({ ...m })
      }
    }
    if (extra.length === 0) {
      return
    }
    messages.value = messages.value.concat(extra)
    void im.markRead(peerId)
    await nextTick()
    scrollToBottom(true)
  } catch {
    // 补拉失败不影响已有内容，下次真正刷新时会补齐。
  }
}

// onIncoming 处理「别人发给我」的实时消息。
//
// 只有当前正在看的这个人发来的才追加气泡；其余的在 im store 里已经变成了
// 左栏的摘要和红点，不需要在这里处理。
function onIncoming(payload: { message: MessageResp; sender?: Brief; unread: number }) {
  const msg = payload.message
  if (!msg || msg.sender_id !== activePeerId.value) {
    return
  }
  // 同一条消息可能被重投（推送未 ACK 会重试），按 id 去重。
  for (const m of messages.value) {
    if (m.id === msg.id) {
      return
    }
  }
  messages.value.push({ ...msg, mine: false })
  // 正在看这个会话，服务端的未读已经加过了，立刻清掉。
  void im.markRead(activePeerId.value)
  // 自动贴底由 BubbleList 判断：用户正往上翻历史时它不会强行拉到底。
  void nextTick(() => {
    scrollToBottom(true)
  })
}

// submit 发送一条消息。
//
// 先插一条本地「发送中」的气泡再发请求：
// 等一个往返才看到自己发的字，体感上像是输入被吞了。
// 幂等键在发送时生成好，重发用的还是同一个值，服务端不会落两条。
async function submit() {
  const sender = senderRef.value
  if (!sender) {
    return
  }
  const model = sender.getModelValue()
  const content = model.text.trim()
  const target = peer.value
  if (!content || !target) {
    return
  }
  sender.clear()
  const clientMsgId = newClientMsgId()
  // 记住发出时对话的是谁：请求飞行途中用户可能切到别人，
  // 回来时把结果写进新会话的列表，就会显示成一堆别人的消息。
  const targetId = target.id
  const local: ChatMessage = {
    id: 0,
    sender_id: me.userId,
    content,
    read_at: null,
    created_at: new Date().toISOString(),
    mine: true,
    client_msg_id: clientMsgId,
    pending: true,
  }
  messages.value.push(local)
  sendingCount.value += 1
  await nextTick()
  scrollToBottom(true)

  try {
    const saved = await sendMessage(targetId, content, clientMsgId)
    if (activePeerId.value === targetId) {
      replaceLocal(clientMsgId, { ...saved })
    }
    // 左栏摘要无论如何都要更新：消息确实已经发出去了，
    // 和当前正在看哪个会话无关。
    im.applySent(target, saved)
  } catch {
    if (activePeerId.value === targetId) {
      markFailed(clientMsgId)
    } else {
      // 已经切走的话本地那条气泡随窗口一起清掉了，但消息可能已经落库，
      // 下次进这个会话会从历史里读到，不需要额外补救。
    }
  } finally {
    sendingCount.value -= 1
  }
}

// replaceLocal 用服务端返回的消息替换本地那条。
// 按 client_msg_id 找而不是按下标：发送期间可能又插进来别的消息。
function replaceLocal(clientMsgId: string, saved: ChatMessage) {
  for (let i = 0; i < messages.value.length; i++) {
    if (messages.value[i].client_msg_id === clientMsgId) {
      messages.value[i] = saved
      return
    }
  }
  messages.value.push(saved)
}

function markFailed(clientMsgId: string) {
  for (const m of messages.value) {
    if (m.client_msg_id === clientMsgId) {
      m.pending = false
      m.failed = true
      return
    }
  }
}

// retry 重发失败的消息，复用原来的幂等键。
// 如果上一次其实已经落库、只是响应丢了，服务端会返回同一条，不会出现两个气泡。
async function retry(m: ChatMessage) {
  const target = peer.value
  if (!target || !m.client_msg_id) {
    return
  }
  const targetId = target.id
  const clientMsgId = m.client_msg_id
  m.failed = false
  m.pending = true
  try {
    const saved = await sendMessage(targetId, m.content, clientMsgId)
    if (activePeerId.value === targetId) {
      replaceLocal(clientMsgId, { ...saved })
    }
    im.applySent(target, saved)
  } catch {
    if (activePeerId.value === targetId) {
      markFailed(clientMsgId)
    }
  }
}

function onResize() {
  isMobile.value = window.innerWidth < 768
}

// 路由参数是唯一入口：点列表、直接输网址、浏览器后退都走这里。
watch(
  () => route.params.userId,
  (value) => {
    const raw = Array.isArray(value) ? value[0] : value
    const id = Number(raw || 0)
    if (id > 0) {
      void loadConversation(id)
      return
    }
    activePeerId.value = 0
    peer.value = null
    messages.value = []
  },
)

let offFrame: (() => void) | null = null
let offReconnect: (() => void) | null = null

onMounted(async () => {
  window.addEventListener('resize', onResize)
  im.initWS()
  offFrame = onFrame('message.created', onIncoming)
  offReconnect = onReconnect(() => {
    void syncLatest()
  })

  // 会话列表和当前会话并行拉：列表用来画左栏，会话用来画右栏，互不依赖。
  if (!im.loaded) {
    void im.refresh()
  }
  const raw = route.params.userId
  const id = Number((Array.isArray(raw) ? raw[0] : raw) || 0)
  if (id > 0) {
    await loadConversation(id)
  }
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize)
  if (offFrame) {
    offFrame()
  }
  if (offReconnect) {
    offReconnect()
  }
})
</script>

<style scoped>
/* 抖音私信的取色：主色是偏亮的天蓝，气泡与按钮共用；
   文字用近黑（#161823）而不是纯黑，纯黑在大面积白底上偏硬。 */
.im-page {
  height: 100%;
  --im-primary: #1c7dff;
  --im-text: #161823;
  --im-text-weak: #8a8b91;
  --im-fill: #f2f3f5;
  --el-color-primary: #1c7dff;
  background: #fff;
}

/* 左栏固定 320：窄到放不下摘要，宽了挤压聊天区。 */
.im-aside {
  display: flex;
  flex-direction: column;
  border-right: 1px solid #f0f0f0;
  background: #fff;
  overflow: hidden;
}

.im-aside__head {
  padding: 16px 16px 8px;
}

.im-aside__title {
  font-size: 20px;
  font-weight: 700;
  color: var(--im-text);
  margin-bottom: 10px;
}

/* 搜索框做成灰底无边框，是抖音那种「一块圆角灰条」的观感。
   Element Plus 默认的描边输入框在这里会显得很重。 */
.im-aside__search :deep(.el-input__wrapper) {
  background: var(--im-fill);
  box-shadow: none;
  border-radius: 18px;
  padding: 0 12px;
}

.im-aside__search :deep(.el-input__wrapper.is-focus) {
  box-shadow: 0 0 0 1px var(--im-primary) inset;
}

.im-conv {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

/* 行内容：头像 + 两行文字 + 右侧时间/红点。 */
.conv {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 4px 2px;
  min-width: 0;
}

/* 未读红点压在头像右上角内侧。
   el-badge 默认把点推到容器外侧（translateX(100%)），
   而会话列表为了文字省略在行上设了 overflow 裁剪，外侧那半个点会被切掉——
   表现是「有未读但看不见红点」。改成内侧定位就不依赖容器是否裁剪。 */
.conv__badge :deep(.el-badge__content.is-dot) {
  background: #fe2c55;
  border: 2px solid #fff;
  width: 10px;
  height: 10px;
  top: 2px;
  right: 2px;
  transform: none;
}

.conv__body {
  flex: 1;
  min-width: 0;
}

.conv__line {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.conv__name {
  font-size: 15px;
  font-weight: 600;
  color: var(--im-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.conv__time {
  flex: 0 0 auto;
  font-size: 12px;
  color: var(--im-text-weak);
}

.conv__preview {
  margin-top: 3px;
  font-size: 13px;
  color: var(--im-text-weak);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.im-aside__empty {
  padding: 24px 0;
}

.im-chat {
  display: flex;
  flex-direction: column;
  min-width: 0;
  background: #fff;
}

.im-chat__head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 20px;
  border-bottom: 1px solid #f0f0f0;
}

.im-chat__back {
  padding: 4px 6px;
}

.im-chat__who {
  min-width: 0;
}

.im-chat__name {
  font-size: 15px;
  font-weight: 600;
  color: var(--im-text);
  line-height: 1.3;
}

.im-chat__hint {
  font-size: 12px;
  color: #e6a23c;
  min-height: 16px;
}

/* 消息区自己滚，页面不跟着滚；el-main 默认 20px 内边距在这里要去掉，
   否则气泡区左右各缩进一块，和顶部不对齐。 */
.im-chat__body {
  flex: 1;
  min-height: 0;
  padding: 8px 20px;
  overflow: hidden;
}

.im-bubbles {
  height: 100%;
}

.chat-row {
  width: 100%;
}

.chat-time {
  width: 100%;
  text-align: center;
  font-size: 12px;
  color: #b0b1b6;
  padding: 10px 0 6px;
}

.chat-text {
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 15px;
  line-height: 1.5;
}

/* 发送中半透明：还没落库，避免用户以为对方已经看到了 */
.chat-text--pending {
  opacity: 0.55;
}

.chat-failed {
  margin-top: 4px;
  font-size: 12px;
  color: #f56c6c;
  display: flex;
  align-items: center;
  gap: 6px;
}

/* 气泡配色覆盖。组件库的类名是 elx- 前缀（不是 Element Plus 的 el-），
   BEM 结构：elx-bubble / elx-bubble--end / elx-bubble__content。
   它的默认规则写在「content-wrapper + content」两层选择器上，
   所以这里也要带上 wrapper，否则特异性不够、background 会被它盖掉。
   这是实测出来的：只写 .elx-bubble__content 时，自己的气泡仍然是灰的。 */
.im-bubbles :deep(.elx-bubble__content-wrapper .elx-bubble__content) {
  border-radius: 18px;
  padding: 9px 14px;
  background: var(--im-fill);
  color: var(--im-text);
  /* 组件默认给气泡算了 46px 的最小高度（12px 上下内边距 + 一行文字），
     一条「好」也占那么高，几条短消息之间会显得空得厉害。
     置 0 让高度由内容决定，接近抖音那种紧凑的排布。 */
  min-height: 0;
}

.im-bubbles :deep(.elx-bubble--end .elx-bubble__content-wrapper .elx-bubble__content) {
  background: var(--im-primary);
  color: #fff;
}

.im-chat__foot {
  padding: 10px 20px 14px;
  border-top: 1px solid #f0f0f0;
}

.im-chat__placeholder {
  margin: auto;
}

/* 窄屏：左栏和聊天区一次只显示一个，
   用 activePeerId 决定显示哪个，比抽屉组件少一层状态。 */
.is-hidden-mobile {
  display: none;
}

@media (max-width: 767px) {
  .im-aside {
    width: 100% !important;
    flex: 1 1 100%;
    border-right: none;
  }

  .im-chat__body {
    padding: 8px 12px;
  }
}
</style>
