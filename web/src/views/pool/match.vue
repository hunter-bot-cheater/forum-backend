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

    <main class="pm__stage fs-table-stage">
      <!-- 球台单独一层：力度条挂在它的右边，两者一起居中。
           以前力度条贴着屏幕右边缘，宽屏上离球台隔着好几百像素，
           眼睛要在两处来回跳。 -->
      <div class="pm__table fs-table-box">
        <PoolTable
          :balls="tableBalls"
          :aim="aim"
          :ghost="ghost"
          @drag="onDrag"
          @tap="onTap"
          @hover="onHover"
        />

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

        <div class="pm__power">
          <PowerSlider v-model:value="power" :disabled="!canShoot" @shoot="shoot" />
        </div>
      </div>
    </main>

    <footer class="pm__foot">
      <!-- 手中球：那圈微调尺用不上（方向还没定），换成一行提示。
           提示留在底栏而不是球台上，免得压住库边影响点击。 -->
      <div v-if="mustPlace" class="pm__place">
        <span class="pm__place-tip">点一下台面，白球就放在那里</span>
      </div>
      <FineTuneRail v-else :phi="phi" :disabled="!canAim" @nudge="onNudge" />
    </footer>

    <ResultSheet
      v-if="result"
      :won="result.won"
      :reason="result.reason"
      @again="rematch"
      @lobby="backToLobby"
    />

    <!-- 连接彻底放弃（重连窗口走完）时才整屏拦一下：
         留在球台上只会让人对着一个死局拖力度。
         但给一个「重新连接」：断网超过四十秒才会走到这里，
         而人多半只是切了一下网，直接赶回大厅等于这局白打。 -->
    <div v-if="connState === 'gaveup'" class="pm__gone" role="dialog" aria-modal="true">
      <p class="pm__gone-text">{{ fatalText || '与对局的连接已断开' }}</p>
      <p class="pm__gone-sub">
        {{ fatalText ? '这一局已经结算，回大厅可以再开一局。' : '网络恢复后可以重试；对局还在服务端等你。' }}
      </p>
      <div class="pm__gone-actions">
        <button class="pm__gone-btn pm__gone-btn--ghost" type="button" @click="backToLobby">
          回大厅
        </button>
        <!-- 对局已经没了的时候不给重连：点一百次也都是同一个 4004。 -->
        <button v-if="!fatalText" class="pm__gone-btn" type="button" @click="retryConnect">
          重新连接
        </button>
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
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
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
// mySeat 是「我坐哪」。null 表示还没确定——收到观战快照时会让位跳走，
// 那一瞬间不能默认成座位 0，否则界面会把左边的球当成自己的、
// 还把力度条点亮，看上去能出杆。
const mySeat = ref<Seat | null>(null)
const turn = ref<Seat>(0)
// deadlineAt 是本杆读秒的本地截止时刻（毫秒时间戳）。
// 服务端给的是「还剩多少毫秒」，换成绝对时刻本地才能自己倒数。
const deadlineAt = ref(0)
const nowTick = ref(Date.now())
const connState = ref<ConnectionState>('connecting')
// fatalText 有值表示服务端明确说了「这一局不在了」（关闭码 4004）。
// 与普通断线分开：普通断线该给重连，这一种给重连只是让人白点。
const fatalText = ref('')

// ===== 本地状态 =====

const power = ref(0)
const phi = ref(0)
const shooting = ref(false)
const notice = ref('')

// aimLocked 表示方向已经定住：鼠标再怎么移、手指再怎么拖，杆子都不动。
//
// 这是视频里那套操作的核心一步。不锁的话，手从球台移到右边的力度条这一段路
// 会把瞄准线一路带偏，最后一杆打出去的方向取决于鼠标怎么划过去的，
// 根本没法瞄。锁了之后，调力度、点微调都不会影响方向。
const aimLocked = ref(false)

// 换人横幅
const bannerVisible = ref(false)
const bannerMine = ref(false)
const bannerName = ref('')
const bannerAvatar = ref('')
const bannerSubtitle = ref('')

