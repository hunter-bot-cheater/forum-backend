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
      <!-- 连不上就一直退避重连没有意义：对局没了或名额被抢走，
           重试一万次也是同一个结果，给个台阶回大厅。 -->
      <button v-if="fatalText" class="wt__retry" type="button" @click="back">回大厅</button>
      <button
        v-else-if="connState === 'gaveup'"
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
import {
  CLOSE_SPECTATOR_FULL,
  GameSocket,
  type ConnectionState,
  type DownMessage,
  type ResultData,
  type ShotData,
} from '@/game/protocol'
import { remainingOf } from '@/game/score'
import { SEAT_SPECTATOR } from '@/game/types'
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
// fatalText 是「再看也看不到」的说明（名额被抢、这一局没有了）。
// 有值时不再给重试按钮：重试必然还是同一个结果。
const fatalText = ref('')
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
// pendingResult 同理：结算帧紧跟在最后一杆之后，
// 排在动画前面就会先盖一条结果再继续放球，看起来像结算抢跑。
let pendingResult: ResultData | null = null
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
  // 已经出结果就把位置让给结果条：两个盒子都在球台正中，
  // 同时出现的结果是「谁赢下这一局」四周露出半截「正在重连…」。
  // 结束后断线本来也没必要说——对局已经打完了，说什么都不会再变。
  if (finishedText.value) {
    return ''
  }
  if (fatalText.value) {
    return fatalText.value
  }
  if (connState.value === 'gaveup') {
    return '与对局的连接已断开'
  }
  // 掉线时即使手里有旧快照也要说一声：画面会停在最后一次收到的位置，
  // 不说的话看起来就像两个人一起不动了。
  if (connState.value === 'closed') {
    return '连接断开，正在重连…'
  }
  if (snapshot.value) {
    return ''
  }
  return '正在进入对局…'
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
  if (animating) {
    // 上一杆还没播完就来了下一杆（后台标签页、慢设备上会出现）：
    // 先把上一杆排队的快照与结算放行，否则它们会挂到这一杆播完才生效，
    // 轮次与读秒会倒退一格。放行之后再从新一杆的起点重画。
    animating.stop()
    animating = null
    flushQueued()
  }
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
        flushQueued()
      },
    },
  )
}

// flushQueued 把动画期间攒下的结算与快照依次生效。
//
// 结算先套：它只改顶部那一句，套完再套快照，快照里的 status 是 finished 时
// 还会再写一次同样的话，重复无害。
function flushQueued() {
  const queuedResult = pendingResult
  pendingResult = null
  if (queuedResult) {
    finishWith(queuedResult)
  }
  const queued = pendingState
  pendingState = null
  if (queued) {
    applySnapshot(queued.snap, queued.deadlineAt)
  }
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
  if (msg.type === 'joined') {
    // 服务端按「是不是局中人」决定座位。观战页却收到了一个真座位，
    // 说明自己就是这一局的当事人（大厅里点开了自己那场）。
    // 这时必须让位：继续留在这里等于用第二条连接顶掉自己那条对局连接。
    if (msg.data.seat !== SEAT_SPECTATOR) {
      fatalText.value = '这一局你是当事人，正在回到对局页…'
      // 先把这条连接关掉再跳：跳转是异步的，导航窗口里它仍占着你的座位，
      // 一旦它先断，对手就会收到一条「他掉线了」。
      socket?.close()
      socket = null
      void router.replace('/pool/match/' + props.id)
    }
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
    if (animating) {
      pendingResult = msg.data
      return
    }
    finishWith(msg.data)
    return
  }
  // turn / error 在观战这边不需要额外动作：
  // 换人已经包含在随后的快照里，而观战者本来就不发命令，收不到业务错误。
}

// finishWith 记下结算结果，名字从快照里取。
function finishWith(data: ResultData) {
  const snap = snapshot.value
  if (!snap) {
    // 手里还没有快照就收到结算（正常流程下不会：state 永远排在 result 前面，
    // 重连也会先给一份快照）。真出现了也别把它咽掉——
    // 不知道名字就只说输赢已定，总比停在「轮到某人」上强。
    finishedText.value = '对局已结束'
    deadlineAt.value = 0
    return
  }
  showFinished(data.winner, snap.players)
}

// connect 建一条观战连接。断开由 GameSocket 自己退避重连。
function connect() {
  socket = new GameSocket({
    matchId: props.id,
    token: () => getToken(),
    onMessage,
    onState: (state) => {
      connState.value = state
    },
    onFatal: (code) => {
      // 只有升级之后才说得清的两种拒绝会走到这里。
      // 名额满了与这一局没了给的话不一样：前者还会有人走，后者再点也没用。
      if (code === CLOSE_SPECTATOR_FULL) {
        fatalText.value = '这场对局的观战人数已满，换一场看吧。'
        return
      }
      fatalText.value = '这一局已经结束了，回大厅看看别的对局吧。'
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
