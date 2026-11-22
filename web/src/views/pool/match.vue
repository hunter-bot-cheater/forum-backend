<template>
  <div class="pm">
    <!-- 顶栏三块：左上角一张座位卡、正中读秒、右上角另一张。
         以前两张卡挤在一条里，两个人黏在一起，看不出谁在左谁在右；
         拆到两个角上之后，「我」永远在左上，对手永远在右上，一眼能对上。 -->
    <header class="pm__bar">
      <div class="pm__corner">
        <button class="pm__back" type="button" aria-label="返回" @click="leave">
          <el-icon :size="18"><ArrowLeft /></el-icon>
        </button>
        <SeatCard
          v-if="leftCard"
          :player="leftCard.player"
          :group="leftCard.group"
          :remaining="leftCard.remaining"
          :active="leftCard.active"
          :is-me="leftCard.seat === mySeat"
          :continuing="leftCard.active && continuing"
        />
      </div>

      <span class="pm__clock">{{ clockText }}</span>

      <div class="pm__corner pm__corner--right">
        <SeatCard
          v-if="rightCard"
          :player="rightCard.player"
          :group="rightCard.group"
          :remaining="rightCard.remaining"
          :active="rightCard.active"
          :is-me="rightCard.seat === mySeat"
          :continuing="rightCard.active && continuing"
        />
      </div>
    </header>

    <main class="pm__stage fs-table-stage">
      <!-- 球台单独一层：力度条挂在它的右边，两者一起居中。
           以前力度条贴着屏幕右边缘，宽屏上离球台隔着好几百像素，
           眼睛要在两处来回跳。 -->
      <div class="pm__table fs-table-box">
        <PoolTable
          :balls="tableBalls"
          :aim="aim"
          :peer-aim="peerAim"
          :ghost="ghost"
          @drag="onDrag"
          @tap="onTap"
          @hover="onHover"
        />

        <!-- 换人横幅盖在球台上，不挡操作。 -->
        <TurnBanner
          :visible="bannerVisible"
          :is-me="bannerMine"
          :title="bannerTitle"
          :name="bannerName"
          :avatar="bannerAvatar"
          :subtitle="bannerSubtitle"
        />

        <!-- 自由球：拿到手中球的那一刻弹一块大牌子。
             这件事只在底栏写一行小字是看不出来的——玩家正盯着球台，
             不知道「为什么现在打不出杆」，只会觉得界面卡住了。
             牌子不拦点击（pointer-events: none），点哪里球就落在哪里。 -->
        <Transition name="pm-ballin">
          <div v-if="ballInHandTip" class="pm__ballin" role="status">
            <span class="pm__ballin-tag">自由球</span>
            <span class="pm__ballin-text">{{ ballInHandText }}</span>
          </div>
        </Transition>

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
      <!-- 桌下那一排：已经进袋的球，按落袋先后排开。
           它是「这一局打到哪了」最直观的一条，比顶部席位卡上的小球更清楚。 -->
      <div v-if="pottedOrder.length > 0" class="pm__potted" aria-label="已进袋的球">
        <span
          v-for="id in pottedOrder"
          :key="id"
          class="pm__potted-ball"
          :style="pottedStyle(id)"
          >{{ id }}</span
        >
      </div>
      <!-- 手中球：那圈微调尺用不上（方向还没定），换成一块明显的提示。
           提示留在底栏而不是只盖在球台上，是因为弹出来的牌子几秒后会收起，
           而「还在等他放球」这件事要一直看得到。
           两种形态：摆球时整行是提示条，右侧一个「去瞄准」按钮；
           其余时候微调尺照常显示，自由球期间在最左边多一颗「重摆」入口
           ——白球摆下去之后往往还要再挪两次，入口不能只给一次。 -->
      <div class="pm__ctls">
        <div v-if="placingMode" class="pm__place">
          <span class="pm__place-tag">自由球</span>
          <span class="pm__place-tip">点台面挪白球，可以反复挪</span>
          <button
            class="pm__place-btn"
            type="button"
            :disabled="!cueOnTable"
            @click="finishPlacing"
          >
            去瞄准
          </button>
        </div>
        <template v-else>
          <button v-if="ballInHand" class="pm__reopen" type="button" @click="reopenPlacing">
            自由球 · 重摆
          </button>
          <FineTuneRail :phi="phi" :disabled="!canAim" @nudge="onNudge" />
        </template>
      </div>
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
import { adjustPhi, aimFromCue, aimGuides, normalizePhi } from '@/game/aim'
import { applySample, playShot, type PlayHandle } from '@/game/animator'
import { ballLookOf } from '@/game/balls'
import type { Point } from '@/game/geometry'
import {
  GameSocket,
  type AimData,
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

// pottedOrder 是桌下那一排「已经进袋的球」，按落袋先后排。
//
// 不直接从 balls 里筛 on_table=false：那样得到的是摆球时的顺序，
// 一颗颗进袋的过程就看不出来了，而视频里那排球是越打越长的。
const pottedOrder = ref<string[]>([])

// peerAimState 是对面此刻的瞄准姿态（由服务端转发的 aim 帧刷新）。
//
// at 用来判断「这帧还新不新鲜」：对面掉了线或走了，最后一帧不该一直挂在台上。
const peerAimState = ref<{ seat: Seat; phi: number; power: number; at: number } | null>(null)

// AIM_STALE_MS 之后不再显示对面的杆子。正常瞄准是每 60ms 一帧，
// 四秒没新帧说明对面停了，再挂着一根杆会让人以为他还在瞄。
const AIM_STALE_MS = 4000

// AIM_SEND_INTERVAL 是上报瞄准的最小间隔。
//
// 60ms 约等于 16 帧每秒：对面看到的转杆是连贯的，而一条一局几分钟的连接
// 也不会被这个频率打满。手速再快也没必要每像素发一帧。
const AIM_SEND_INTERVAL = 60

// aimLocked 表示方向已经定住：鼠标再怎么移、手指再怎么拖，杆子都不动。
//
// 这是视频里那套操作的核心一步。不锁的话，手从球台移到右边的力度条这一段路
// 会把瞄准线一路带偏，最后一杆打出去的方向取决于鼠标怎么划过去的，
// 根本没法瞄。锁了之后，调力度、点微调都不会影响方向。
const aimLocked = ref(false)

// continuing 表示当前这一手是「上一杆进球之后接着打」，不是换人。
//
// 服务端换人帧只在座位号上体现结果：进了球回合还归同一个人，帧里的 seat
// 和上一杆一样。光看 seat 分不清「他连打」还是「正常轮到他」，
// 所以记住上一杆是谁打的，两者一比对就知道是不是连击。
const continuing = ref(false)

// lastShooter 是上一杆的击球方，用来判断下一帧 turn 是不是连击。
// 不能用 turn.value 比较：换人帧到达时 turn 已经被改成新的一方了。
let lastShooter: Seat | null = null

// 换人横幅
const bannerVisible = ref(false)
const bannerMine = ref(false)
const bannerTitle = ref('')
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

// shotTimer 是出杆之后等结果的兜底计时器，超时会把 shotPending 解开。
// 没有它的话，一杆在链路上丢掉（服务端重启、连接抖动）就会永久卡住：
// canShoot 一直为假，力度条永远是灰的，玩家只能刷新页面。
let shotTimer: ReturnType<typeof setTimeout> | null = null

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

// ballInHand 表示这一杆手里握着母球：摆到哪算哪，直到出杆之前都有效。
//
// 服务端在落位之后不会收回这项权利（只有出杆才收），所以摆完还能接着挪。
const ballInHand = computed(() => {
  if (!snapshot.value) {
    return false
  }
  return snapshot.value.ball_in_hand === true && isMyTurn.value
})

// cueOnTable 是「母球此刻在台上吗」。摔袋之后它是 false，
// 直到落位请求被服务端接受、新快照把球摆回去。
const cueOnTable = computed(() => {
  for (const ball of balls.value) {
    if (ball.id === 'cue') {
      return ball.on_table
    }
  }
  return false
})

// placingMode 是「正在摆白球」的本地模式：指针带着虚影走，点台面把它放下去。
//
// 由玩家自己进出（底栏两个按钮），只在一处自动切换——母球刚落到台上那一刻。
// 让它一直挂在摆球模式下，就永远没有瞄准线；反过来放一次就永久收起，
// 又变成「只能放一次」。
const placingMode = ref(false)

// cueInHand 表示母球还在手上：台面上不画它，由虚影代表。
const cueInHand = computed(() => placingMode.value && !cueOnTable.value)

// ghost 是手中球还没落位时跟着指针走的虚影位置（世界坐标）。
//
// 虚影就是那颗球本身，不是「预览」：指针指到哪它跟到哪，点一下台面就落定。
// 球一落到台上就收起来——两颗白球同时在台上，分不清哪颗能拖。
const ghost = ref<Point | null>(null)

// syncGhost 按当前模式决定虚影要不要出现、出现在哪。
//
// 母球还在台上时没有虚影；已经落袋了就摆到开球线附近，
// 免得虚影一出来就在袋口里（那里既放不下球，也看不清）。
function syncGhost() {
  if (!cueInHand.value) {
    ghost.value = null
    return
  }
  if (ghost.value) {
    return
  }
  ghost.value = { x: TABLE_WIDTH / 2, y: TABLE_LENGTH * 0.25 }
}

// ballInHandTip 控制「自由球」那块大牌子的显示。
//
// 只在拿到手中球的那一刻弹一次、几秒后收起：牌子会盖住半个球台，
// 一直挂着就没法看球位了。底栏那一条会一直留着，说明不会丢。
const ballInHandTip = ref(false)
let ballInHandTimer: ReturnType<typeof setTimeout> | null = null

// ballInHandText 是牌子上那句话：说的是「下一步做什么」。
// 母球还在台上时不能让他去点台面——那时点台面是锁方向。
const ballInHandText = computed(() =>
  placingMode.value ? '点一下台面，把白球放在那里' : '白球可以随时重摆',
)

// 拿到手中球的那一刻：进摆球模式，并弹一次大牌子——
// 这是「为什么现在打不出杆」的唯一解释。
//
// 母球还在台上时（犯规但没摔袋）先按瞄准处理：多数人只是要这一杆的击球权，
// 不想再看一遍自己已经能看见的球位。想挪他自然会点底栏的「自由球」。
watch(ballInHand, (has) => {
  if (ballInHandTimer) {
    clearTimeout(ballInHandTimer)
    ballInHandTimer = null
  }
  if (!has) {
    placingMode.value = false
    ballInHandTip.value = false
    return
  }
  placingMode.value = !cueOnTable.value
  ballInHandTip.value = true
  ballInHandTimer = setTimeout(() => {
    ballInHandTip.value = false
    ballInHandTimer = null
  }, 3200)
})

// 母球刚落到台上的那一刻回瞄准模式：摆下去之后玩家要的是瞄准线，
// 而不是继续跟着指针跑的虚影。想再挪由底栏的「自由球」按钮负责。
watch(cueOnTable, (onTable) => {
  if (onTable && placingMode.value) {
    placingMode.value = false
  }
})

watch([placingMode, cueOnTable], syncGhost)

// finishPlacing 收起摆球模式去瞄准。母球不在台上时按钮是禁用的，这里不再判。
function finishPlacing() {
  placingMode.value = false
}

// reopenPlacing 回到摆球模式，把母球再挪一挪。
// 顺手解锁方向：摆完球的方向和刚才瞄的不是同一条线。
function reopenPlacing() {
  placingMode.value = true
  aimLocked.value = false
}

// ===== 出杆后的本地起步 =====
//
// pool-sim 算一杆要 12 毫秒到 1.7 秒（开球最重：十六颗球、几百次接触），
// 这段时间里客户端只有一张静止的台面。等真实帧回来才动的话，
// 松手到出球之间会空一段，越长越像「点了没反应」。
//
// 这里用与服务端同一套出射速度（1.0 + power × 6.0 米每秒，见 pool-sim 的
// power_to_speed）先让母球沿瞄准方向滚 160 毫秒。收到真实帧时把播放起点
// 设成已经滚过的时长，白球从当前位置接着走，不会跳回起点。
const PREDICT_SECONDS = 0.16

// strikePos 是预测出来的母球位置（世界坐标），null 表示此刻没在预测。
const strikePos = ref<Point | null>(null)
// strikeAt 是出杆那一刻的时间戳，用来算已经滚了多久。
let strikeAt = 0
let strikeRaf = 0

// startStrikeRoll 按力度让母球先滚一小段。
//
// 位移上限取「沿瞄准方向最先撞到的那个东西」：撞球就停在接触点，撞库就停在库边。
// 不截断的话满力 160 毫秒能走 1.1 米，白球会直接穿过球堆。
function startStrikeRoll(shotPhi: number, shotPower: number) {
  stopStrikeRoll()
  const cue = balls.value.find((ball) => ball.id === 'cue')
  if (!cue || !cue.on_table) {
    return
  }
  const speed = 1 + shotPower * 6
  const dx = Math.cos(shotPhi)
  const dy = Math.sin(shotPhi)
  // 复用瞄准那套几何：它已经算好了「最先撞到谁、距离多少」。
  const limit = aimFromCue(balls.value, shotPhi).distance
  const fromX = cue.x
  const fromY = cue.y
  strikeAt = performance.now()

  const step = () => {
    const elapsed = (performance.now() - strikeAt) / 1000
    const travel = Math.min(speed * Math.min(elapsed, PREDICT_SECONDS), limit)
    strikePos.value = { x: fromX + dx * travel, y: fromY + dy * travel }
    // 走满这段时间，或者已经贴到要撞的东西上，就停手等真实帧。
    if (elapsed >= PREDICT_SECONDS || travel >= limit) {
      strikeRaf = 0
      return
    }
    strikeRaf = requestAnimationFrame(step)
  }
  step()
}

// stopStrikeRoll 收掉预测，返回母球已经滚过的秒数。
// 这个值给真实轨迹当起始偏移：真实帧的 t=0 就是出杆那一刻，
// 从 lead 开始播才对得上白球现在的位置。
function stopStrikeRoll(): number {
  if (strikeRaf) {
    cancelAnimationFrame(strikeRaf)
    strikeRaf = 0
  }
  strikePos.value = null
  if (strikeAt === 0) {
    return 0
  }
  const lead = Math.min((performance.now() - strikeAt) / 1000, PREDICT_SECONDS)
  strikeAt = 0
  return lead
}

// tableBalls 是画到球台上的球。
//
// 母球还在手上时把它去掉：那颗球现在在玩家手上，台面上由虚影代表。
// 不去掉的话会在台上看到两颗白球——一颗虚的跟着指针、一颗实的停在原处，
// 分不清哪颗才是真的要打的那颗。
//
// 预测中的母球单独覆盖坐标：只换这一颗的 x/y，其余球原样透传。
const tableBalls = computed(() => {
  const predicted = strikePos.value
  if (!cueInHand.value && !predicted) {
    return balls.value
  }
  const rest: BallState[] = []
  for (const ball of balls.value) {
    if (cueInHand.value && ball.id === 'cue') {
      continue
    }
    if (predicted && ball.id === 'cue') {
      rest.push({ ...ball, x: predicted.x, y: predicted.y })
      continue
    }
    rest.push(ball)
  }
  return rest
})

// canShoot 是「现在能不能出杆」的唯一判据。
// 五个条件缺一不可：轮到我、没有一杆在路上、不在播动画、不在摆球、母球在台上。
const canShoot = computed(
  () =>
    isMyTurn.value &&
    !shotPending.value &&
    !shooting.value &&
    !placingMode.value &&
    cueOnTable.value,
)

// refreshPotted 把「已经进袋的球」同步到桌下那一排。
//
// 已有的顺序不动，只把新掉袋的按号码补到末尾。重连或刷新时没有真实先后，
// 按号码补至少每次打开都一样，不会看起来在乱跳。
function refreshPotted() {
  const kept: string[] = []
  const off: string[] = []
  for (const ball of balls.value) {
    if (ball.on_table) {
      continue
    }
    off.push(ball.id)
    if (pottedOrder.value.includes(ball.id)) {
      kept.push(ball.id)
    }
  }
  const added: string[] = []
  for (const id of off) {
    if (!kept.includes(id)) {
      added.push(id)
    }
  }
  added.sort(byBallNumber)
  const next = kept.concat(added)

  // 只在真的变了才赋值：这个函数在每一帧动画里都会被调用，
  // 每次都换一个新数组会让整个底栏跟着重渲染。
  if (next.length !== pottedOrder.value.length) {
    pottedOrder.value = next
    return
  }
  for (let i = 0; i < next.length; i += 1) {
    if (next[i] !== pottedOrder.value[i]) {
      pottedOrder.value = next
      return
    }
  }
}

// byBallNumber 按球号排：母球排最后（正常不会出现在这里），非法号也排最后。
function byBallNumber(a: string, b: string): number {
  const na = Number(a)
  const nb = Number(b)
  const va = Number.isFinite(na) ? na : 99
  const vb = Number.isFinite(nb) ? nb : 99
  return va - vb
}

// pottedStyle 给桌下那一排的小球上色，和台面上的球对得上。
//
// 花色球用白底加一道色带，纯色球整颗上色；号码用深色字，
// 因为这一排的球只有二十来像素，白字压在黄球上根本看不清。
function pottedStyle(id: string): Record<string, string> {
  const look = ballLookOf(id)
  if (look.striped) {
    return {
      background:
        'linear-gradient(180deg, #fbfaf6 0 26%, ' +
        look.color +
        ' 26% 74%, #fbfaf6 74% 100%)',
    }
  }
  return { background: look.color }
}

// canAim 决定台上还有没有杆。
//
// 四个条件：轮到我、不在播动画、没有一杆在路上、不在摆球。
// 前两条之外的那一条是必须的：服务端算一杆要 12 毫秒到 1.7 秒（开球最重），
// 只等 shot 帧到达才收杆的话，松手之后球杆会继续挂在台上盯着白球，
// 玩家看到的是「我打了，但什么都没发生」。
// 摆球时也要收杆：白球还在手上，杆子没有立足点。
const canAim = computed(
  () => isMyTurn.value && !shooting.value && !shotPending.value && !placingMode.value,
)

// seats 是两张座位卡要传的数据：座位、该侧组别、该侧剩余球。
//
// 顺序固定是「我先、对手后」：左上角永远是查看者自己，右上角永远是对手。
// 不按座位号排——按座位号排的话，坐 1 号位的人会看到自己在右上角，
// 每次重开一局左右都要重新认一遍。
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

// 左上、右上各取一张。拆成两个 computed 而不是在模板里写 seats[0]：
// 数组可能为空（快照还没到），模板里取下标要额外判空，写在这里一次就够。
const leftCard = computed(() => seats.value[0] ?? null)
const rightCard = computed(() => seats.value[1] ?? null)

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
    // 球杆的朝向与后拉量都在渲染层算：这一层只把「往哪打、多大力、锁没锁」交出去。
    phi: phi.value,
    power: power.value,
    locked: aimLocked.value,
    guides: aimGuides(balls.value, phi.value, hit),
  }
})

