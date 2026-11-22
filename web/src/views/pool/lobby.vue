<template>
  <div class="pl">
    <header class="pl__head">
      <h1 class="pl__title">台球对战</h1>
      <div class="pl__tools">
        <button class="pl__tool" type="button" @click="toggleJoin">
          {{ joinOpen ? '收起' : '输入房号' }}
        </button>
        <router-link class="pl__tool" to="/pool/records">我的战绩</router-link>
      </div>
    </header>

    <!-- 输房号是「偶尔用一次」的入口，收进按钮后面：常驻一行输入框
         会一直占着页面最显眼的位置，而大部分人是从在线名单里点人。 -->
    <form v-if="joinOpen" class="pl__join" @submit.prevent="onJoin">
      <input
        ref="joinInput"
        v-model="joinCode"
        class="pl__join-input"
        type="text"
        inputmode="numeric"
        autocomplete="off"
        maxlength="6"
        placeholder="六位房号"
        @input="onJoinInput"
      />
      <button class="pl__join-btn" type="submit" :disabled="joinBusy || joinCode.length !== 6">
        {{ joinBusy ? '进入中…' : '进入' }}
      </button>
    </form>

    <!-- 等待对方接受邀请。比房间卡靠前：发出邀请后最想知道的是「他到底看没看到」，
         而房间卡一直在那儿，早两秒晚两秒没关系。 -->
    <section v-if="outgoing" class="pl__waiting">
      <span class="pl__waiting-label">已邀请 {{ outgoingName }}，等对方接受</span>
      <div class="pl__waiting-side">
        <span class="pl__waiting-tip">{{ waitLeft }} 秒后失效</span>
        <button class="pl__cancel" type="button" @click="pool.cancelOutgoing()">先不等了</button>
      </div>
    </section>

    <!-- 在别人的房间里没有名单（也不该从这里邀请第三个人），
         右栏整块收起，房间卡占满整行。 -->
    <div class="pl__body" :class="{ 'is-solo': !isOwner }">
      <div class="pl__main">
        <!-- 我的房间。这一块是整页的主角：点进台球就该看见自己的头像、
             对面那个空位，以及一串能被念出去的数字。 -->
        <section class="pl__room">
          <div class="pl__room-head">
            <h2 class="pl__room-title">{{ isOwner ? '我的房间' : '对方的房间' }}</h2>
            <div class="pl__code">
              <span class="pl__code-label">房号</span>
              <span class="pl__code-value">{{ roomCode || '——' }}</span>
              <button v-if="roomCode" class="pl__copy" type="button" @click="copyCode">
                {{ copied ? '已复制' : '复制' }}
              </button>
            </div>
          </div>

          <!-- 开房失败与「房间还没回来」要分开：前者要有重试入口，
               后者只是慢一拍，弹个错误反而更慌。 -->
          <p v-if="roomFailed" class="pl__empty">
            房间没开出来。<button class="pl__retry" type="button" @click="retryRoom">重试</button>
          </p>

          <div v-else-if="!room" class="pl__room-skeleton">
            <el-skeleton :rows="3" animated />
          </div>

          <template v-else>
            <div class="pl__seats">
              <!-- 自己这一侧永远在左，不管服务端分配的座位号是 0 还是 1。 -->
              <article class="pl__seat is-me" :class="{ 'is-ready': myReady }">
                <div class="pl__seat-tag">你</div>
                <el-avatar
                  :size="56"
                  :src="me?.avatar || undefined"
                  :style="{ backgroundColor: avatarColor(me?.nickname || '') }"
                >
                  {{ initialOf(me?.nickname) }}
                </el-avatar>
                <span class="pl__seat-name">{{ me?.nickname || '我' }}</span>
                <span class="pl__seat-ready">{{ myReady ? '已准备' : '未准备' }}</span>
              </article>

              <!-- 空位用虚线框：一眼能看出「这里还能坐一个人」，
                   而不是「对手掉线了」。 -->
              <article v-if="otherEmpty" class="pl__seat is-empty">
                <div class="pl__seat-tag">空位</div>
                <div class="pl__seat-ghost">+</div>
                <span class="pl__seat-name">等一个人</span>
              </article>

              <article v-else class="pl__seat" :class="{ 'is-ready': otherReady }">
                <div class="pl__seat-tag">{{ isOwner ? '对手' : '房主' }}</div>
                <el-avatar
                  :size="56"
                  :src="other?.avatar || undefined"
                  :style="{ backgroundColor: avatarColor(other?.nickname || '') }"
                >
                  {{ initialOf(other?.nickname) }}
                </el-avatar>
                <span class="pl__seat-name">{{ other?.nickname || '对手' }}</span>
                <span class="pl__seat-ready">{{ otherReady ? '已准备' : '未准备' }}</span>
              </article>
            </div>

            <div class="pl__actions">
              <button
                class="pl__ready"
                type="button"
                :class="{ 'is-cancel': myReady }"
                :disabled="!canReady"
                @click="toggleReady"
              >
                {{ readyLabel }}
              </button>
              <!-- 客人给「退出」，房主给「换一间」：
                   对面可能早就关了页面，房主不该被一间凑不齐的房锁住。 -->
              <button
                v-if="!isOwner"
                class="pl__leave"
                type="button"
                :disabled="leaving"
                @click="onLeave"
              >
                {{ leaving ? '退出中…' : '退出' }}
              </button>
              <button v-else class="pl__leave" type="button" :disabled="reopening" @click="onReopen">
                {{ reopening ? '换房中…' : '换一间' }}
              </button>
            </div>
          </template>
        </section>

        <!-- 正在进行的对局。没有人在打时整块不出现：
             一块永远空着的「现在没有对局」比不显示更占地方。
             但刷新失败时仍然要出现，否则用户没有任何地方能重试。 -->
        <section v-if="live.length > 0 || liveFailed" class="pl__live">
          <div class="pl__panel-head">
            <h2 class="pl__panel-title">正在进行的对局</h2>
            <button v-if="liveFailed" class="pl__retry" type="button" @click="retryLive">
              刷新失败，重试
            </button>
            <span v-else class="pl__panel-count">{{ live.length }} 场</span>
          </div>

          <p v-if="live.length === 0" class="pl__empty">对局列表没加载出来。刷新一下试试。</p>

          <ul v-else class="pl__list">
            <li v-for="item in live" :key="item.match_id" class="pl__row">
              <div class="pl__who">
                <span class="pl__name">
                  {{ nameOfPlayer(item, 0) }} vs {{ nameOfPlayer(item, 1) }}
                </span>
                <span class="pl__handle">
                  第 {{ item.shot_seq }} 杆 · {{ item.spectators }}/{{ item.spectators_max }} 人
                </span>
              </div>
              <!-- 自己正在打的那一局不能给「观战」：观战连接会以玩家身份
                   被服务端安排到座位上，等于把自己的对局连接顶掉。 -->
              <router-link
                v-if="isMine(item)"
                class="pl__watch"
                :to="'/pool/match/' + String(item.match_id)"
              >
                回到对局
              </router-link>
              <router-link
                v-else-if="item.spectators < item.spectators_max"
                class="pl__watch"
                :to="'/pool/watch/' + String(item.match_id)"
              >
                观战
              </router-link>
              <span v-else class="pl__watch is-full">已满</span>
            </li>
          </ul>
        </section>
      </div>

      <!-- 在线名单竖在右边并单独滚动：人多的时候页面不该被这张名单拉着一起变长。
           只列全部在线的（关注的人排前面）——台球不该是互相关注才能玩的游戏。 -->
      <aside v-if="isOwner" class="pl__side">
        <div class="pl__panel-head">
          <h2 class="pl__panel-title">在线</h2>
          <span v-if="online.length > 0" class="pl__panel-count">{{ online.length }}</span>
        </div>

        <div class="pl__side-body">
          <div v-if="onlineLoading && online.length === 0" class="pl__skeleton">
            <el-skeleton :rows="3" animated />
          </div>

          <p v-else-if="onlineFailed" class="pl__empty">
            名单没加载出来。<button class="pl__retry" type="button" @click="retryOnline">重试</button>
          </p>

          <p v-else-if="online.length === 0" class="pl__empty">暂无其他人在线</p>

          <ul v-else class="pl__list">
            <li v-for="person in online" :key="person.user_id" class="pl__row">
              <el-avatar
                :size="36"
                :src="person.avatar || undefined"
                :style="{ backgroundColor: avatarColor(person.nickname) }"
              >
                {{ initialOf(person.nickname) }}
              </el-avatar>
              <span class="pl__name">{{ person.nickname || '有人' }}</span>
              <!-- 正在邀请的那个人按钮变成「已发送」并禁用。
                   不禁用的话连点两下会拿到 409，用户看到一句「你已有一条待接受的邀请」
                   却不知道说的是哪一条。 -->
              <button
                class="pl__invite"
                type="button"
                :disabled="busyId === person.user_id || isWaitingOn(person.user_id)"
                @click="onInvite(person)"
              >
                {{ isWaitingOn(person.user_id) ? '已发送' : busyId === person.user_id ? '发送中…' : '邀请' }}
              </button>
            </li>
          </ul>
        </div>
      </aside>
    </div>
  </div>
