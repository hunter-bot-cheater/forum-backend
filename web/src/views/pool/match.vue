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
      @again="backToLobby"
      @lobby="backToLobby"
    />

    <!-- 连接彻底放弃（重连多次失败）时才整屏拦一下：
         对局已经从服务端回收了，留在球台上只会让人对着一个死局拖力度。 -->
    <div v-if="connState === 'gaveup'" class="pm__gone" role="dialog" aria-modal="true">
      <p class="pm__gone-text">与对局的连接已断开</p>
      <button class="pm__gone-btn" type="button" @click="backToLobby">回大厅</button>
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
import { useRouter } from 'vue-router'
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
import type { BallState, MatchSnapshot, PoolDrag, Seat, ShotParams } from '@/game/types'
import { getToken } from '@/utils/auth'

const props = defineProps<{
  id: string
}>()

const router = useRouter()

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

// 动画句柄与后置消息队列。
//
// 犯规与换人帧会在一杆的动画还在播的时候到达（服务端是连着发的），
// 直接显示会让「球还在滚」和「已经换人」同时出现在屏幕上。
// 所以先存起来，等动画播完再放出去。
let handle: PlayHandle | null = null
let pendingFoul: FoulData | null = null
let pendingTurn: TurnData | null = null

// 拖动起点的角度。整个拖动过程都基于它加总位移，
// 逐帧累加会把浮点误差攒起来，拖久了瞄准线会慢慢偏。
let dragBase = 0

let socket: GameSocket | null = null
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
// 三个条件缺一不可：轮到我、不在播放动画、没有手中球。
const canShoot = computed(() => isMyTurn.value && !shooting.value && !mustPlace.value)

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

// applySnapshot 用服务端快照重建本地场景。
//
// 整份替换而不是增量合并：进房与重连拿到的都是完整状态，
// 合并还要处理「哪些球被删了、位置要不要保留」，而重建是零风险的。
function applySnapshot(snap: MatchSnapshot) {
  snapshot.value = snap
  mySeat.value = snap.seat
  turn.value = snap.turn
  deadlineAt.value = snap.deadline_ms > 0 ? Date.now() + snap.deadline_ms : 0

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
  if (msg.type === 'joined') {
    mySeat.value = msg.data.seat
    return
  }
  if (msg.type === 'state') {
    applySnapshot(msg.data)
    return
  }
  if (msg.type === 'shot') {
    playShotFrame(msg.data)
    return
  }
  if (msg.type === 'foul') {
    pendingFoul = msg.data
    return
  }
  if (msg.type === 'turn') {
    pendingTurn = msg.data
    // 没有动画在播（比如重连后收到的第一条就是 turn），直接放出去。
    if (!shooting.value) {
      flushAfterShot()
    }
    return
  }
  if (msg.type === 'result') {
    applyResult(msg.data)
    return
  }
  if (msg.type === 'peer') {
    onPeer(msg.data.event)
    return
  }
  if (msg.type === 'error') {
    showNotice(msg.data.message)
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
  shooting.value = true
  // 出杆后力度归零：下一杆要从头拖，留着上一次的满力容易误出杆。
  power.value = 0

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

// flushAfterShot 在动画播完之后把攒下的犯规与换人放出去。
function flushAfterShot() {
  if (pendingFoul) {
    const foul = pendingFoul
    pendingFoul = null
    showNotice(foul.reason)
  }
  if (pendingTurn) {
    const next = pendingTurn
    pendingTurn = null
    turn.value = next.seat
    deadlineAt.value = next.deadline_ms > 0 ? Date.now() + next.deadline_ms : 0
    showBanner(next.seat)
  }
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
function onTap(point: Point) {
  if (!mustPlace.value) {
    return
  }
  const spot = clampIntoCueArea(point)
  // 先把母球摆到本地再发给服务端：不等确认能避免「点了没反应」的错觉。
  // 服务端若判定非法会回 error，那条提示会盖过这次乐观更新，
  // 而下一份快照会把位置纠正回来。
  const cue = balls.value.find((b) => b.id === 'cue')
  if (cue) {
    cue.x = spot.x
    cue.y = spot.y
    cue.on_table = true
  }
  socket?.placeCue(spot.x, spot.y)
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
  socket?.shoot(params)
}

// leave 离开对局页。对局还在进行时先确认：中途离开会被判负。
async function leave() {
  const playing = snapshot.value !== null && snapshot.value.status === 'playing' && !result.value
  if (playing) {
    try {
      await ElMessageBox.confirm('对局还没结束，现在离开会判负。确定离开吗？', '离开对局', {
        confirmButtonText: '离开',
        cancelButtonText: '继续打',
        type: 'warning',
      })
    } catch {
      return
    }
    socket?.leave()
  }
  backToLobby()
}

// backToLobby 停掉一切并回大厅。
function backToLobby() {
  cleanup()
  void router.replace('/pool')
}

// cleanup 停止动画、计时器与连接。
// 卸载与「回大厅」都走它，避免两处各写一份收尾逻辑漏掉一样东西。
function cleanup() {
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
    token,
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
</style>
