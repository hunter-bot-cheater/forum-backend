<template>
  <div class="wt">
    <header class="wt__bar">
      <button class="wt__back" type="button" aria-label="返回" @click="back">
        <el-icon :size="18"><ArrowLeft /></el-icon>
      </button>

      <!-- 双方条与对局页同一套座位卡：观战者没有「我」这一侧，
           所以两张卡都不标「你」，也不按视角左右翻转。 -->
      <div class="wt__seats">
        <SeatCard
          v-for="side in seats"
          :key="side.seat"
          :player="side.player"
          :group="side.group"
          :remaining="side.remaining"
          :active="side.active"
          :is-me="false"
        />
      </div>

      <span class="wt__badge">观战</span>
    </header>

    <main class="wt__stage">
      <PoolTable :balls="balls" :aim="null" />

      <Transition name="wt-notice">
        <div v-if="notice" class="wt__notice">{{ notice }}</div>
      </Transition>

      <p v-if="hintText" class="wt__hint">{{ hintText }}</p>

      <!-- 结束只报一句结果，不弹结算面板。
           结算面板说的是「你赢了 / 你输了」，而观战者没有这一侧。 -->
      <div v-if="finishedText" class="wt__end">{{ finishedText }}</div>
    </main>

    <footer class="wt__foot">
      <span class="wt__turn">{{ turnText }}</span>
      <button
        v-if="connState === 'gaveup'"
        class="wt__retry"
        type="button"
        @click="retryConnect"
      >
        重新连接
      </button>
    </footer>
  </div>
</template>

<script setup lang="ts">
// 观战页：看别人的对局，只能看不能动。
//
// 与对局页（match.vue）共用的东西全部复用：同一块 PoolTable、同一套座位卡、
// 同一个 animator。不共用的是「操作」那一半——观战没有瞄准、没有力度、
// 没有落位、没有「我是不是该出杆」的判断，硬塞进 match.vue 会让那边
// 每处判断都多一个分支，所以单独一页。
//
// 服务端在握手时就把观战者认出来了（joined.seat 为 -1），这里不再发任何
// 上行消息；断线重连走的是 GameSocket 自带的那套退避。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowLeft } from '@element-plus/icons-vue'
import PoolTable from '@/components/pool/PoolTable.vue'
import SeatCard from '@/components/pool/SeatCard.vue'
import { applySample, playShot, type PlayHandle } from '@/game/animator'
import { GameSocket, type ConnectionState, type DownMessage, type ShotData } from '@/game/protocol'
import { remainingOf } from '@/game/score'
import type { BallState, MatchSnapshot, Seat } from '@/game/types'
import { getToken } from '@/utils/auth'
import { goBack } from '@/utils/nav'

const props = defineProps<{
  id: string
}>()

const router = useRouter()

const snapshot = ref<MatchSnapshot | null>(null)
// balls 是本地渲染用的球位：动画期间被逐帧改写，一杆结束后按快照对齐。
const balls = ref<BallState[]>([])
const turn = ref<Seat>(0)
const connState = ref<ConnectionState>('connecting')
// finishedText 有值表示对局结束了，画面上盖一条结果。
const finishedText = ref('')
// notice 是犯规之类的短提示，3 秒后自己消失。
const notice = ref('')

// deadlineAt 是本杆读秒的本地截止时刻，0 表示不计时。
const deadlineAt = ref(0)
const now = ref(Date.now())

let socket: GameSocket | null = null
let animating: PlayHandle | null = null
let tickTimer: ReturnType<typeof setInterval> | null = null
let noticeTimer: ReturnType<typeof setTimeout> | null = null
// pendingState 存动画期间到达的快照。
// 一杆的 shot 帧与随后的 state 帧是连着来的，动画没播完就套用新快照，
// 球会先跳到终局位置再倒回动画开头。
let pendingState: { snap: MatchSnapshot; deadlineAt: number } | null = null
let disposed = false