</template>

<script setup lang="ts">
// 台球大厅，同时也是「我的房间」。
//
// 页面上是三件同一层意思的事：
//   - 我的房间：自己的头像 + 对面那个空位 + 房号。房号一报，人就能进来；
//   - 输房号：别人把房号报给我时，点右上角那个按钮进他的房间；
//   - 在线名单：懒得报数字就点一个人邀请他，走推送把他叫过来。
//
// 准备状态与开局都在这一页完成：双方都点准备，状态翻成 playing，直接跳对局页。
// 中间不再有单独的「房间页」——多一层跳转只会让人找不到自己的房号。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  getLiveMatches,
  getMyRoom,
  getOnlineUsers,
  joinRoom,
  leaveRoom,
  reopenRoom,
  setReady,
  type LiveMatch,
  type OnlineUser,
  type PoolRoom,
} from '@/api/pool'
import { usePoolStore } from '@/store/modules/pool'
import { useUserStore } from '@/store/modules/user'
import { avatarColor } from '@/utils/avatar'
import { initials } from '@/utils/format'

const pool = usePoolStore()
const user = useUserStore()
const router = useRouter()

// ── 我的房间 ─────────────────────────────────────────────

const room = ref<PoolRoom | null>(null)
// roomFailed 只表示「这一次开房请求失败了」，不是「没有房间」：
// 后者是 room === null 且没在失败态，属于正在加载。
const roomFailed = ref(false)
const busyReady = ref(false)
const leaving = ref(false)
const reopening = ref(false)
const copied = ref(false)

