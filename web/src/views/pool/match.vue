<template>
  <div class="pm">
    <!-- 顶部只留一条窄栏。对局页不套站点 Layout：60px 顶栏加内容区留白，
         在竖屏手机上会吃掉球台将近六分之一的高度。 -->
    <header class="pm__bar">
      <button class="pm__back" type="button" aria-label="返回" @click="leave">
        <el-icon :size="18"><ArrowLeft /></el-icon>
      </button>

      <!-- 双方条放在顶栏里而不是单独占一行：竖屏手机上球台越高越好，
           而这两张座位卡只需要一行的位置。 -->
      <div class="pm__seats">
        <SeatCard
          v-for="side in seats"
          :key="side.seat"
          :player="side.player"
          :group="side.group"
          :remaining="side.remaining"
          :active="side.active"
          :is-me="side.seat === mySeat"
        />
      </div>

      <span class="pm__clock">{{ clockText }}</span>
    </header>

    <main class="pm__stage">
      <PoolTable :balls="balls" :aim="aim" @drag="onDrag" @tap="onTap" />

      <!-- 换人横幅盖在球台上，不挡操作。 -->
      <TurnBanner
        :visible="bannerVisible"
        :is-me="bannerMine"
        :name="bannerName"
        :avatar="bannerAvatar"
        :subtitle="bannerSubtitle"
      />

      <!-- 犯规与提示条。3 秒后自动消失，不需要用户去点。 -->
      <Transition name="pm-notice">
        <div v-if="notice" class="pm__notice">{{ notice }}</div>
      </Transition>

      <!-- 手中球：整块球台可点，落点前不允许出杆。 -->
      <div v-if="mustPlace" class="pm__place">点击台面放置母球</div>

      <div class="pm__power">
        <PowerSlider v-model:value="power" :disabled="!canShoot" @shoot="shoot" />
      </div>
    </main>

    <footer class="pm__foot">
      <FineTuneRail :phi="phi" :disabled="!canAim" @nudge="onNudge" />
    </footer>

    <ResultSheet
      v-if="result"
      :won="result.won"
      :reason="result.reason"
      :delta="result.delta"
      @again="rematch"
      @lobby="backToLobby"
    />

    <!-- 连接彻底放弃（重连窗口走完）时才整屏拦一下：
         留在球台上只会让人对着一个死局拖力度。
         但给一个「重新连接」：断网超过四十秒才会走到这里，
         而人多半只是切了一下网，直接赶回大厅等于这局白打。 -->
    <div v-if="connState === 'gaveup'" class="pm__gone" role="dialog" aria-modal="true">
      <p class="pm__gone-text">与对局的连接已断开</p>
      <p class="pm__gone-sub">网络恢复后可以重试；对局还在服务端等你。</p>
      <div class="pm__gone-actions">
        <button class="pm__gone-btn pm__gone-btn--ghost" type="button" @click="backToLobby">
          回大厅
        </button>
        <button class="pm__gone-btn" type="button" @click="retryConnect">重新连接</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
// 对局页。所有球位、轮次、犯规判定都来自服务端，本地只负责「把一杆画出来」。
//
// 三种状态要分清楚：
//   1. 服务端快照（snapshot）：权威状态，进房与重连时整份替换；
//   2. 本地球位（balls）：为了播放动画逐帧被原地改写，动画结束后按 final 对齐；
//   3. 本地瞄准与力度：纯客户端状态，服务端只收最终那五个数。
//
// 规则判定一行都不在客户端：能不能出杆由 turn 与 ball_in_hand 决定，
// 但服务端会再判一次，客户端提前拦住只是省一次往返，不是权威。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import { ArrowLeft } from '@element-plus/icons-vue'
import { ElMessageBox } from 'element-plus'
import FineTuneRail from '@/components/pool/FineTuneRail.vue'
import PoolTable from '@/components/pool/PoolTable.vue'
import PowerSlider from '@/components/pool/PowerSlider.vue'
import ResultSheet from '@/components/pool/ResultSheet.vue'
import SeatCard from '@/components/pool/SeatCard.vue'
import TurnBanner from '@/components/pool/TurnBanner.vue'
import { adjustPhi, aimFromCue, normalizePhi, rotateByDrag } from '@/game/aim'
import { applySample, playShot, type PlayHandle } from '@/game/animator'
import type { Point } from '@/game/geometry'
import {
  GameSocket,
  type ConnectionState,
  type DownMessage,
  type FoulData,
  type ResultData,
  type ShotData,
  type TurnData,
} from '@/game/protocol'
import type { AimLayer } from '@/game/renderer'
import { remainingOf } from '@/game/score'
import { BALL_RADIUS, TABLE_LENGTH, TABLE_WIDTH } from '@/game/table'
import type {
  BallState,
  MatchSnapshot,
  PoolDrag,
  Seat,
  ShotParams,
  Viewer,
} from '@/game/types'
import { SEAT_SPECTATOR } from '@/game/types'
import { getToken } from '@/utils/auth'
import { usePoolStore } from '@/store/modules/pool'