// peerAim 是对面那一方的瞄准层，画在自己这一层下面、淡一档。
//
// 用本地同一套 aimFromCue 现算：服务端只转发角度与力度，杆子落在哪、
// 会撞到哪颗球，都是几何问题，本端算比多传一份数据更省也更不会不同步。
const peerAim = computed<AimLayer | null>(() => {
  const state = peerAimState.value
  if (!state || !snapshot.value || snapshot.value.status !== 'playing') {
    return null
  }
  // 自己发出去的帧（理论上服务端不会回显）不该画成对面的杆。
  if (state.seat === mySeat.value) {
    return null
  }
  if (Date.now() - state.at > AIM_STALE_MS) {
    return null
  }
  const cue = balls.value.find((b) => b.id === 'cue')
  if (!cue || !cue.on_table) {
    return null
  }
  const hit = aimFromCue(balls.value, state.phi)
  return {
    cue: { x: cue.x, y: cue.y },
    hit: { x: hit.x, y: hit.y, kind: hit.kind },
    phi: state.phi,
    power: state.power,
    locked: false,
    guides: aimGuides(balls.value, state.phi, hit),
  }
})

// onPeerAim 收到对面的瞄准帧：把角度与力度存下来，交给 peerAim 去画。
//
// 不校验轮次：服务端只会转发「当前回合的那个人」的帧，
// 这里再判一遍反而会在换人的瞬间把最后一帧挡掉，杆子闪一下。
function onPeerAim(data: AimData) {
  if (data.seat === mySeat.value) {
    return
  }
  peerAimState.value = { seat: data.seat, phi: data.phi, power: data.power, at: Date.now() }
}