// 轮询间隔分两档，按「对面有没有人」切换。
//
// 有人在对面时两秒：准备是人的动作，差两秒看到结果可以接受，
// 再慢就会出现「对方已经准备好几秒了，我这边还没变」的错位感。
//
// 只有自己时十秒：对面进人这件事由 pool_room_joined 推送告诉我们
// （见 store 的 roomTick），快轮询只是白烧请求——而接口限流是按 IP 的
// 令牌桶（容量 10、每秒补 2 个），大厅每两秒一发会把别的页面的额度挤掉。
const ROOM_POLL_MS = 2000
const ROOM_IDLE_POLL_MS = 10000

let roomTimer: ReturnType<typeof setInterval> | null = null
// roomPolling 挡住重入：一次请求没回来就不再发下一次，
// 否则服务端慢的时候会堆起一串请求，最后按乱序覆盖状态。
let roomPolling = false
// roomSeq 是请求代际。弱网下请求会叠起来，只认最新一次的结果。
let roomSeq = 0
let roomDisposed = false

const roomCode = computed(() => room.value?.room_code || '')
const isOwner = computed(() => (room.value ? room.value.seat === 0 : true))

// me / other 是按「我坐哪」取的两侧资料。
//
// 服务端按座位 0/1 给 players，直接按这个顺序摆会让一半用户看到自己在右边。
const me = computed(() => {
  if (!room.value) {
    return null
  }
  return room.value.players[room.value.seat]
})

const other = computed(() => {
  if (!room.value) {
    return null
  }
  return room.value.players[room.value.seat === 0 ? 1 : 0]
})

// 空位判定看 user_id：服务端用 0 表示「这个座位还没人」。
const otherEmpty = computed(() => !other.value || other.value.user_id === 0)

const myReady = computed(() => {
  if (!room.value) {
    return false
  }
  return room.value.ready[room.value.seat]
})

const otherReady = computed(() => {
  if (!room.value) {
    return false
  }
  return room.value.ready[room.value.seat === 0 ? 1 : 0]
})

// canReady：对面没人的时候点准备没有意义——两个座位都得准备才开局。
const canReady = computed(() => {
  if (!room.value || room.value.status !== 'waiting') {
    return false
  }
  return !otherEmpty.value || myReady.value
})

const readyLabel = computed(() => {
  if (myReady.value) {
    return '取消准备'
  }
  if (otherEmpty.value) {
    return '等对手进来'
  }
  return '准备'
})

// initialOf 取昵称首字，头像里显示的那个字。
function initialOf(name?: string): string {
  if (!name) {
    return ''
  }
  return initials(name)
}

// applyRoom 用服务端返回的快照整份替换本地状态。
//
// 不做增量合并：两份状态一旦分叉，页面会一直显示错的准备标记，
// 而这份数据本来就很小，整份换掉最省事也最不容易错。
function applyRoom(next: PoolRoom) {
  room.value = next
  if (next.status === 'playing') {
    stopRoomTimer()
    void router.replace('/pool/match/' + String(next.match_id))
    return
  }
  // 对面从「空着」变成「有人」时把表切到快档，反之切回慢档。
  startRoomTimer()
}