const props = defineProps<{
  id: string
}>()

const router = useRouter()
const pool = usePoolStore()

// ===== 服务端状态 =====

const snapshot = ref<MatchSnapshot | null>(null)
// balls 是本地渲染用的球位。动画期间被逐帧改写，动画结束按 final 对齐。
const balls = ref<BallState[]>([])
const mySeat = ref<Seat>(0)
const turn = ref<Seat>(0)
// deadlineAt 是本杆读秒的本地截止时刻（毫秒时间戳）。
// 服务端给的是「还剩多少毫秒」，换成绝对时刻本地才能自己倒数。
const deadlineAt = ref(0)
const nowTick = ref(Date.now())
const connState = ref<ConnectionState>('connecting')

// ===== 本地状态 =====

const power = ref(0)
const phi = ref(0)
const shooting = ref(false)
const notice = ref('')

// 换人横幅
const bannerVisible = ref(false)
const bannerMine = ref(false)
const bannerName = ref('')
const bannerAvatar = ref('')
const bannerSubtitle = ref('')

// 结算
const result = ref<{ won: boolean; reason: string; delta: number | null } | null>(null)

// shotPending 表示「已经发出去一杆，还没收到 shot 帧」。
//
// 不能只靠 shooting：它要等服务端算完（开球 700~1500ms）才有第一帧可播，
// 这段空窗期里力度条已经解禁，连点两下就会发两杆——
// 第二杆的 seq 更小、服务端按「不是你的回合」拒掉，玩家看到一句莫名其妙的报错。
const shotPending = ref(false)

// placing 表示「已经点了落点，等服务端回快照」。
// 落位不做乐观更新：越界或落点在球上时服务端会拒，本地先挪过去还得再挪回来，
// 一次往返只有几毫秒，等权威结果更省事。
const placing = ref(false)

// 动画句柄与后置消息队列。
//
// 一杆的结果是一串连着的帧：shot → foul → turn/result → state。
// 动画要播好几秒，后面几条在动画期间就到了。直接应用会让「球还在滚」
// 和「已经换人」同时出现在屏幕上，state 更糟——它会把球瞬移到终局位置。
// 所以除了 shot（用来驱动动画）之外，其余一律先存起来，播完再放出去。
let handle: PlayHandle | null = null
let pendingFoul: FoulData | null = null
// pendingTurn 连着截止时刻一起存。
//
// 只存帧本身的话，applyTurn 里再算 Date.now() + deadline_ms，会用「动画播完」那一刻
// 当起点，读秒上限凭空多出整段动画时长；而 flush 里它排在 state 后面，
// 还会把 state 给的正确值覆盖掉。
let pendingTurn: { data: TurnData; deadlineAt: number } | null = null
let pendingState: { snap: MatchSnapshot; deadlineAt: number } | null = null
let pendingResult: ResultData | null = null

// 拖动起点的角度。整个拖动过程都基于它加总位移，
// 逐帧累加会把浮点误差攒起来，拖久了瞄准线会慢慢偏。
let dragBase = 0

let socket: GameSocket | null = null
// switchingToWatch 挡住重复跳转：joined 与紧随其后的 state 都会撞上观战座位，
// 不加这个标记时 router.replace 会被调用两次。
let switchingToWatch = false
// disposed 在卸载时置位，用来挡住在途帧。
let disposed = false
// leaveSent 保证「告知服务端退出」只发一次，见 sendLeave。
let leaveSent = false
// 读秒用的 1Hz 计时器。
let tickTimer: ReturnType<typeof setInterval> | null = null
let bannerTimer: ReturnType<typeof setTimeout> | null = null
let noticeTimer: ReturnType<typeof setTimeout> | null = null