const seats = computed(() => {
  const snap = snapshot.value
  if (!snap) {
    return []
  }
  // 观战固定按座位 0、1 的顺序排，不做左右翻转：
  // 没有「我」就没有「我这边」，翻转只会让两个人的位置来回变。
  const order: Seat[] = [0, 1]
  return order.map((seat) => ({
    seat,
    player: snap.players[seat],
    group: snap.group[seat],
    remaining: remainingOf(snap, seat),
    active: turn.value === seat && snap.status === 'playing',
  }))
})

// clockText 是剩余读秒。截止时刻在本地算，避免与服务端钟差。
const clockText = computed(() => {
  if (deadlineAt.value === 0) {
    return ''
  }
  const left = Math.max(0, Math.ceil((deadlineAt.value - now.value) / 1000))
  return String(left) + ' 秒'
})

const turnText = computed(() => {
  const snap = snapshot.value
  if (!snap) {
    return '正在进入对局…'
  }
  if (finishedText.value) {
    return '对局已结束'
  }
  const name = snap.players[turn.value].nickname || '玩家 ' + String(turn.value + 1)
  const clock = clockText.value
  if (!clock) {
    return '轮到 ' + name
  }
  return '轮到 ' + name + ' · ' + clock
})

// hintText 是盖在球台上的说明条，只在这几种「画面还不对」的时候出现。
const hintText = computed(() => {
  if (connState.value === 'gaveup') {
    return '与对局的连接已断开'
  }
  if (snapshot.value) {
    return ''
  }
  if (connState.value === 'connecting') {
    return '正在进入对局…'
  }
  return '连接断开，正在重连…'
})

// applySnapshot 用快照重建场面。
function applySnapshot(snap: MatchSnapshot, deadline: number) {
  snapshot.value = snap
  turn.value = snap.turn
  deadlineAt.value = deadline
  now.value = Date.now()

  const list: BallState[] = []
  for (const ball of snap.balls) {
    list.push({ id: ball.id, x: ball.x, y: ball.y, z: ball.z, on_table: ball.on_table })
  }
  balls.value = list

  // 快照里已经结算：可能是刚进来时就结束了，或者断线重连回来才发现打完。
  if (snap.status === 'finished' && snap.winner !== null) {
    showFinished(snap.winner, snap.players)
  }
}

// showFinished 把结果写进顶部那一条。
function showFinished(winner: Seat, players: MatchSnapshot['players']) {
  const name = players[winner].nickname || '玩家 ' + String(winner + 1)
  finishedText.value = name + ' 赢下这一局'
  deadlineAt.value = 0
}

// playShotFrame 播一杆。动画期间到达的快照先存着，播完再套。
function playShotFrame(data: ShotData) {
  animating?.stop()
  animating = playShot(
    { frames: data.frames, duration: data.duration },
    {
      onSample: (sample) => {
        applySample(balls.value, sample)
      },
      onDone: () => {
        animating = null
        if (disposed) {
          return
        }
        const queued = pendingState
        pendingState = null
        if (queued) {
          applySnapshot(queued.snap, queued.deadlineAt)
        }
      },
    },
  )
}

// showNotice 显示一条短提示。
function showNotice(text: string) {
  notice.value = text
  if (noticeTimer) {
    clearTimeout(noticeTimer)
  }
  noticeTimer = setTimeout(() => {
    notice.value = ''
    noticeTimer = null
  }, 3000)
}

// onMessage 分发下行消息。观战者不会收到 peer（服务端不给观战者发）。
function onMessage(msg: DownMessage) {
  if (disposed) {
    return
  }
  if (msg.type === 'state') {
    const entry = {
      snap: msg.data,
      deadlineAt: msg.data.deadline_ms > 0 ? Date.now() + msg.data.deadline_ms : 0,
    }
    if (animating) {
      pendingState = entry
      return
    }
    applySnapshot(entry.snap, entry.deadlineAt)
    return
  }
  if (msg.type === 'shot') {
    playShotFrame(msg.data)
    return
  }
  if (msg.type === 'foul') {
    showNotice(msg.data.reason)
    return
  }
  if (msg.type === 'result') {
    const snap = snapshot.value
    if (snap) {
      showFinished(msg.data.winner, snap.players)
    }
  }
  // joined / turn / error 在观战这边都不需要额外动作：
  // 座位是 -1，joined 没有可用的东西；换人已经包含在随后的快照里。
}