// loadRoom 拉一次我的房间。失败不是致命错误，等下一轮再试。
//
// 轮询走 getMyRoom 而不是 getRoom(id)：房间可能被后台清扫收掉
// （见后端 pool.Manager 的清扫规则），这时 getRoom 会一直 404，
// 而 getMyRoom 会按库里那条还在 waiting 的行把房间重建出来，自己就恢复了。
async function loadRoom() {
  if (roomPolling) {
    return
  }
  roomPolling = true
  const seq = roomSeq + 1
  roomSeq = seq
  try {
    const next = await getMyRoom()
    if (roomDisposed || seq !== roomSeq) {
      return
    }
    roomFailed.value = false
    applyRoom(next)
  } catch {
    // 拦截器已经弹过提示。保留上一份房间（比整块消失好），
    // 失败态只用来决定要不要给重试入口。
    if (roomDisposed || seq !== roomSeq) {
      return
    }
    roomFailed.value = true
  } finally {
    roomPolling = false
  }
}

// roomInterval 记着当前表的间隔，用来判断要不要重起。
let roomInterval = 0

// startRoomTimer 按当前状态起表：间隔不合适就重起一张。
function startRoomTimer() {
  const want = otherEmpty.value ? ROOM_IDLE_POLL_MS : ROOM_POLL_MS
  if (roomTimer && roomInterval === want) {
    return
  }
  stopRoomTimer()
  roomInterval = want
  roomTimer = setInterval(() => {
    void loadRoom()
  }, want)
}

function stopRoomTimer() {
  if (roomTimer) {
    clearInterval(roomTimer)
    roomTimer = null
  }
  roomInterval = 0
}

// retryRoom 手动重试：立刻拉一次，并确保表在跑。
function retryRoom() {
  roomFailed.value = false
  void loadRoom()
  startRoomTimer()
}

// toggleReady 切换自己的准备状态。
//
// 接口返回整份房间状态，直接 apply：不做「本地先翻转再等服务端确认」的乐观更新，
// 准备按钮点错一次只是等一下，而乐观更新在失败时会留下一个假的「已准备」。
async function toggleReady() {
  if (busyReady.value || !room.value) {
    return
  }
  busyReady.value = true
  try {
    const next = await setReady(room.value.match_id, !myReady.value)
    applyRoom(next)
  } catch {
    // 拦截器已提示。下一轮轮询会把真实状态拉回来。
  } finally {
    busyReady.value = false
  }
}

// onLeave 退出别人的房间，然后重新拿一间自己的。
async function onLeave() {
  if (leaving.value) {
    return
  }
  leaving.value = true
  try {
    await leaveRoom()
    // 退房之后「我的房间」变成自己在座位 0 的房，重新拉一次就到位了。
    await loadRoom()
  } catch {
    // 拦截器已提示。
  } finally {
    leaving.value = false
  }
}

// onReopen 换一间新房：旧的那间作废，房号会变。
//
// 对面可能早就关了页面（断网、直接关机），而房间只要还有人轮询就一直活着。
// 没有这个出口，房主只能等两小时那条清扫规则。
async function onReopen() {
  if (reopening.value) {
    return
  }
  reopening.value = true
  try {
    const next = await reopenRoom()
    applyRoom(next)
    ElMessage.success('换好了，新的房号是 ' + next.room_code)
  } catch {
    // 拦截器已提示（对局进行中 / 服务不可用）。
  } finally {
    reopening.value = false
  }
}

// copyCode 复制房号。手机上要把这串数字发出去，手打六位很容易错。
async function copyCode() {
  const code = roomCode.value
  if (!code) {
    return
  }
  try {
    await navigator.clipboard.writeText(code)
    copied.value = true
    setTimeout(() => {
      copied.value = false
    }, 2000)
  } catch {
    // 非 HTTPS 环境（或浏览器不给权限）没有 clipboard，这时什么都不做：
    // 房号本来就在屏幕上，看得见就能手抄。
  }
}

// ── 输房号进别人的房间 ────────────────────────────────────

const joinOpen = ref(false)
const joinCode = ref('')
const joinBusy = ref(false)
const joinInput = ref<HTMLInputElement | null>(null)

// toggleJoin 展开 / 收起输入条。展开时清空上一次没输完的房号，
// 并把焦点直接放进输入框——手机上少点一次。
function toggleJoin() {
  joinOpen.value = !joinOpen.value
  if (!joinOpen.value) {
    return
  }
  joinCode.value = ''
  void nextTick(() => {
    joinInput.value?.focus()
  })
}

// onJoinInput 只留数字：房号是六位数字，粘进来一串带空格的东西时
// 直接过滤比提交后报「房号是六位数字」省一次往返。
function onJoinInput(event: Event) {
  const input = event.target as HTMLInputElement
  joinCode.value = input.value.replace(/\D/g, '').slice(0, 6)
  input.value = joinCode.value
}