// ===== 派生状态 =====

const isMyTurn = computed(() => {
  if (!snapshot.value) {
    return false
  }
  return snapshot.value.status === 'playing' && turn.value === mySeat.value
})

// mustPlace 表示手里握着母球，必须先落位才能出杆。
const mustPlace = computed(() => {
  if (!snapshot.value) {
    return false
  }
  return snapshot.value.ball_in_hand === true && isMyTurn.value
})

// canShoot 是「现在能不能出杆」的唯一判据。
// 四个条件缺一不可：轮到我、没有一杆在路上、不在播动画、没有手中球。
const canShoot = computed(
  () => isMyTurn.value && !shotPending.value && !shooting.value && !mustPlace.value,
)

// canAim 只影响瞄准线与微调尺：动画期间不该让瞄准线跟着手指跑。
const canAim = computed(() => isMyTurn.value && !shooting.value)

// seats 是双方条要传的数据：座位、该侧组别、该侧剩余球。
const seats = computed(() => {
  const snap = snapshot.value
  if (!snap) {
    return []
  }
  const me = mySeat.value
  const other: Seat = me === 0 ? 1 : 0
  // 显式标 Seat[]：不标的话数组字面量会被推成 number[]，
  // 后面 group[seat] / players[seat] 都要按座位号取，类型对不上。
  const order: Seat[] = [me, other]
  return order.map((seat) => ({
    seat,
    player: snap.players[seat],
    group: snap.group[seat],
    remaining: remainingOf(snap, seat),
    active: turn.value === seat && snap.status === 'playing',
  }))
})

// clockText 是右上角的读秒。
const clockText = computed(() => {
  if (result.value) {
    return '已结束'
  }
  if (connState.value === 'connecting' || connState.value === 'closed') {
    return '重连中'
  }
  if (!snapshot.value || snapshot.value.status === 'waiting') {
    return '等待开始'
  }
  if (!isMyTurn.value) {
    return '对手回合'
  }
  if (deadlineAt.value === 0) {
    return '你的回合'
  }
  const left = Math.max(0, Math.ceil((deadlineAt.value - nowTick.value) / 1000))
  return '你 · ' + String(left) + 's'
})

// aim 是当前瞄准线。动画期间不画：球在动的时候还挂一条虚线，
// 会让人以为下一杆方向已经定了。
const aim = computed<AimLayer | null>(() => {
  if (!canAim.value) {
    return null
  }
  const cue = balls.value.find((b) => b.id === 'cue')
  if (!cue || !cue.on_table) {
    return null
  }
  const hit = aimFromCue(balls.value, phi.value)
  return {
    cue: { x: cue.x, y: cue.y },
    hit: { x: hit.x, y: hit.y, kind: hit.kind },
  }
})

// ===== 与服务端的交互 =====

// seatOfViewer 把协议里的「看的人坐哪」收窄成座位。
//
// 对局页只服务两个座位：观战快照（-1）本该出现在观战页。真收到了说明
// 走错了入口（大厅里点开了自己那场之外的观战链接），这时不能拿座位 0
// 硬画——界面会把左边的球当成「我的球」，看着像坐错了位置。
// 换成观战页去，那里才是这份快照该去的地方。
function seatOfViewer(viewer: Viewer): Seat | null {
  if (viewer !== SEAT_SPECTATOR) {
    return viewer
  }
  if (!switchingToWatch) {
    switchingToWatch = true
    socket?.close()
    void router.replace('/pool/watch/' + props.id)
  }
  return null
}