// 上报自己的瞄准姿态，让对面与观战者看到杆子在转。
//
// 节流到 60ms：拖动一秒能产生上百个事件，全发没必要，而对面 16fps 已经连贯。
// 用「尾帧必发」的方式节流：间隔不够时挂一个定时器把最后一次的值补上去，
// 否则松手前的最后一个角度可能永远发不出去，对面看到的是上一帧的杆位。
let aimSentAt = 0
let aimTimer: ReturnType<typeof setTimeout> | null = null

// flushAim 立即发一帧当前姿态。
function flushAim() {
  aimTimer = null
  // 杆不在台上时不上报：对面看到一根不存在的杆在转，比看到杆停住更怪。
  if (!socket || !canAim.value) {
    return
  }
  aimSentAt = Date.now()
  socket.sendAim(phi.value, power.value)
}

watch([phi, power], () => {
  // 杆不在台上时不上报：对面看到一根不存在的杆在转，比看到杆停住更怪。
  if (!socket || !canAim.value) {
    return
  }
  const wait = AIM_SEND_INTERVAL - (Date.now() - aimSentAt)
  if (wait <= 0) {
    flushAim()
    return
  }
  if (aimTimer === null) {
    aimTimer = setTimeout(flushAim, wait)
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
  clearShotTimeout()
  // 本地那一段预测也要收掉：快照里的球位才是真的，继续盖着会画错。
  stopStrikeRoll()
  // 每次权威状态回来都解锁：球位可能变了，上一杆锁定的方向已经不作数。
  aimLocked.value = false
  // 对面的瞄准帧同样作废：快照之后球位可能全变了，旧角度画出来是错的。
  peerAimState.value = null
  // 快照本身不区分「连击」与「换人」，先按换人处理；
  // 同一批帧里紧随其后的 turn 帧会把它改回连击（见 flushAfterShot 的顺序）。
  continuing.value = false

  const list: BallState[] = []
  for (const ball of snap.balls) {
    list.push({ id: ball.id, x: ball.x, y: ball.y, z: ball.z, on_table: ball.on_table })
  }
  balls.value = list
  refreshPotted()

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
  if (msg.type === 'aim') {
    onPeerAim(msg.data)
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
  // 本地预测已经滚了一小段，真实轨迹从那个时刻接着播：
  // 白球从脚下继续走，不会跳回出杆位置重来一遍。
  const lead = stopStrikeRoll()
  clearShotTimeout()
  // 服务端已经收下这一杆并算完了，在途标记可以放开：
  // 从这里开始由 shooting 接管，直到动画播完都不允许再出杆。
  shotPending.value = false
  placing.value = false
  shooting.value = true
  // 记下这一杆是谁打的：紧跟着的 turn 帧要靠它判断「连击还是换人」。
  lastShooter = data.seat
  // 球已经打出去了，对面那根杆要立刻收起来，不能跟着轨迹一起挂在台上。
  peerAimState.value = null
  // 力度在发出去的那一刻就清零了，这里不再重复：
  // 两处都清会让人以为「播放帧」也要负责界面状态。

  handle = playShot(
    { frames: data.frames, duration: data.duration },
    {
      // 预测已经播掉的那一段跳过，从当前位置继续。
      startAt: lead,
      onSample: (sample) => {
        applySample(balls.value, sample)
        // 每帧对一次：这一杆里刚掉袋的球要立刻进桌下那一排，
        // 等动画结束才更新的话，进球那一下完全没有反馈。
        refreshPotted()
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
  refreshPotted()
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
  // 换人意味着上一手结束了：他停手之后留下的那根杆不该继续挂在台上。
  peerAimState.value = null
  // 回合还归上一杆那个人，说明他进了球在接着打；换人了才是真的换人。
  continuing.value = lastShooter !== null && data.seat === lastShooter
  showBanner(data.seat)
}

// applyResult 结算。只关心「我赢没赢」和机读的原因码，不显示积分。
function applyResult(data: ResultData) {
  result.value = { won: data.winner === mySeat.value, reason: data.reason }
}

// peerOfflineTimer 让「对手掉线了」晚 4 秒再说。
//
// 一次 1006 断线多半是网络抖了半秒，对面往往两秒内就自己接回来了。
// 立刻弹提示的后果是它闪一下又消失，比不提示更让人摸不着头脑；
// 反过来压一下，真的掉线只是晚四秒知道，代价小得多。
let peerOfflineTimer: ReturnType<typeof setTimeout> | null = null

function clearPeerOffline() {
  if (peerOfflineTimer) {
    clearTimeout(peerOfflineTimer)
    peerOfflineTimer = null
  }
}

// onPeer 处理对手进出。
function onPeer(event: string) {
  if (event === 'left') {
    clearPeerOffline()
    showNotice('对手离开了对局')
    return
  }
  if (event === 'offline') {
    clearPeerOffline()
    peerOfflineTimer = setTimeout(() => {
      peerOfflineTimer = null
      showNotice('对手掉线了，正在等他重连')
    }, 4000)
    return
  }
  if (event === 'joined') {
    clearPeerOffline()
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
  clearShotTimeout()
  // 这一杆被拒了，母球回到原位：留着预测位置会让人以为球已经打出去了。
  stopStrikeRoll()
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
//
// 三种情况分开写文案：连击时说「继续击打」，别让人以为自己刚被换上场——
// 连击和换人的区别全在「上一杆也是他打的」，横幅要替玩家把这件事说明白。
function showBanner(seat: Seat) {
  const snap = snapshot.value
  if (!snap) {
    return
  }
  const mine = seat === mySeat.value
  bannerMine.value = mine
  bannerName.value = snap.players[seat].nickname
  bannerAvatar.value = snap.players[seat].avatar
  if (continuing.value) {
    bannerTitle.value = '继续击打'
    bannerSubtitle.value = mine ? '进了球，接着来' : '他进球了，接着打'
  } else {
    bannerTitle.value = mine ? '你的回合' : '对手回合'
    bannerSubtitle.value = mine ? '该你出杆了' : '等他出杆'
  }
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
  // 球还在手上时，指针就是那颗球：桌面端指到哪虚影跟到哪，不用按住拖。
  if (cueInHand.value) {
    moveGhost(point)
    return
  }
  if (!canAim.value || aimLocked.value) {
    return
  }
  aimTowards(point)
}

// aimTowards 把瞄准线指向台面上的某一点。
//
// 桌面端靠鼠标悬停、手机端靠手指拖动，两条路都走这里：手指/指针在哪，
// 球就往哪走。以前手机端按水平位移换算角度，斜着划或者在屏幕中间来回擦时
// 方向会和手感对不上——有时反着走，有时干脆不动，因为竖直分量被丢掉了。
function aimTowards(point: Point) {
  const cue = balls.value.find((b) => b.id === 'cue')
  if (!cue || !cue.on_table) {
    return
  }
  const dx = point.x - cue.x
  const dy = point.y - cue.y
  // 指针压在母球上时方向没有意义，保持原角度，免得线在原地乱抖。
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

// onDrag 处理画布拖动：拖动即瞄准，方向指向手指所在的位置。
//
// 触摸屏上没有悬停，这是手机端唯一的瞄准方式；鼠标上它等同于按住跟手。
// 锁定之后一律不理，理由同 onHover。
function onDrag(payload: PoolDrag) {
  // 摆球模式下拖动就是挪球，抬手那一下落位。
  // 不做「拖动中实时落位」：一帧一条请求没有意义，服务端只认最后那个点。
  if (placingMode.value) {
    if (!payload.point) {
      return
    }
    if (payload.phase === 'end') {
      placeCue(payload.point)
      return
    }
    // 只有还没落位的那颗跟着手指走。已经在台上的不动：
    // 跟着跑的话看起来像被拖走了，却不知道落在哪。
    if (cueInHand.value && payload.phase === 'move') {
      moveGhost(payload.point)
    }
    return
  }
  if (!canAim.value || aimLocked.value || !payload.point) {
    return
  }
  aimTowards(payload.point)
}

// onNudge 处理底部微调按钮与键盘方向键上报的度数增量。
function onNudge(deltaDeg: number) {
  if (!canAim.value) {
    return
  }
  phi.value = adjustPhi(phi.value, deltaDeg)
}

// onTap 处理台面点按。两种用意，按当前状态分流：
//   - 摆球模式：点哪里就把母球放到哪里（已经放下的也可以再挪）；
//   - 平时：锁定 / 解锁方向。锁上之后才能安心去拖力度条。
function onTap(point: Point) {
  if (placingMode.value) {
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
  // 出杆这一刻就让母球先滚起来：服务端算一杆 12 毫秒到 1.7 秒，
  // 这段时间干等着只会让人觉得没打出去（收到真实帧时按已滚时长接着播）。
  startStrikeRoll(params.phi, params.power)
  // 兜底：开球是最慢的一杆（约 1.7 秒），12 秒还没有任何帧回来
  // 说明这一杆在链路上丢了。不兜底的话 shotPending 永远为真，
  // 力度条会一直灰着，玩家只能刷新页面。
  if (shotTimer) {
    clearTimeout(shotTimer)
  }
  shotTimer = setTimeout(() => {
    shotTimer = null
    if (!shotPending.value) {
      return
    }
    shotPending.value = false
    stopStrikeRoll()
    showNotice('这一杆没有收到结果，可以重新出杆')
  }, SHOT_TIMEOUT_MS)
  // 出杆之后力度归零、方向解锁：下一杆从「自由瞄准」开始。
  power.value = 0
  aimLocked.value = false
  // 连击标记跟着这一杆一起结束：进球之后接着打，是下一次判定的结果，
  // 不是一直挂着的状态。不在这里清掉，出杆动画期间卡片上还写着「继续击打」。
  continuing.value = false
}

// SHOT_TIMEOUT_MS 是「一杆发出去之后最多等多久」。
// 取值比服务端调物理的超时（10 秒）多留两秒，保证客户端不会先于服务端放弃。
const SHOT_TIMEOUT_MS = 12_000

// clearShotTimeout 在收到任何与这一杆有关的帧时调用。
// shot / foul / turn / state / error 任意一条到，都说明服务端已经有结论了。
function clearShotTimeout() {
  if (shotTimer) {
    clearTimeout(shotTimer)
    shotTimer = null
  }
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
  // 节流中的瞄准帧也要撤掉：组件已经卸了，定时器再发一帧只是白发。
  if (aimTimer) {
    clearTimeout(aimTimer)
    aimTimer = null
  }
  if (noticeTimer) {
    clearTimeout(noticeTimer)
    noticeTimer = null
  }
  if (ballInHandTimer) {
    clearTimeout(ballInHandTimer)
    ballInHandTimer = null
  }
  // 「对手掉线了」那条延迟提示也要撤：组件没了还弹给谁看。
  clearPeerOffline()
  // 出杆兜底计时与本地预测都要一起收：留着的话组件已经销毁，
  // 超时回调仍会去改 ref、预测的 rAF 仍会继续跑。
  clearShotTimeout()
  stopStrikeRoll()
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

/* 顶栏三列：左边一张卡、中间读秒、右边另一张卡。
   用 grid 的 1fr auto 1fr 而不是 flex：读秒要钉在屏幕正中，
   两侧内容宽度不一样时 flex 会让它偏，grid 的两条 1fr 把它夹在中间。 */
.pm__bar {
  display: grid;
  flex: none;
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
  gap: 8px;
  width: 100%;
  max-width: 820px;
  min-height: 58px;
  margin: 0 auto;
  padding: 6px 10px;
  color: #e8eaed;
}

/* 左上角：返回键贴着左边，座位卡紧跟着。 */
.pm__corner {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

/* 右上角：只有卡片，靠到右边缘。 */
.pm__corner--right {
  justify-content: flex-end;
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

/* 读秒压在两张卡中间，号码固定宽度不跟着跳。 */
.pm__clock {
  flex: none;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  color: #8a919c;
  white-space: nowrap;
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
     以前是 -44px，宽屏上眼睛要在球台与力度条之间来回跳。
     宽度 44 是给手指留的热区，滑轨本身还是 12px、位置不变。 */
  right: -30px;
  width: 44px;
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

/* 桌下那一排已进袋的球：按落袋先后从左往右排，新进来的有一个落下的动作。 */
.pm__potted {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  justify-content: center;
  padding: 0 4px 2px;
}

.pm__potted-ball {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  font-size: 9px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  color: #fff;
  text-shadow: 0 1px 1px rgba(0, 0, 0, 0.55);
  border-radius: 50%;
  box-shadow:
    inset 0 -3px 5px rgba(0, 0, 0, 0.32),
    inset 0 2px 3px rgba(255, 255, 255, 0.35),
    0 1px 2px rgba(0, 0, 0, 0.45);
  animation: pm-potted-in 240ms ease-out;
}

/* 从上方落进这一排：进袋是「往下掉」，落到槽里那一下要有点惯性。 */
@keyframes pm-potted-in {
  from {
    opacity: 0;
    transform: translateY(-7px) scale(0.55);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

/* 提示条浮在球台正上方而不是顶栏里：
   犯规、掉线这些消息和球台是同一件事的上下文，
   放到顶栏会和座位卡抢那一行的位置。
   字号按「不用凑近也能读到」定，这一条是玩家唯一能知道「刚才发生了什么」的地方。
   圆角给 16px 而不是胶囊形：犯规说明会折成两行，胶囊圆角在换行后像一块药片。 */
.pm__notice {
  position: absolute;
  top: 18px;
  left: 50%;
  max-width: min(90%, 420px);
  padding: 10px 20px;
  font-size: 14px;
  line-height: 1.5;
  color: #fff;
  text-align: center;
  background: rgba(18, 22, 30, 0.92);
  border-radius: 16px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.34);
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

/* 自由球大牌子：盖在球台正中偏上，闪一下就走。
   竖排而不是横排——「自由球」三个字是这一下的全部信息，
   横着写会和后面的说明挤成一行，反而看不出重点。 */
.pm__ballin {
  position: absolute;
  top: 50%;
  left: 50%;
  z-index: 20;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 18px 34px 16px;
  background: rgba(28, 32, 44, 0.9);
  border: 1px solid rgba(240, 192, 47, 0.55);
  border-radius: 20px;
  box-shadow: 0 16px 40px rgba(0, 0, 0, 0.45);
  transform: translate(-50%, -50%);
  /* 不拦点击：牌子就在球台中间，点它后面的台面也得能落球。 */
  pointer-events: none;
}

.pm__ballin-tag {
  font-size: 26px;
  font-weight: 800;
  letter-spacing: 4px;
  color: #f0c02f;
  text-shadow: 0 2px 10px rgba(240, 192, 47, 0.35);
}

.pm__ballin-text {
  font-size: 14px;
  color: rgba(255, 255, 255, 0.82);
}

.pm-ballin-enter-active,
.pm-ballin-leave-active {
  transition: opacity 0.22s ease, transform 0.22s ease;
}

.pm-ballin-enter-from,
.pm-ballin-leave-to {
  opacity: 0;
  transform: translate(-50%, -50%) scale(0.86);
}

/* 手中球时底栏换成一块黄底提示条：落位靠点台面，不放按钮——
   球跟着指针走，挪去按按钮的路上球就跑了。
   「去瞄准」是例外：白球已经落在台上，那颗按钮不会把球带走。 */
.pm__ctls {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
}

.pm__place {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: center;
  gap: 10px;
  height: 44px;
  border: 1px solid rgba(240, 192, 47, 0.4);
  border-radius: 12px;
  background: rgba(240, 192, 47, 0.12);
}

.pm__place-btn {
  flex: none;
  height: 28px;
  padding: 0 12px;
  font-size: 12px;
  font-weight: 700;
  color: #2a2100;
  background: #f0c02f;
  border: none;
  border-radius: 8px;
  cursor: pointer;
}

.pm__place-btn:disabled {
  opacity: 0.4;
  cursor: default;
}

/* 瞄准状态下的「重摆」入口：做成描边胶囊而不是实心按钮，
   它是个补充入口，不该比微调尺更抢眼。 */
.pm__reopen {
  flex: none;
  height: 30px;
  padding: 0 10px;
  font-size: 12px;
  font-weight: 600;
  color: #f0c02f;
  white-space: nowrap;
  background: rgba(240, 192, 47, 0.14);
  border: 1px solid rgba(240, 192, 47, 0.4);
  border-radius: 999px;
  cursor: pointer;
}

.pm__reopen:active {
  background: rgba(240, 192, 47, 0.26);
}

.pm__place-tag {
  padding: 2px 9px;
  font-size: 12px;
  font-weight: 700;
  color: #2a2100;
  background: #f0c02f;
  border-radius: 999px;
}

.pm__place-tip {
  font-size: 13px;
  color: #f4d98a;
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
  font-size: 18px;
  color: #e8eaed;
}

.pm__gone-sub {
  margin: -10px 0 0;
  font-size: 13px;
  color: #8a919c;
}

.pm__gone-actions {
  display: flex;
  gap: 10px;
}

.pm__gone-btn {
  height: 46px;
  padding: 0 28px;
  font-size: 16px;
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