async function onJoin() {
  if (joinBusy.value || joinCode.value.length !== 6) {
    return
  }
  joinBusy.value = true
  try {
    const next = await joinRoom(joinCode.value)
    joinCode.value = ''
    joinOpen.value = false
    applyRoom(next)
    ElMessage.success('进房了，两边都点准备就开始')
  } catch {
    // 房号不对 / 房间已开局 / 座位被占：服务端给的中文说明由拦截器弹出，
    // 这里不清输入框——多数情况是把某一位打错了，改一位再点比重新输入快。
  } finally {
    joinBusy.value = false
  }
}

// ── 在线的人 ─────────────────────────────────────────────

const online = ref<OnlineUser[]>([])
const onlineLoading = ref(false)
const onlineFailed = ref(false)
const busyId = ref(0)

// ONLINE_POLL_MS 是刷新在线名单的间隔。
//
// 名单会变：朋友可能刚上线，也可能刚开了一局（那就不该再邀请他）。
// 十秒一次与观战列表同频，两个请求加起来也远低于限流桶的补充速度。
const ONLINE_POLL_MS = 10 * 1000
// ONLINE_MAX_MISSES 与观战列表同理：连续两次不通就停表，等用户点重试。
const ONLINE_MAX_MISSES = 2

let onlineTimer: ReturnType<typeof setInterval> | null = null
let onlineMisses = 0
// onlineSeq 与房间轮询同理：弱网下后发的响应可能先到，
// 只认最新一次，免得旧名单盖掉新名单。
let onlineSeq = 0

// loadOnline 拉一次在线名单。
//
// 只在第一次（手里还没有名单）时显示骨架屏：每十秒把列表换成骨架会让人
// 以为页面在闪。后续刷新失败也不清空名单——上一份总比空白有用。
async function loadOnline() {
  const seq = onlineSeq + 1
  onlineSeq = seq
  if (online.value.length === 0) {
    onlineLoading.value = true
  }
  try {
    const list = await getOnlineUsers()
    if (seq !== onlineSeq) {
      return
    }
    online.value = list
    onlineFailed.value = false
    onlineMisses = 0
  } catch {
    if (seq !== onlineSeq) {
      return
    }
    onlineMisses += 1
    onlineFailed.value = true
    if (onlineMisses >= ONLINE_MAX_MISSES) {
      stopOnlineTimer()
    }
  } finally {
    if (seq === onlineSeq) {
      onlineLoading.value = false
    }
  }
}

function stopOnlineTimer() {
  if (onlineTimer) {
    clearInterval(onlineTimer)
    onlineTimer = null
  }
}

function startOnlineTimer() {
  if (onlineTimer) {
    return
  }
  onlineTimer = setInterval(() => {
    void loadOnline()
  }, ONLINE_POLL_MS)
}

// retryOnline 手动重试：先补一次，再把表重新起上。
// 不先清 onlineFailed：面板靠它决定显示「没加载出来」还是空状态，
// 先清会让文案在请求回来之前跳一次。
function retryOnline() {
  onlineMisses = 0
  void loadOnline()
  startOnlineTimer()
}

// onInvite 邀请一个人。成功就由 store 记住「发给谁」，
// 那一行据此显示成「已发送」；失败（409 已有待接受邀请、400 对方已离线）
// 交给拦截器提示。
async function onInvite(person: OnlineUser) {
  if (busyId.value !== 0) {
    return
  }
  busyId.value = person.user_id
  try {
    await pool.invite(person.user_id)
  } catch {
    // 拦截器已提示。
  } finally {
    busyId.value = 0
  }
}

// ── 等待对方接受 ─────────────────────────────────────────

const outgoing = computed(() => pool.outgoing)

// outgoingName 取被邀请人的昵称。在线名单里找不到（对方刚掉线）时说「对方」。
const outgoingName = computed(() => {
  const id = pool.outgoingTo
  for (const person of online.value) {
    if (person.user_id === id) {
      return person.nickname || '对方'
    }
  }
  return '对方'
})

// waitLeft 是等待卡上的剩余秒数，按 store 里记下的本地截止时刻倒数。
const waitLeft = ref(0)
let waitTimer: ReturnType<typeof setInterval> | null = null

function stopWaitTimer() {
  if (waitTimer) {
    clearInterval(waitTimer)
    waitTimer = null
  }
}

// tick 重算剩余秒数。
//
// 不从 expires_in 逐秒减：从别的页面回到大厅时这张卡可能已经挂了几十秒，
// 按剩余量重新起表会显示「还有 60 秒」而服务端那边早就过期了。
function tick() {
  const invite = outgoing.value
  if (!invite) {
    stopWaitTimer()
    waitLeft.value = 0
    return
  }
  const left = Math.max(0, Math.ceil((invite.expires_at - Date.now()) / 1000))
  waitLeft.value = left
  if (left === 0) {
    stopWaitTimer()
    pool.cancelOutgoing()
  }
}