// applySnapshot 用服务端快照重建本地场景。
//
// 整份替换而不是增量合并：进房与重连拿到的都是完整状态，
// 合并还要处理「哪些球被删了、位置要不要保留」，而重建是零风险的。
function applySnapshot(snap: MatchSnapshot, deadline: number) {
  const first = snapshot.value === null
  snapshot.value = snap
  const seat = seatOfViewer(snap.seat)
  if (seat === null) {
    return
  }
  mySeat.value = seat
  turn.value = snap.turn
  deadlineAt.value = deadline
  // 快照回来了说明落位请求已经有结论（成功会带新球位，失败会有错误帧），
  // 这个标记只在「点了台面到服务端答复」之间有用。
  placing.value = false
  // 出杆在途标记同理：快照是权威状态，它能到就说明那一杆已经有结论了
  // （服务端要么算完广播了 shot，要么把请求拒了）。
  // 断线重连时正是靠这一句把标记解开——否则 canShoot 永远是 false，只能刷新页面。
  shotPending.value = false
  // 第一次拿到快照时才知道母球在哪，瞄准角要在这之后算：
  // 组件挂载那一刻 balls 还是空的，算出来恒为 0（永远指向 +x）。
  if (first) {
    phi.value = initialPhi()
  }

  const list: BallState[] = []
  for (const ball of snap.balls) {
    list.push({ id: ball.id, x: ball.x, y: ball.y, z: ball.z, on_table: ball.on_table })
  }
  balls.value = list

  // 重连进来时对局可能已经结束，快照里带的就是结果。
  if (snap.status === 'finished' && snap.winner !== null) {
    result.value = { won: snap.winner === snap.seat, reason: snap.win_reason, delta: null }
  }
}