// 结算
const result = ref<{ won: boolean; reason: string } | null>(null)

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
  if (!snapshot.value || mySeat.value === null) {
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

// ghost 是手中球还没落位时跟着指针走的虚影位置（世界坐标）。
//
// 虚影就是那颗球本身，不是「预览」：指针指到哪它跟到哪，点一下台面就落定。
// 不用底栏按钮确认，是因为球会一直跟着指针——挪开鼠标去按按钮的路上，
// 球已经跟着走了，永远按不出「先摆好后确认」。
const ghost = ref<Point | null>(null)

// 拿到手中球时把虚影摆在一个合理的位置：母球还在台上就摆它现在的位置
// （视觉上「球被拿起来了」），已经落袋了就摆在开球线附近，免得虚影出现在袋口。
watch(mustPlace, (holding) => {
  if (!holding) {
    ghost.value = null
    return
  }
  const cue = balls.value.find((b) => b.id === 'cue')
  ghost.value =
    cue && cue.on_table
      ? { x: cue.x, y: cue.y }
      : { x: TABLE_WIDTH / 2, y: TABLE_LENGTH * 0.25 }
})

// tableBalls 是画到球台上的球。
//
// 手里握着母球时把它去掉：那颗球现在在玩家手上，台面上由虚影代表。
// 不去掉的话会在台上看到两颗白球——一颗虚的跟着指针、一颗实的停在原处，
// 分不清哪颗才是真的要打的那颗。
const tableBalls = computed(() => {
  if (!mustPlace.value) {
    return balls.value
  }
  const rest: BallState[] = []
  for (const ball of balls.value) {
    if (ball.id !== 'cue') {
      rest.push(ball)
    }
  }
  return rest
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
  if (me === null) {
    return []
  }
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
  // 手里握着球时不画瞄准线：母球还没落位，这条线指向的是台面上那颗
  // 等着被拿走的球，跟着它瞄只会误导向。
  if (mustPlace.value) {
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
    // 球杆的朝向与后拉量都在渲染层算：这一层只把「往哪打、多大力、锁没锁」交出去。
    phi: phi.value,
    power: power.value,
    locked: aimLocked.value,
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
  // 每次权威状态回来都解锁：球位可能变了，上一杆锁定的方向已经不作数。
  aimLocked.value = false

  const list: BallState[] = []
  for (const ball of snap.balls) {
    list.push({ id: ball.id, x: ball.x, y: ball.y, z: ball.z, on_table: ball.on_table })
  }
  balls.value = list

  // 默认瞄准角必须在球位落地之后算：它要读母球与球堆的位置。
  // 放在上面会让第一杆永远朝 +x（phi=0）打出去——开球直接空杆犯规。
  if (first) {
    phi.value = initialPhi()
  }

  // 重连进来时对局可能已经结束，快照里带的就是结果。
  if (snap.status === 'finished' && snap.winner !== null) {
    result.value = { won: snap.winner === snap.seat, reason: snap.win_reason }
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

// applyResult 结算。只关心「我赢没赢」和机读的原因码，不显示积分。
function applyResult(data: ResultData) {
  result.value = { won: data.winner === mySeat.value, reason: data.reason }
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

// initialPhi 算进房后的默认瞄准角：指向「正前方最近的那颗球」。
//
// 开球时那就是球堆的顶点。不能默认朝 +y 直推：母球并不在台面中线上
// （x=0.5944，顶点球在 0.4953），直推会从侧面切过去，球堆只散一半，
// 母球还会带着大半速度撞库，很容易直接摔袋——第一次开球就吃犯规。
//
// 后面每一杆也复用这条规则：瞄准最近的一颗球是一个不至于离谱的起点，
// 玩家要打别的球自己转就是。
function initialPhi(): number {
  const cue = balls.value.find((b) => b.id === 'cue')
  if (!cue) {
    return 0
  }
  let targetX = cue.x
  let targetY = cue.y + 1
  let best = Number.POSITIVE_INFINITY
  for (const ball of balls.value) {
    if (ball.id === 'cue' || !ball.on_table || ball.y <= cue.y) {
      continue
    }
    const distance = Math.hypot(ball.x - cue.x, ball.y - cue.y)
    if (distance < best) {
      best = distance
      targetX = ball.x
      targetY = ball.y
    }
  }
  return normalizePhi(Math.atan2(targetY - cue.y, targetX - cue.x))
}

// onHover 处理鼠标在台面上的移动：让瞄准线指向指针。
//
// 「指哪打哪」比「左右拖着转」直观得多，尤其在这一页——玩家盯着的是目标球，
// 而不是屏幕下方的刻度。锁定之后就不再跟随，否则手去点力度条的路上会把方向带偏。
function onHover(point: Point) {
  if (!canAim.value) {
    return
  }
  // 手里握着球时，指针就是那颗球：桌面端指到哪虚影跟到哪，不用按住拖。
  if (mustPlace.value) {
    moveGhost(point)
    return
  }
  if (aimLocked.value) {
    return
  }
  const cue = balls.value.find((b) => b.id === 'cue')
  if (!cue || !cue.on_table) {
    return
  }
  // 指针压在母球上时方向没有意义，保持原角度，免得线在原地乱抖。
  const dx = point.x - cue.x
  const dy = point.y - cue.y
  if (Math.hypot(dx, dy) < BALL_RADIUS * 1.2) {
    return
  }
  phi.value = normalizePhi(Math.atan2(dy, dx))
}

// moveGhost 把手中球的虚影挪到指针处，并夹进台面内沿。
//
// 夹在本地做而不是等服务端拒绝：越界的虚影会画到库边木框外面，
// 看起来像球穿进了桌子。真正落位时服务端仍会校验一次。
function moveGhost(point: Point) {
  ghost.value = clampIntoCueArea(point)
}

// onDrag 处理画布拖动：只认水平位移，换算出新的瞄准角。
//
// 触摸屏上没有悬停，转角度就靠这一条；鼠标上它只是「按住微调」的快捷方式。
// 锁定之后一律不理，理由同 onHover。
function onDrag(payload: PoolDrag) {
  // 手里握着球时拖动就是挪球。触摸屏没有悬停，这是手机上唯一的挪法。
  if (mustPlace.value) {
    if (payload.phase !== 'start' && payload.point) {
      moveGhost(payload.point)
    }
    return
  }
  if (!canAim.value || aimLocked.value) {
    return
  }
  if (payload.phase === 'start') {
    dragBase = phi.value
    return
  }
  phi.value = rotateByDrag(dragBase, payload.dx)
}

// onNudge 处理底部微调按钮与键盘方向键上报的度数增量。
function onNudge(deltaDeg: number) {
  if (!canAim.value) {
    return
  }
  phi.value = adjustPhi(phi.value, deltaDeg)
}

// onTap 处理台面点按。两种用意，按当前状态分流：
//   - 手里握着母球：点哪里就落在哪里；
//   - 平时：锁定 / 解锁方向。锁上之后才能安心去拖力度条。
function onTap(point: Point) {
  if (mustPlace.value) {
    placeCue(point)
    return
  }
  if (!canAim.value) {
    return
  }
  aimLocked.value = !aimLocked.value
  if (aimLocked.value) {
    showNotice('方向已锁定，拖右边的力度条出杆')
    return
  }
  showNotice('可以重新瞄准了')
}

// placeCue 把母球放到点到的位置。
//
// 不做乐观更新：服务端落位成功会广播一份完整快照，母球位置从那里来。
// 先在本地挪过去的话，一旦被判越界（或落点在别的球上）就得再挪回来，
// 中间那一帧「球压在别的球上」看起来像渲染错乱。
function placeCue(point: Point) {
  if (placing.value) {
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
  // 出杆之后力度归零、方向解锁：下一杆从「自由瞄准」开始。
  power.value = 0
  aimLocked.value = false
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
  const other: Seat = (mySeat.value ?? 0) === 0 ? 1 : 0
  const target = snap ? snap.players[other].user_id : 0
  // 昵称一起带过去：回大厅时在线名单还没拉回来，那里现查会先显示成「对方」。
  const nickname = snap ? snap.players[other].nickname : ''
  backToLobby()
  if (target === 0) {
    return
  }
  try {
    await pool.invite(target, nickname)
  } catch {
    // 对方已经下线、或者互相关注的关系变了，接口会拒。
    // 拦截器已经弹过提示，回到大厅重新挑人即可。
  }
}

// onKey 让桌面端用方向键微调：0.1°，按住 Shift 是 1°。
//
// 鼠标瞄准想要更细的一档时，用手去够底部那两个按钮会带偏指针，
// 键盘是这一页唯一「不会动到指针」的微调方式。
function onKey(e: KeyboardEvent) {
  if (!canAim.value) {
    return
  }
  if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') {
    return
  }
  // 方向键默认会滚动页面。对局页本来就不滚，但拦一下更稳。
  e.preventDefault()
  const step = e.shiftKey ? 1 : 0.1
  onNudge(e.key === 'ArrowLeft' ? -step : step)
}

// pointerIsCoarse 判断当前是不是触摸为主的设备。
// 提示文案要按它换：手机上「移动鼠标」是句废话。
function pointerIsCoarse(): boolean {
  if (typeof window.matchMedia !== 'function') {
    return false
  }
  return window.matchMedia('(pointer: coarse)').matches
}

// retryConnect 在放弃之后人工重试一次。
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
  window.removeEventListener('keydown', onKey)
}

// 第一次轮到我时教一遍操作。之后每回合都弹一条就成了噪音——
// 玩家第二次就记住了，第三次只会觉得挡住球台。
let hintShown = false
watch(canAim, (allowed) => {
  if (!allowed || hintShown) {
    return
  }
  hintShown = true
  if (pointerIsCoarse()) {
    showNotice('拖动台面瞄准，点一下台面锁定方向')
    return
  }
  showNotice('移动鼠标瞄准，点一下台面锁定方向')
})

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
    onFatal: () => {
      // 服务端在升级之后用关闭码说的那类拒绝：这一局已经不在了。
      // 不写具体文案区分，玩家这一侧只有「打完了」一种可能。
      fatalText.value = '这一局已经结束'
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

  window.addEventListener('keydown', onKey)
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
  /* 参考视频的底色不是纯黑，是一层带一点紫的深灰，中间偏亮。
     纯黑会把中灰的外框与绿呢一起压死，看着像贴纸。 */
  background: radial-gradient(120% 80% at 50% 40%, #2d2e3b 0%, #20212c 58%, #171720 100%);
}

.pm__bar {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
  width: 100%;
  max-width: 820px;
  height: 48px;
  margin: 0 auto;
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
  width: 100%;
  max-width: 820px;
  margin: 0 auto;
  /* 居中与球台比例走 .fs-table-stage（styles/index.css），这里只管留白。
     右边比左边多留一点：力度条挂在球台外面 24px 处，两边留白差 22px，
     正好让「球台 + 力度条」这一组的中心落在屏幕中线上。 */
  padding: 0 34px 4px 12px;
}

/* 球台容器：高度占满，宽度按球台的真实宽高比推出来。
   这一步是「力度条贴着球台」的关键——不让 canvas 自己在宽屏里留出几百像素，
   而是让容器的宽度就等于球台画出来的宽度。 */
.pm__table {
  position: relative;
}

/* 力度条浮在球台右侧的留白里，紧挨着库边。
   高度取屏高的 56% 并且封顶 360px，不跟着球台拉伸：
   拉满整屏时滑块有 700px 高，拖一格 1% 要有 7px 精度，反而更难控制，
   视觉上也会像一根栏杆。 */
.pm__power {
  position: absolute;
  top: 50%;
  /* 挂到球台外框外面一点，但贴着库边：从框沿到滑轨只有 12px 左右。
     以前是 -44px，宽屏上眼睛要在球台与力度条之间来回跳。 */
  right: -24px;
  width: 30px;
  height: min(48%, 300px);
  transform: translateY(-50%);
}

.pm__foot {
  flex: none;
  width: 100%;
  max-width: 820px;
  margin: 0 auto;
  padding: 2px 10px 8px;
}

/* 提示条浮在球台上方而不是顶栏里：
   犯规、掉线这些消息和球台是同一件事的上下文，
   放到顶栏会和双方条抢那一行的位置。
   圆角给 14px 而不是胶囊形：犯规说明会折成两行，胶囊圆角在换行后像一块药片。 */
.pm__notice {
  position: absolute;
  top: 16px;
  left: 50%;
  max-width: min(88%, 340px);
  padding: 7px 14px;
  font-size: 12px;
  line-height: 1.5;
  color: #fff;
  text-align: center;
  background: rgba(18, 22, 30, 0.9);
  border-radius: 14px;
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

/* 手中球只有一句提示：落位靠点台面，底栏不放按钮——
   球跟着指针走，挪去按按钮的路上球就跑了。 */
.pm__place {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 44px;
}

.pm__place-tip {
  font-size: 12px;
  color: rgba(255, 255, 255, 0.62);
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