function isWaitingOn(userId: number): boolean {
  return outgoing.value !== null && pool.outgoingTo === userId
}

// ── 正在进行的对局（观战入口） ────────────────────────────

const live = ref<LiveMatch[]>([])
let liveTimer: ReturnType<typeof setInterval> | null = null
const liveFailed = ref(false)
let liveMisses = 0

// LIVE_POLL_MS 取得比一杆（约 20 秒）小一档：
// 一局打完就立刻从列表里消失，不至于点进去发现「对局已结束」。
const LIVE_POLL_MS = 10 * 1000
// LIVE_MAX_MISSES 是连续失败几次就停表。两次是「网络抖一下」与「真的不通」的分界。
const LIVE_MAX_MISSES = 2

let liveSeq = 0
let liveDisposed = false

// loadLive 拉一次观战列表。
async function loadLive() {
  const seq = liveSeq + 1
  liveSeq = seq
  try {
    const list = await getLiveMatches()
    if (liveDisposed || seq !== liveSeq) {
      return
    }
    live.value = list
    liveFailed.value = false
    liveMisses = 0
  } catch {
    if (liveDisposed || seq !== liveSeq) {
      return
    }
    // 拦截器已经弹过提示。保留上一份列表（总比整块消失好），
    // 但连续失手两次就停下来，等用户自己点重试。
    liveMisses += 1
    liveFailed.value = true
    if (liveMisses >= LIVE_MAX_MISSES) {
      stopLiveTimer()
    }
  }
}

function stopLiveTimer() {
  if (liveTimer) {
    clearInterval(liveTimer)
    liveTimer = null
  }
}

function startLiveTimer() {
  if (liveTimer) {
    return
  }
  liveTimer = setInterval(() => {
    void loadLive()
  }, LIVE_POLL_MS)
}

// startLive 起表。资料还没回来之前不起：
// 「哪一局是我在打」要靠 user.userId，而它是异步拉的——
// 冷启动就轮询的话，自己的局会被当成别人的局摆上「观战」，
// 点进去等于用第二条连接顶掉自己那条对局连接。
function startLive() {
  void loadLive()
  startLiveTimer()
}

// retryLive 手动重试观战列表：重置计数、立刻拉一次、必要时重新起表。
//
// 立刻拉一次是必须的：只失败过一次时表还活着，光起表等于点了没反应，
// 用户要等到下一个十秒。
// 不先清 liveFailed：面板是 v-if="live.length > 0 || liveFailed"，
// 先清会让空列表时整块消失几百毫秒再回来。等这次请求成功了再清。
function retryLive() {
  liveMisses = 0
  void loadLive()
  startLiveTimer()
}

// isMine 判断这一局里有没有自己。List 里的 players 是按座位排的定长数组。
function isMine(item: LiveMatch): boolean {
  const me = user.userId
  if (me === 0) {
    return false
  }
  return item.players[0].user_id === me || item.players[1].user_id === me
}

// nameOfPlayer 取某一侧昵称，没有资料时说「玩家 1 / 玩家 2」。
//
// players 是定长两项，但没进房的那一侧 user_id 为 0、昵称为空串，
// 而正在打的对局两边都该有资料——这里是兜住后端数据异常，不是兜住 undefined。
function nameOfPlayer(item: LiveMatch, seat: 0 | 1): string {
  const name = item.players[seat].nickname
  if (!name) {
    return '玩家 ' + String(seat + 1)
  }
  return name
}

// ── 生命周期 ─────────────────────────────────────────────

// 有人进了我的房间：推送一到就重拉一次，不用等下一轮轮询。
// 这是「只有自己时可以慢速轮询」的前提。
watch(
  () => pool.roomTick,
  () => {
    void loadRoom()
  },
)

// 观战列表等个人资料拉过一次再起表：见 startLive 里的说明。
// 用 profileLoaded 而不是 userId——资料拉失败时 userId 也是 0，
// 但那时「哪一局是我在打」同样没有答案，一样不能起表。
watch(
  () => user.profileLoaded,
  (loaded) => {
    if (loaded) {
      startLive()
    }
  },
  { immediate: true },
)

// 等待卡出现与消失都走这一条：出现就起表，消失（对方接受/拒绝、自己点了不等）就停表。
//
// immediate 不能省：从对局页「再约一局」回到大厅时，outgoing 在挂载前就已经有值，
// 不标 immediate 会停在「0 秒后失效」且不再倒数，看起来像一条已经失效的邀请。
watch(
  outgoing,
  (value) => {
    stopWaitTimer()
    if (!value) {
      waitLeft.value = 0
      return
    }
    tick()
    waitTimer = setInterval(tick, 1000)
  },
  { immediate: true },
)