// onMessage 分发下行消息。
//
// 首次快照不弹换人横幅：刚进房就滑一条「你的回合」出来，
// 会让人以为中间漏看了一段。只在后面收到 turn 帧时弹。
function onMessage(msg: DownMessage) {
  // 卸载之后还可能收到在途的帧（close 是异步的），
  // 继续往下走会往已经销毁的组件里写 ref，动画也可能被重新拉起来。
  if (disposed) {
    return
  }
  if (msg.type === 'joined') {
    const seat = seatOfViewer(msg.data.seat)
    if (seat !== null) {
      mySeat.value = seat
    }
    return
  }
  if (msg.type === 'state') {
    // 读秒按「收到这一刻」算：服务端给的是发送时的剩余毫秒，
    // 动画播完再套用会把上限往后推四五秒，界面上的读秒就和真实读秒错开。
    const entry = {
      snap: msg.data,
      deadlineAt: msg.data.deadline_ms > 0 ? Date.now() + msg.data.deadline_ms : 0,
    }
    if (shooting.value) {
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
    if (shooting.value) {
      pendingFoul = msg.data
      return
    }
    applyFoul(msg.data)
    return
  }
  if (msg.type === 'turn') {
    // 与 state 一样按「收到这一刻」换算截止时刻：晚了整段动画再算，
    // 读秒就会比服务端的真实读秒多出四五秒。
    const entry = {
      data: msg.data,
      deadlineAt: msg.data.deadline_ms > 0 ? Date.now() + msg.data.deadline_ms : 0,
    }
    if (shooting.value) {
      pendingTurn = entry
      return
    }
    applyTurn(entry.data, entry.deadlineAt)
    return
  }
  if (msg.type === 'result') {
    if (shooting.value) {
      pendingResult = msg.data
      return
    }
    applyResult(msg.data)
    return
  }
  if (msg.type === 'peer') {
    onPeer(msg.data.event)
    return
  }
  if (msg.type === 'error') {
    onServerError(msg.data)
  }
}

// playShotFrame 播放一杆的轨迹。
//
// 双方都播同一段帧：自己那一杆也要播，否则出杆后画面会停住直到结果到达，
// 看起来像点了没反应。
function playShotFrame(data: ShotData) {
  if (handle) {
    handle.stop()
    handle = null
  }
  // 服务端已经收下这一杆并算完了，在途标记可以放开：
  // 从这里开始由 shooting 接管，直到动画播完都不允许再出杆。
  shotPending.value = false
  placing.value = false
  shooting.value = true
  // 力度在发出去的那一刻就清零了，这里不再重复：
  // 两处都清会让人以为「播放帧」也要负责界面状态。

  handle = playShot(
    { frames: data.frames, duration: data.duration },
    {
      onSample: (sample) => {
        applySample(balls.value, sample)
      },
      onDone: () => {
        handle = null
        // 动画是插值播的，最后一帧不一定正好落在静止位置。
        // 按服务端给的 final 对齐一次，避免每杆累积几十毫米的偏差。
        applyFinal(data)
        shooting.value = false
        flushAfterShot()
      },
    },
  )
}

// applyFinal 把球位对齐到服务端给的终局。
//
// 紧接着应用的 state 快照会再对齐一次（两份数据同源），
// 这里保留是因为 state 有可能因为断线丢包，而 shot 帧已经到手了。
function applyFinal(data: ShotData) {
  const byId = new Map<string, BallState>()
  for (const ball of balls.value) {
    byId.set(ball.id, ball)
  }
  for (const item of data.final) {
    const ball = byId.get(item.id)
    if (!ball) {
      continue
    }
    ball.x = item.x
    ball.y = item.y
    ball.z = item.z
    ball.on_table = item.on_table
  }
}

// flushAfterShot 在动画播完之后把攒下的消息依次放出去。
//
// 顺序是有讲究的：
//   1. 先应用 state —— 它是权威，球位、定组、手中球、轮次都按它对齐；
//   2. 再提示犯规 —— 文案来自 foul 帧，服务端已经写好，前端不拼；
//   3. 然后才是换人横幅 —— 它必须弹在「回合已经变了」之后，
//      否则横幅写着「你的回合」，界面上的可出杆状态还是旧的；
//   4. 最后结算 —— 结算面板会盖住球台，放在最后不影响前几步的动画收尾。
function flushAfterShot() {
  if (pendingState) {
    const next = pendingState
    pendingState = null
    applySnapshot(next.snap, next.deadlineAt)
  }
  if (pendingFoul) {
    const foul = pendingFoul
    pendingFoul = null
    applyFoul(foul)
  }
  if (pendingTurn) {
    const next = pendingTurn
    pendingTurn = null
    applyTurn(next.data, next.deadlineAt)
  }
  if (pendingResult) {
    const next = pendingResult
    pendingResult = null
    applyResult(next)
  }
}

// applyFoul 显示犯规文案，并顺便采纳「这一杆之后是不是手中球」。
//
// 正常顺序下紧随其后的 state 会用权威值覆盖它，但 WS 不保证不丢包：
// state 恰好丢掉时，foul 里的这一项就是唯一来源。
// 只在 true 时采纳——漏掉「有手中球」会让接手方既摆不了球也出不了杆（彻底卡死），
// 而多留一个手中球最多被服务端按 ErrNotBallInHand 拒一次，下一次快照就纠正回来。
function applyFoul(data: FoulData) {
  const snap = snapshot.value
  if (snap && data.ball_in_hand) {
    snap.ball_in_hand = true
  }
  showNotice(data.reason)
}

// applyTurn 应用换人：轮次、读秒、横幅。
// deadline 是收包时换算好的绝对截止时刻，这里不再重新算。
// 只在没有后续 state 帧时才需要它来改 turn；
// 有 state 时两者一致，重复应用同一个值没有副作用。
function applyTurn(data: TurnData, deadline: number) {
  turn.value = data.seat
  deadlineAt.value = deadline
  showBanner(data.seat)
}

// applyResult 结算。
//
// delta 取自己那一边；两个都为 0 说明这一帧没带积分（积分在落库那一步算，
// 那时广播已经发出去了），此时传 null 让面板不显示积分行。
function applyResult(data: ResultData) {
  let delta: number | null = null
  if (data.elo_delta_0 !== 0 || data.elo_delta_1 !== 0) {
    delta = mySeat.value === 0 ? data.elo_delta_0 : data.elo_delta_1
  }
  result.value = { won: data.winner === mySeat.value, reason: data.reason, delta }
}

// onPeer 处理对手进出。
function onPeer(event: string) {
  if (event === 'left') {
    showNotice('对手离开了对局')
    return
  }
  if (event === 'offline') {
    showNotice('对手掉线了，正在等他重连')
    return
  }
  if (event === 'joined') {
    showNotice('对手已进入对局')
  }
}

// onServerError 处理服务端的业务错误。
//
// 除了显示文案，还必须解开本地在途标记：出杆被拒时不会再有 shot 帧回来，
// 标记不解开力度条会永久变灰，玩家只能刷新页面才能继续。
// 「球桌算不动」这类暂时性错误也一样，重试由玩家自己决定。
function onServerError(data: { code: string; message: string }) {
  shotPending.value = false
  placing.value = false
  showNotice(data.message)
}

// showNotice 显示一条提示，3 秒后自动消失。
//
// 不用 ElMessage：对局页是深色沉浸场景，右上角弹一条白色 toast 很跳，
// 而「不是你的回合」这类提示就发生在球台边上。
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

// showBanner 弹换人横幅，1.4 秒后收起。
function showBanner(seat: Seat) {
  const snap = snapshot.value
  if (!snap) {
    return
  }
  const mine = seat === mySeat.value
  bannerMine.value = mine
  bannerName.value = snap.players[seat].nickname
  bannerAvatar.value = snap.players[seat].avatar
  bannerSubtitle.value = mine ? '该你出杆了' : '等他出杆'
  bannerVisible.value = true

  if (bannerTimer) {
    clearTimeout(bannerTimer)
  }
  bannerTimer = setTimeout(() => {
    bannerVisible.value = false
    bannerTimer = null
  }, 1400)
}

// ===== 本地操作 =====

// initialPhi 算母球到八号球的方向，作为进房后的默认瞄准角。
//
// 不默认朝右或朝上：开球时玩家想看的就是这条线，初始方向错了要先自己转一下。
function initialPhi(): number {
  let targetX = TABLE_WIDTH / 2
  let targetY = TABLE_LENGTH * 0.75
  for (const ball of balls.value) {
    if (ball.id === '8') {
      targetX = ball.x
      targetY = ball.y
    }
  }
  const cue = balls.value.find((b) => b.id === 'cue')
  if (!cue) {
    return 0
  }
  return normalizePhi(Math.atan2(targetY - cue.y, targetX - cue.x))
}

// onDrag 处理画布拖动：只认水平位移，换算出新的瞄准角。
function onDrag(payload: PoolDrag) {
  if (!canAim.value) {
    return
  }
  if (payload.phase === 'start') {
    dragBase = phi.value
    return
  }
  phi.value = rotateByDrag(dragBase, payload.dx)
}

// onNudge 处理底部微调尺上报的度数增量。
function onNudge(deltaDeg: number) {
  if (!canAim.value) {
    return
  }
  phi.value = adjustPhi(phi.value, deltaDeg)
}

// onTap 处理台面点按。只在手中球时有用：落位母球。
//
// 不做乐观更新：服务端落位成功会广播一份完整快照，母球位置从那里来。
// 先在本地挪过去的话，一旦被判越界（或落点在别的球上）就得再挪回来，
// 中间那一帧「球压在别的球上」看起来像渲染错乱。
function onTap(point: Point) {
  if (!mustPlace.value || placing.value) {
    return
  }
  const spot = clampIntoCueArea(point)
  const seq = socket?.placeCue(spot.x, spot.y) ?? 0
  if (seq === 0) {
    showNotice('连接已断开，正在重连')
    return
  }
  placing.value = true
}

// clampIntoCueArea 把落点收进台面内沿。
//
// 服务端也会校验并拒绝越界，这里先夹一次是为了不让球被画到库边外面去。
// 中心留一个球半径的余量：球心压在库边上时球身会穿过木框。
function clampIntoCueArea(point: Point): Point {
  const minX = BALL_RADIUS
  const maxX = TABLE_WIDTH - BALL_RADIUS
  const minY = BALL_RADIUS
  const maxY = TABLE_LENGTH - BALL_RADIUS
  let x = point.x
  let y = point.y
  if (x < minX) {
    x = minX
  }
  if (x > maxX) {
    x = maxX
  }
  if (y < minY) {
    y = minY
  }
  if (y > maxY) {
    y = maxY
  }
  return { x, y }
}

// shoot 出杆：把五个参数发给服务端，等 shot 帧回来再播。
function shoot() {
  if (!canShoot.value || power.value < 0.02) {
    return
  }
  const params: ShotParams = {
    phi: phi.value,
    power: power.value,
    // 抬杆与侧旋暂不开放：交互只做角度与力度两个维度，
    // 服务端字段留着，接 UI 时不用改协议。
    theta: 0,
    a: 0,
    b: 0,
  }
  // 连接已经断了时 shoot 返回 0，那就别把在途标记立起来——
  // 立了之后没有 shot 帧回来解锁，力度条会永久变灰。
  const seq = socket?.shoot(params) ?? 0
  if (seq === 0) {
    showNotice('连接已断开，正在重连')
    return
  }
  shotPending.value = true
  // 出杆之后力度归零，下一杆从头拖。
  power.value = 0
}

// isPlaying 表示对局还在进行、也还没出结果。
// 「要不要判负」只看这一条：房间页与已结算的状态都不该弹确认框。
function isPlaying(): boolean {
  return snapshot.value !== null && snapshot.value.status === 'playing' && !result.value
}

// sendLeave 通知服务端退出，只发一次。
//
// 两个入口（点返回、浏览器返回键）会走到同一个守卫，
// 不去重就是两条 leave：第二条按「对局已结束」被拒，日志里白白多一条错误。
function sendLeave() {
  if (leaveSent) {
    return
  }
  leaveSent = true
  socket?.leave()
}

// leave 点左上角返回。对局还在进行时先确认：中途离开会被判负。
async function leave() {
  if (isPlaying()) {
    try {
      await ElMessageBox.confirm('对局还没结束，现在离开会判负。确定离开吗？', '离开对局', {
        confirmButtonText: '离开',
        cancelButtonText: '继续打',
        type: 'warning',
      })
    } catch {
      return
    }
    sendLeave()
  }
  backToLobby()
}

// rematch 再约一局：给同一个对手发一条新邀请，回大厅等对方接受。
//
// 复用大厅那条邀请流程而不是在本页干等：对局已经结算、服务端那一局也回收了，
// 重开一局必须走「邀请 → 对方接受 → 新房间」这条完整链路。
async function rematch() {
  const snap = snapshot.value
  const other: Seat = mySeat.value === 0 ? 1 : 0
  const target = snap ? snap.players[other].user_id : 0
  backToLobby()
  if (target === 0) {
    return
  }
  try {
    await pool.invite(target)
  } catch {
    // 对方已经下线、或者互相关注的关系变了，接口会拒。
    // 拦截器已经弹过提示，回到大厅重新挑人即可。
  }
}

// retryConnect 在放弃之后人工重试一次。
//
// 走到 gaveup 只说明「自动重连的窗口走完了」，不代表对局没了：
// 服务端不会因为断线判负，重连上来照样拿得到完整快照。
function retryConnect() {
  if (!socket) {
    return
  }
  // connect 内部会复位重试计数（见 protocol.ts），所以这是一次全新的重连。
  socket.connect()
}

// backToLobby 停掉一切并回大厅。
function backToLobby() {
  cleanup()
  void router.replace('/pool')
}

// cleanup 停止动画、计时器与连接。
// 卸载与「回大厅」都走它，避免两处各写一份收尾逻辑漏掉一样东西。
function cleanup() {
  disposed = true
  if (handle) {
    handle.stop()
    handle = null
  }
  if (tickTimer) {
    clearInterval(tickTimer)
    tickTimer = null
  }
  if (bannerTimer) {
    clearTimeout(bannerTimer)
    bannerTimer = null
  }
  if (noticeTimer) {
    clearTimeout(noticeTimer)
    noticeTimer = null
  }
  if (socket) {
    socket.close()
    socket = null
  }
}

onMounted(() => {
  const token = getToken()
  if (!token) {
    // 路由守卫已经拦过没登录的情况，走到这里说明 token 刚被清掉
    // （比如另一个标签页退出了登录）。
    void router.replace('/login')
    return
  }
  phi.value = initialPhi()

  socket = new GameSocket({
    matchId: props.id,
    // 传函数而不是当时的字符串：重连时它会重新取一次，
    // access 过期后由 request 层刷新令牌，这里拿到的是新的那个。
    token: () => getToken() || '',
    onMessage,
    onState: (state) => {
      connState.value = state
    },
  })
  socket.connect()

  // 1Hz 的读秒。不用 requestAnimationFrame：读秒每秒只变一次，
  // rAF 一秒跑 60 次纯属浪费，还会跟着屏幕刷新率抖动。
  //
  // 读秒走完由服务端推 turn 或 result，本地不主动判负：
  // 「超时判负」是规则，规则只有服务端算。
  tickTimer = setInterval(() => {
    nowTick.value = Date.now()
  }, 1000)
})

onBeforeUnmount(cleanup)

// 浏览器/手机的返回键不走 leave()，它直接换路由，确认框根本没机会弹。
// 这里补一发 leave：判负是规则，逃不掉；不加的话对手要干等满两次读秒
// （40 秒）才知道人已经走了。
onBeforeRouteLeave(() => {
  if (isPlaying()) {
    sendLeave()
  }
})
</script>

<style scoped>
/* 对局页是深色沉浸场景，颜色不套浅色主题 token。
   100dvh 而不是 100vh：移动端地址栏收起时 vh 不会变，
   用 vh 会让底部一截被工具栏压住。 */
.pm {
  display: flex;
  flex-direction: column;
  height: 100dvh;
  background: #0e1116;
}

.pm__bar {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
  height: 48px;
  padding: 0 10px;
  color: #e8eaed;
}

.pm__back {
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  padding: 0;
  color: inherit;
  background: rgba(255, 255, 255, 0.08);
  border: none;
  border-radius: 8px;
  cursor: pointer;
}

.pm__back:active {
  background: rgba(255, 255, 255, 0.18);
}

/* 双方条占满中间，时钟钉在右边。 */
.pm__seats {
  display: flex;
  flex: 1;
  gap: 4px;
  min-width: 0;
}

.pm__clock {
  flex: none;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  color: #8a919c;
}

/* 剩余空间全给球台。min-height: 0 是 flex 子项能收缩的前提，
   少了它 canvas 会按内容高度撑开，把顶栏顶出屏幕。
   右内边距给力度条让位，球台右库边不会被滑块压住。 */
.pm__stage {
  position: relative;
  flex: 1;
  min-height: 0;
  padding: 0 40px 4px 8px;
}

/* 力度条浮在球台右侧的留白里。
   高度取屏高的 56% 并且封顶 360px，不跟着球台拉伸：
   拉满整屏时滑块有 700px 高，拖一格 1% 要有 7px 精度，反而更难控制，
   视觉上也会像一根栏杆。 */
.pm__power {
  position: absolute;
  top: 50%;
  right: 4px;
  width: 32px;
  height: min(56%, 360px);
  transform: translateY(-50%);
}

.pm__foot {
  flex: none;
  padding: 2px 10px 8px;
}

/* 提示条浮在球台上方而不是顶栏里：
   犯规、掉线这些消息和球台是同一件事的上下文，
   放到顶栏会和双方条抢那一行的位置。 */
.pm__notice {
  position: absolute;
  top: 8px;
  left: 50%;
  max-width: calc(100% - 24px);
  padding: 7px 14px;
  font-size: 12px;
  color: #fff;
  background: rgba(18, 22, 30, 0.9);
  border-radius: 999px;
  transform: translateX(-50%);
  pointer-events: none;
}

.pm-notice-enter-active,
.pm-notice-leave-active {
  transition: opacity 0.2s ease, transform 0.2s ease;
}

.pm-notice-enter-from,
.pm-notice-leave-to {
  opacity: 0;
  transform: translateX(-50%) translateY(-6px);
}

/* 手中球提示贴在球台底部：手指正在台面上找落点，
   提示放在离手指最近的地方最不容易被忽略。 */
.pm__place {
  position: absolute;
  bottom: 10px;
  left: 50%;
  padding: 6px 14px;
  font-size: 12px;
  color: #0e1116;
  background: rgba(240, 192, 47, 0.92);
  border-radius: 999px;
  transform: translateX(-50%);
  pointer-events: none;
}

.pm__gone {
  position: fixed;
  inset: 0;
  z-index: 1500;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 18px;
  background: rgba(9, 12, 18, 0.86);
}

.pm__gone-text {
  margin: 0;
  font-size: 15px;
  color: #e8eaed;
}

.pm__gone-sub {
  margin: -10px 0 0;
  font-size: 12px;
  color: #8a919c;
}

.pm__gone-actions {
  display: flex;
  gap: 10px;
}

.pm__gone-btn {
  height: 42px;
  padding: 0 26px;
  font-size: 15px;
  font-weight: 600;
  color: #fff;
  background: #4c7ef3;
  border: none;
  border-radius: 12px;
  cursor: pointer;
}

/* 次级按钮必须写在主按钮之后：两条规则特异性相同（都是单类），
   谁在后面谁生效。写在前面会被主按钮的蓝底盖掉。 */
.pm__gone-btn--ghost {
  color: #c8ccd3;
  background: rgba(255, 255, 255, 0.1);
}
</style>