// connect 建一条观战连接。断开由 GameSocket 自己退避重连。
function connect() {
  socket = new GameSocket({
    matchId: props.id,
    token: () => getToken(),
    onMessage,
    onState: (state) => {
      connState.value = state
      // 重连成功后服务端会补一份完整快照，这里不做任何本地修补。
    },
  })
  socket.connect()
}

// retryConnect 是放弃重连之后的人工重试。
function retryConnect() {
  if (socket) {
    socket.connect()
  }
}

// back 回大厅。
function back() {
  goBack(router, '/pool')
}

onMounted(() => {
  connect()
  // 读秒每 250ms 刷一次：整秒刷新会有一瞬间从 3 直接跳到 1，
  // 四分之一秒的粒度已经够顺，也不至于每秒算四次时间。
  tickTimer = setInterval(() => {
    now.value = Date.now()
  }, 250)
})

onBeforeUnmount(() => {
  disposed = true
  animating?.stop()
  animating = null
  if (tickTimer) {
    clearInterval(tickTimer)
    tickTimer = null
  }
  if (noticeTimer) {
    clearTimeout(noticeTimer)
    noticeTimer = null
  }
  // 观战不需要发 leave：服务端把这当成一条观战连接断了，只从观战名单里摘掉。
  socket?.close()
  socket = null
})
</script>

<style scoped>
/* 与对局页同一套深色底：它们展示的是同一块球台，
   换成浅底会让人以为是两个功能。 */
.wt {
  display: flex;
  flex-direction: column;
  height: 100dvh;
  background: #0e1116;
}

.wt__bar {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
  height: 52px;
  padding: 0 10px;
}

.wt__back {
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  padding: 0;
  color: #e8eaed;
  background: rgba(255, 255, 255, 0.08);
  border: none;
  border-radius: 8px;
  cursor: pointer;
}

.wt__seats {
  display: flex;
  flex: 1;
  gap: 8px;
  min-width: 0;
}

.wt__badge {
  flex: none;
  padding: 3px 8px;
  font-size: 11px;
  color: #7aa3ff;
  background: rgba(122, 163, 255, 0.16);
  border-radius: 999px;
}

.wt__stage {
  position: relative;
  flex: 1;
  min-height: 0;
  padding: 0 8px 4px;
}

.wt__notice {
  position: absolute;
  top: 8px;
  left: 50%;
  padding: 6px 14px;
  font-size: 12px;
  color: #ffd9a8;
  background: rgba(60, 42, 18, 0.92);
  border-radius: 999px;
  transform: translateX(-50%);
}

.wt-notice-enter-active,
.wt-notice-leave-active {
  transition: opacity 0.2s ease;
}

.wt-notice-enter-from,
.wt-notice-leave-to {
  opacity: 0;
}

.wt__hint {
  position: absolute;
  top: 50%;
  left: 50%;
  margin: 0;
  padding: 8px 16px;
  font-size: 13px;
  color: #e8eaed;
  background: rgba(18, 22, 30, 0.9);
  border-radius: 999px;
  transform: translate(-50%, -50%);
}

.wt__end {
  position: absolute;
  top: 50%;
  left: 50%;
  padding: 10px 20px;
  font-size: 15px;
  font-weight: 600;
  color: #fff;
  background: rgba(18, 22, 30, 0.92);
  border-radius: 12px;
  transform: translate(-50%, -50%);
}

.wt__foot {
  display: flex;
  flex: none;
  align-items: center;
  gap: 10px;
  height: 44px;
  padding: 0 12px;
}

.wt__turn {
  flex: 1;
  font-size: 12px;
  color: #8a919c;
  font-variant-numeric: tabular-nums;
}

.wt__retry {
  flex: none;
  height: 30px;
  padding: 0 12px;
  font-size: 12px;
  color: #fff;
  background: #4c7ef3;
  border: none;
  border-radius: 8px;
  cursor: pointer;
}
</style>