onMounted(() => {
  // 进大厅先要一间房，再顺手补拉一次待接受邀请
  // （万一推送在页面还没挂载时就到了，这里能捞回来）。
  void loadRoom()
  startRoomTimer()
  void loadOnline()
  startOnlineTimer()
  void pool.refresh()
})

onBeforeUnmount(() => {
  roomDisposed = true
  liveDisposed = true
  stopRoomTimer()
  stopOnlineTimer()
  stopWaitTimer()
  stopLiveTimer()
})
</script>

<style scoped>
.pl {
  max-width: 1000px;
  margin: 0 auto;
}

/* ── 页头 ── */

.pl__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.pl__title {
  margin: 0;
  font-size: 22px;
  font-weight: 700;
  color: var(--fs-text-1);
}

.pl__tools {
  display: flex;
  align-items: center;
  gap: 8px;
}

/* 右上角两个入口做成同一种描边按钮：都是「去别处」的次要动作，
   不该抢走房间卡上「准备」的注意力。 */
.pl__tool {
  height: 32px;
  padding: 0 14px;
  font-size: 13px;
  line-height: 30px;
  color: var(--fs-text-2);
  text-decoration: none;
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius-sm);
  cursor: pointer;
}

/* ── 输房号 ── */

.pl__join {
  display: flex;
  gap: 10px;
  margin-bottom: 16px;
}

.pl__join-input {
  flex: 1;
  height: 40px;
  padding: 0 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 16px;
  letter-spacing: 3px;
  color: var(--fs-text-1);
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius-sm);
}

.pl__join-input:focus {
  border-color: var(--fs-primary);
  outline: none;
}

.pl__join-btn {
  flex: none;
  height: 40px;
  padding: 0 20px;
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-primary);
  background: var(--fs-primary-weak);
  border: none;
  border-radius: var(--fs-radius-sm);
  cursor: pointer;
}

.pl__join-btn:disabled {
  color: var(--fs-text-3);
  background: var(--fs-surface-hover);
  cursor: default;
}

/* ── 两栏骨架：房间在左，在线名单在右 ── */

.pl__body {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 264px;
  gap: 16px;
  align-items: start;
}

.pl__body.is-solo {
  grid-template-columns: minmax(0, 1fr);
}

.pl__main {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}

/* 名单跟着页面滚到顶就不再往上走，且自己单独滚动：
   人多时页面高度由房间卡决定，不会被名单拉长。 */
.pl__side {
  position: sticky;
  top: 16px;
  display: flex;
  flex-direction: column;
  max-height: calc(100vh - 32px);
  padding: 4px 14px 6px;
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius);
}

.pl__side-body {
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
}

/* ── 我的房间 ── */

.pl__room {
  padding: 16px;
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius);
}

.pl__room-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}

.pl__room-title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-text-1);
}

.pl__code {
  display: flex;
  align-items: baseline;
  gap: 6px;
}

.pl__code-label {
  font-size: 12px;
  color: var(--fs-text-3);
}

/* 房号是这一页最重要的信息，用等宽字体加字距：
   它经常要被念出来，普通字体下 0 与 6、1 与 7 容易混。 */
.pl__code-value {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 20px;
  font-weight: 700;
  letter-spacing: 3px;
  color: var(--fs-text-1);
}

.pl__copy {
  padding: 2px 8px;
  font-size: 12px;
  color: var(--fs-text-2);
  background: var(--fs-surface-hover);
  border: none;
  border-radius: var(--fs-radius-sm);
  cursor: pointer;
}

.pl__room-skeleton {
  padding: 4px 0 12px;
}

.pl__seats {
  display: flex;
  align-items: stretch;
  gap: 12px;
}

.pl__seat {
  position: relative;
  display: flex;
  flex: 1;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 18px 10px 14px;
  background: var(--fs-surface-hover);
  border: 1px solid transparent;
  border-radius: var(--fs-radius);
  transition: border-color 0.2s, background-color 0.2s;
}

/* 已准备的一侧边框变主色：双方都在等对方，谁能一眼看出「就差他了」很重要。
   只改边框不改背景，是为了不让两张卡片一块深一块浅，看起来像选中了谁。 */
.pl__seat.is-ready {
  border-color: var(--fs-primary);
  background: var(--fs-primary-weak);
}

/* 空位用虚线：一眼能看出「这里还能坐一个人」，
   而不是「对手掉线了」。 */
.pl__seat.is-empty {
  background: transparent;
  border: 1px dashed var(--fs-line);
}

.pl__seat-tag {
  font-size: 11px;
  letter-spacing: 1px;
  color: var(--fs-text-3);
}

.pl__seat.is-me .pl__seat-tag {
  color: var(--fs-primary);
  font-weight: 600;
}

/* 空位的占位图形：56px 与头像同高，两张卡片的高度才对得齐。 */
.pl__seat-ghost {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 56px;
  height: 56px;
  font-size: 24px;
  color: var(--fs-text-3);
  background: var(--fs-surface-hover);
  border-radius: 50%;
}

.pl__seat-name {
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-text-1);
}

.pl__seat.is-empty .pl__seat-name {
  color: var(--fs-text-3);
  font-weight: 400;
}

.pl__seat-ready {
  font-size: 12px;
  color: var(--fs-text-3);
}

.pl__seat.is-ready .pl__seat-ready {
  color: var(--fs-primary);
  font-weight: 600;
}

.pl__actions {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 14px;
}

.pl__ready {
  flex: 1;
  height: 44px;
  font-size: 15px;
  font-weight: 600;
  color: #fff;
  background: var(--fs-primary);
  border: none;
  border-radius: 12px;
  cursor: pointer;
  transition: opacity 0.15s;
}

/* 已准备状态下的按钮改成描边：主色实心再点一次就是「取消」，
   视觉上不该比「准备」更强势。 */
.pl__ready.is-cancel {
  color: var(--fs-primary);
  background: var(--fs-primary-weak);
}

.pl__ready:disabled {
  opacity: 0.45;
  cursor: default;
}

.pl__leave {
  flex: none;
  height: 44px;
  padding: 0 16px;
  font-size: 14px;
  color: var(--fs-text-2);
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: 12px;
  cursor: pointer;
}

.pl__leave:disabled {
  opacity: 0.5;
  cursor: default;
}

/* ── 等待邀请被接受 ── */

.pl__waiting {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
  padding: 14px 16px;
  background: var(--fs-primary-weak);
  border-radius: var(--fs-radius);
}

.pl__waiting-label {
  font-size: 13px;
  color: var(--fs-text-2);
}

.pl__waiting-side {
  display: flex;
  align-items: center;
  gap: 12px;
}

.pl__waiting-tip {
  font-size: 12px;
  color: var(--fs-text-3);
}

.pl__cancel {
  padding: 4px 10px;
  font-size: 12px;
  color: var(--fs-text-2);
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius-sm);
  cursor: pointer;
}

/* ── 对局面板与列表 ── */

.pl__live {
  padding: 4px 16px 8px;
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius);
}

.pl__panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 46px;
}

.pl__panel-title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-text-1);
}

.pl__panel-count {
  font-size: 12px;
  color: var(--fs-text-3);
}

.pl__skeleton {
  padding: 4px 0 16px;
}

.pl__empty {
  margin: 0 0 16px;
  font-size: 13px;
  line-height: 1.8;
  color: var(--fs-text-3);
}

.pl__retry {
  padding: 0;
  font-size: 13px;
  color: var(--fs-primary);
  background: none;
  border: none;
  cursor: pointer;
}

.pl__list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.pl__row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 0;
}

/* 分隔线用相邻兄弟而不是 :not(:last-of-type)：
   列表里全是 li，相邻兄弟写法更直白，以后中间插别的元素也不会错位。 */
.pl__row + .pl__row {
  border-top: 1px solid var(--fs-line);
}

.pl__who {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.pl__name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-text-1);
}

.pl__handle {
  font-size: 12px;
  color: var(--fs-text-3);
}

/* 观战入口做成实心小按钮：同一块面板里「邀请」是主动作，
   而「观战」是看一眼别人的对局，属于次要动作。 */
.pl__watch {
  flex: none;
  height: 28px;
  padding: 0 12px;
  font-size: 12px;
  line-height: 28px;
  color: #fff;
  text-decoration: none;
  background: var(--fs-primary);
  border-radius: var(--fs-radius-sm);
}

/* 名额满了：与按钮同尺寸，但不可点也不像按钮。 */
.pl__watch.is-full {
  color: var(--fs-text-3);
  background: var(--fs-line-soft);
}

.pl__invite {
  flex: none;
  height: 32px;
  padding: 0 14px;
  font-size: 13px;
  font-weight: 600;
  color: var(--fs-primary);
  background: var(--fs-primary-weak);
  border: none;
  border-radius: var(--fs-radius-sm);
  cursor: pointer;
}

.pl__invite:disabled {
  color: var(--fs-text-3);
  background: var(--fs-surface-hover);
  cursor: default;
}

@media (max-width: 860px) {
  /* 窄屏堆成一列：右边那栏接在房间卡下面，仍然自己滚动，
     不然几十个在线的人会把页面拉到很长。 */
  .pl__body {
    grid-template-columns: minmax(0, 1fr);
  }

  .pl__side {
    position: static;
    max-height: 320px;
  }
}

@media (max-width: 560px) {
  /* 手机竖屏下房号与标题挤在同一行会换行成两截，
     干脆让它单独占一行：这串数字是这一页最需要被看清的东西。 */
  .pl__room-head {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }
}
</style>
