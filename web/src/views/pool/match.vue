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
        />
        <!-- 自己发出去的那几句，挂在自己头像下面。
             以前全挂在右上（对手头像下面），人机局里就成了「我发的话从机器人嘴里出来」，
             看着像它在说话。谁说的就挂谁那一侧，跟消息列表里的左右分栏一致。
             自己这一侧永远在左上，所以不需要按座位号判断。 -->
        <div v-if="myToasts.length > 0" class="pm__toasts pm__toasts--left" aria-live="polite">
          <span
            v-for="item in myToasts"
            :key="item.id"
            class="pm__toast is-mine"
            :class="{ 'is-sticker': isSticker(item.text) }"
            >{{ item.text }}</span
          >
        </div>
      </div>

      <div class="pm__mid">
        <!-- 连续对局的大比分。放在读秒上面：它是「这一轮谁占上风」的常驻信息，
             而读秒是每一杆都在变的。两边的座位卡已经标明谁在左，中间读作
             「我 : 对手」，不写名字也不会有歧义。 -->
        <span
          class="pm__series"
          :aria-label="'我赢 ' + myScore + ' 局，对手赢 ' + peerScore + ' 局'"
        >
          <b class="pm__series-num">{{ myScore }}</b>
          <i class="pm__series-colon">:</i>
          <b class="pm__series-num">{{ peerScore }}</b>
        </span>
        <span class="pm__clock">{{ clockText }}</span>
        <!-- 连击提示挂在读秒下面，不放进座位卡。
             放进卡片就得多一行：卡里两行已经 54px，再加一行「继续击打」，
             顶栏从 66px 涨到 86px，下面的球台跟着重新等比缩放一次。
             顶栏这块正好是玩家盯读秒的地方，放这里既显眼又不占额外高度。 -->
        <span v-if="continuing" class="pm__cont">继续击打</span>
      </div>

      <div class="pm__corner pm__corner--right">
        <SeatCard
          v-if="rightCard"
          :player="rightCard.player"
          :group="rightCard.group"
          :remaining="rightCard.remaining"
          :active="rightCard.active"
          :is-me="rightCard.seat === mySeat"
        />
        <!-- 对手说的话挂在他的头像下面。
             面板收起时对面那句只留一个未读点，看不到内容；把最近几句挂到
             对手头像下面，不用开抽屉也知道他说了什么。
             整条 4 秒后自己走开（不点不关），所以是 pointer-events: none。 -->
        <div v-if="peerToasts.length > 0" class="pm__toasts" aria-live="polite">
          <span
            v-for="item in peerToasts"
            :key="item.id"
            class="pm__toast"
            :class="{ 'is-sticker': isSticker(item.text) }"
            >{{ item.text }}</span
          >
        </div>
      </div>
    </header>

    <!-- 出杆倒计时的燃烧条：紧贴顶栏下沿，长度就是这一杆剩下的时间。
         从右往左烧断，末端留一点火星。它是「还剩多久」最容易用余光读到的东西，
         比中间那行「你 · 23s」快得多：同一件事，一个是读字，一个是看长度。
         等待开局与结算之后不画：那时没有读秒，画一条满格只会误导。
         位置必须一直占着（用 visibility 藏，不用 v-if）：这条 8px 一格的高，
         一出现一消失，下面的球台就要重新按比例缩放一次，看起来就是
         「打完一杆球桌动了一下」。 -->
    <div
      class="pm__fuse"
      :class="{ 'is-hidden': !fuseVisible, 'is-urgent': fusePct <= 25 }"
    >
      <div class="pm__fuse-fill" :style="{ width: fusePct + '%' }"></div>
    </div>

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
          :head-string="showHeadString"
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
            <span class="pm__ballin-tag">{{ behindHead ? '开球' : '自由球' }}</span>
            <span class="pm__ballin-text">{{ ballInHandText }}</span>
          </div>
        </Transition>

        <!-- 犯规与提示条。3 秒后自动消失，不需要用户去点。 -->
        <Transition name="pm-notice">
          <div v-if="notice" class="pm__notice">{{ notice }}</div>
        </Transition>

        <!-- 力度条这一条给的是「此刻该看谁的力度」：轮到自己就是自己拖，
             轮到对手就只显示对面推到了哪。
             不给两个根并排放，是因为位置一换，玩家的手会去找原来那一根。 -->
        <div class="pm__power" :class="{ 'is-peer': !isMyTurn }">
          <!-- 聊天入口：挂在力度条正上方，与滑轨同一根竖轴。
               以前贴在左侧库边上（46% 高），那儿正好是长库中间的袋口，
               气泡压掉半个洞，看着像少了一颗袋。
               挪到这一列是因为台面右外侧整条都是留白，压不到任何球洞；
               未读时右上角亮一个小红点，超过 9 条写成「9+」。
               做成实心蓝而不是半透明灰：对局中最该被一眼找到的入口只有这一个，
               灰底深图标的按钮在绿呢与深色背景里几乎是隐形的。 -->
          <button
            class="pm__chat-btn"
            type="button"
            :class="{ 'has-unread': chatUnread > 0 }"
            :aria-label="chatUnread > 0 ? '聊天，' + chatUnread + ' 条未读' : '聊天'"
            @click="toggleChat"
          >
            <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
              <path
                d="M12 3.5c-4.8 0-8.7 3.2-8.7 7.1 0 2.3 1.4 4.3 3.5 5.5l-.8 3.4 3.7-1.9c.7.1 1.5.2 2.3.2 4.8 0 8.7-3.2 8.7-7.2S16.8 3.5 12 3.5z"
                fill="currentColor"
              />
            </svg>
            <span v-if="chatUnread > 0" class="pm__chat-badge">{{
              chatUnread > 9 ? '9+' : chatUnread
            }}</span>
          </button>
          <span v-if="!isMyTurn" class="pm__power-tag">对方</span>
          <PowerSlider
            :value="powerShown"
            :disabled="!canShoot"
            :readonly="!isMyTurn"
            @update:value="onPowerInput"
            @shoot="shoot"
          />
          <!-- 加塞入口：挂在力度条正下方，跟滑轨同一根竖轴。
               放在力度条这一层而不是底栏，是因为「调力度」和「调击球点」是
               连着做的两件事，手不用在屏幕上下两端来回跑。
               面板从这颗球往上展开，盖住球台右下一块，和参考图一致。
               对手回合时它变成只读的击球点——红点挪到哪，对面打的就是哪。 -->
          <SpinPad
            :a="spinShownA"
            :b="spinShownB"
            :disabled="!canAim"
            :readonly="!isMyTurn"
            @update:a="onSpinInputA"
            @update:b="onSpinInputB"
            @open-change="onSpinOpenChange"
          />
        </div>

      </div>
    </main>

    <footer class="pm__foot">
      <!-- 桌下那一排：已经进袋的球，按落袋先后排开。
           它是「这一局打到哪了」最直观的一条，比顶部席位卡上的小球更清楚。
           一排空着也一直在 DOM 里（见下面的 min-height）：第一颗球进袋时才把它
           撑出来，整块底栏会长高 28px，球台跟着缩一截——那一瞬间看起来就像
           「球进了，画面跳了一下」。 -->
      <div
        class="pm__potted"
        :aria-label="pottedOrder.length > 0 ? '已进袋的球' : undefined"
      >
        <span
          v-for="id in pottedOrder"
          :key="id"
          class="pm__potted-ball"
          :style="pottedStyle(id)"
          >{{ id }}</span
        >
      </div>

      <!-- 音效开关：钉在底栏右下角（绝对定位，不占行）。
           不放进微调尺那一行的 flex 里：那一行中间是尺子，塞进去会把整条尺子挤偏。
           也不留在「桌下那一排」里：手机竖屏空间紧张时那一排会被整条收起
           （见文件末尾的竖屏媒体查询），开关跟着一起消失就点不到了。
           图标用内联 SVG 而不是组件库的：静音与有声两种画法想自己定。 -->
      <button
        class="pm__sound"
        type="button"
        :aria-label="soundOn ? '关闭音效' : '打开音效'"
        :aria-pressed="soundOn"
        :title="soundOn ? '关闭音效' : '打开音效'"
        @click="toggleSound"
      >
        <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
          <path d="M4 9.5h3.1L11.4 6.2v11.6L7.1 14.5H4z" fill="currentColor" />
          <g v-if="soundOn">
            <path
              d="M14.4 9.4a3.7 3.7 0 0 1 0 5.2"
              fill="none"
              stroke="currentColor"
              stroke-width="1.6"
              stroke-linecap="round"
            />
            <path
              d="M17 6.9a7.2 7.2 0 0 1 0 10.2"
              fill="none"
              stroke="currentColor"
              stroke-width="1.6"
              stroke-linecap="round"
            />
          </g>
          <path
            v-else
            d="M14.6 9.7l4.6 4.6M19.2 9.7l-4.6 4.6"
            fill="none"
            stroke="currentColor"
            stroke-width="1.7"
            stroke-linecap="round"
          />
        </svg>
      </button>
      <!-- 手中球：那圈微调尺用不上（方向还没定），换成一块明显的提示。
           提示留在底栏而不是只盖在球台上，是因为弹出来的牌子几秒后会收起，
           而「还在等他放球」这件事要一直看得到。
           两种形态：摆球时整行是提示条，右侧一个「去瞄准」按钮；
           其余时候微调尺照常显示，自由球期间在最左边多一颗「重摆」入口
           ——白球摆下去之后往往还要再挪两次，入口不能只给一次。 -->
      <div class="pm__ctls">
        <div v-if="placingMode" class="pm__place">
          <span class="pm__place-tag">{{ behindHead ? '开球' : '自由球' }}</span>
          <span class="pm__place-tip">{{ placingTip }}</span>
          <button
            class="pm__place-btn"
            type="button"
            :disabled="!cueOnTable"
            @click="finishPlacing"
          >
            {{ cueInHand ? '不挪了' : '去瞄准' }}
          </button>
        </div>
        <template v-else>
          <button v-if="ballInHand" class="pm__reopen" type="button" @click="reopenPlacing">
            {{ behindHead ? '开球 · 重摆' : '自由球 · 重摆' }}
          </button>
          <FineTuneRail :phi="phi" :disabled="!canAim" @nudge="onNudge" />
        </template>
      </div>
    </footer>

    <!-- 聊天面板：从底部推上来的一层，盖住底栏与球台下半部分。
         打开时不暂停对局、也不拦出杆——聊天是聊天，球照打。 -->
    <Transition name="pm-chat">
      <section v-if="chatOpen" class="pm__chat" role="dialog" aria-label="对局聊天">
        <header class="pm__chat-head">
          <span class="pm__chat-title">对局聊天</span>
          <button class="pm__chat-close" type="button" aria-label="收起聊天" @click="toggleChat">
            <el-icon :size="16"><Close /></el-icon>
          </button>
        </header>

        <div ref="chatBox" class="pm__chat-log">
          <p v-if="chatLog.length === 0" class="pm__chat-empty">说点什么吧，对面能立刻看到。</p>
          <!-- 自己的消息靠右、对手的靠左：与所有聊天软件一致，不用再认名字。 -->
          <div
            v-for="item in chatLog"
            :key="item.id"
            class="pm__chat-row"
            :class="{ 'is-mine': item.seat === mySeat }"
          >
            <span class="pm__chat-bubble" :class="{ 'is-sticker': isSticker(item.text) }">{{
              item.text
            }}</span>
            <span class="pm__chat-clock">{{ chatTime(item.at) }}</span>
          </div>
        </div>

        <div class="pm__chat-emojis">
          <button
            v-for="emoji in CHAT_EMOJIS"
            :key="emoji"
            class="pm__chat-emoji"
            type="button"
            :aria-label="'发送表情 ' + emoji"
            @click="sendEmoji(emoji)"
          >
            {{ emoji }}
          </button>
        </div>

        <div class="pm__chat-input">
          <!-- 输入框随内容长高（不写 rows）：一句话能看全，长句也不会缩成一行。
               列表自己是滚动的，输入框长高只会挤列表，不会把面板撑破。 -->
          <el-input
            v-model="chatText"
            type="textarea"
            :autosize="{ minRows: 1 }"
            :maxlength="120"
            placeholder="说句话…（回车发送，Shift+回车换行）"
            @keydown.enter="onChatEnter"
          />
          <button
            class="pm__chat-send"
            type="button"
            :disabled="chatText.trim() === ''"
            @click="sendChat"
          >
            发送
          </button>
        </div>
      </section>
    </Transition>

    <ResultSheet
      v-if="result"
      :won="result.won"
      :reason="result.reason"
      :waiting="pool.rematchWaiting"
      :peer-name="rematchPeer"
      :bot="isBotMatch"
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
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import { ArrowLeft, Close } from '@element-plus/icons-vue'
import { ElMessageBox } from 'element-plus'
import FineTuneRail from '@/components/pool/FineTuneRail.vue'
import PoolTable from '@/components/pool/PoolTable.vue'
import PowerSlider from '@/components/pool/PowerSlider.vue'
import ResultSheet from '@/components/pool/ResultSheet.vue'
import SeatCard from '@/components/pool/SeatCard.vue'
import SpinPad from '@/components/pool/SpinPad.vue'
import TurnBanner from '@/components/pool/TurnBanner.vue'
import { adjustPhi, aimFromCue, aimGuides, normalizePhi } from '@/game/aim'
import {
  applySample,
  playShot,
  SHOT_PLAY_RATE,
  type PlayHandle,
  type ShotFrame,
} from '@/game/animator'
import { buildShotSfx, sfx, type SfxCue } from '@/game/audio'
import { ballLookOf } from '@/game/balls'
import type { Point } from '@/game/geometry'
import { predictShot, speedOfPower, type PredictedShot } from '@/game/predict'
import {
  GameSocket,
  type AimData,
  type ChatData,
  type ConnectionState,
  type DownMessage,
  type FoulData,
  type ResultData,
  type ShotData,
  type ShotEvent,
  type TurnData,
} from '@/game/protocol'
import type { AimLayer } from '@/game/renderer'
import { aimForbidden, remainingOf } from '@/game/score'
import { BALL_RADIUS, CUE_HEAD_STRING, POCKETS, TABLE_LENGTH, TABLE_WIDTH } from '@/game/table'
import type {
  BallState,
  MatchSnapshot,
  PoolDrag,
  Seat,
  ShotParams,
  Viewer,
} from '@/game/types'
import { SEAT_SPECTATOR } from '@/game/types'
import { startBotMatch } from '@/api/pool'
import { getToken } from '@/utils/auth'
import { usePoolStore } from '@/store/modules/pool'
import { connectWS } from '@/utils/ws'

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
// spinA 左右塞、spinB 高低杆，都是 -1 到 1。物理口径见 predict.ts 的那组常量：
// a 只影响撞库后的反弹角，b 在母球第一次撞球时结算成跟进／拉回。
const spinA = ref(0)
const spinB = ref(0)

// spinOpen 表示加塞面板是不是铺开着（由 SpinPad 的 open-change 通知）。
//
// 展开期间这一页要把瞄准停住：玩家点面板外面是为了关面板，手一路划到台面上
// 会持续发悬停事件；不拦的话「关面板」就等于「重新瞄一次」。
const spinOpen = ref(false)
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
const peerAimState = ref<{
  seat: Seat
  phi: number
  power: number
  a: number
  b: number
  at: number
} | null>(null)

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

// resultSeries 是结算帧里带过来的大比分（含刚打完这一局）。
//
// 正常情况下快照里的 series 已经是新的，但判负（对手离场、超时）那两条路径
// 服务端不再补快照，只靠快照会让顶上的比分停在开打前。
const resultSeries = ref<[number, number] | null>(null)

// seriesShown 是顶栏此刻要显示的两个数，按座位排。
//
// 结算帧优先于快照：它一定是「包含本局」的那一份，而快照可能是旧的。
const seriesShown = computed<[number, number]>(() => {
  if (resultSeries.value) {
    return resultSeries.value
  }
  const snap = snapshot.value
  if (!snap) {
    return [0, 0]
  }
  return [snap.series[0], snap.series[1]]
})

// myScore / peerScore 把按座位排的比分换成「我」与「对手」。
//
// 座位还没确定时（第一份快照没回来）两边都显示 0，不要拿 0 号位硬当自己。
const myScore = computed(() => {
  const seat = mySeat.value
  if (seat === null) {
    return 0
  }
  return seriesShown.value[seat]
})

const peerScore = computed(() => {
  const seat = mySeat.value
  if (seat === null) {
    return 0
  }
  return seriesShown.value[seat === 0 ? 1 : 0]
})

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
// pendingShot 是「上一杆还在播时又到了一杆」的轨迹。
//
// 正常节奏下它一直是空的：服务端会等我们这一杆播完再让机器人出杆。
// 但客户端掉帧、后台标签页被降频、或者服务端还是旧版（不等动画）时，
// 下一杆的 shot 帧会赶在上一杆 onDone 之前到。直接换动画会让球从半路
// 跳到新起点、两段动画同时写 balls，之后的 state 也会错位——
// 所以这里排一杆，等当前这段播完再起。单槽够用：真排上两杆说明已经严重落后，
// 保留最新的一份至少能对齐到服务端的当前局面。
let pendingShot: ShotData | null = null

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

// behindHead 表示这次手中球只允许摆在开球线之后（开球那一杆）。
//
// 规则在服务端：越线的落位会被拒。这里拿它夹虚影、画开球线，
// 让玩家在摆之前就看得见边界，而不是点下去才被拒。
const behindHead = computed(() => snapshot.value?.place_zone === 'behind_head')

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

// showHeadString 决定台面上那条开球线要不要画：只有开球摆球时才有意义。
const showHeadString = computed(() => placingMode.value && behindHead.value)

// holdCue 是「玩家主动把已经在台面上的母球重新拿回手里」。
//
// 它必须与 cueOnTable 分开记：犯规但没摔袋时母球一直在台上，玩家点了
// 「自由球 · 重摆」，此时该像开局摆球那样——台面上不留那颗实球，
// 改由跟着指针的虚线虚影代表，点一下才落定。只看 cueOnTable 的话，
// 重摆只是切了个模式，母球仍钉在原处，要再点一次才动，手感完全不同。
const holdCue = ref(false)

// cueInHand 表示母球在手上：台面上不画它，由虚影代表。
// 两种来源——自然摔袋（库里就是不在台上），或者玩家主动重摆。
const cueInHand = computed(
  () => placingMode.value && (holdCue.value || !cueOnTable.value),
)

// ghost 是手中球还没落位时跟着指针走的虚影位置（世界坐标）。
//
// 虚影就是那颗球本身，不是「预览」：指针指到哪它跟到哪，点一下台面就落定。
// 球一落到台上就收起来——两颗白球同时在台上，分不清哪颗能拖。
const ghost = ref<Point | null>(null)

// nearPocket 判断一个位置是不是贴着袋口。
//
// 虚影不能从袋口里冒出来：那里放不下球，也看不出这颗球能不能挪。
// 半径按「洞口 + 半个球」算，正好把「大半个球压在洞沿上」的位置也算进去。
function nearPocket(x: number, y: number): boolean {
  for (const pocket of POCKETS) {
    const limit = pocket.radius + BALL_RADIUS * 0.5
    const dx = x - pocket.x
    const dy = y - pocket.y
    if (dx * dx + dy * dy < limit * limit) {
      return true
    }
  }
  return false
}

// ghostSeed 给手中的母球一个虚影起点。
//
// 母球刚摔袋时，它最后一次出现的位置就在袋口里。直接拿那个位置当起点的话，
// 鼠标不动虚影就不动，看到的就是「母球还在袋口，没有消失」。
// 所以贴着袋口的位置一律换成开球线后中央，那里一定放得下球。
function ghostSeed(): Point {
  const cue = balls.value.find((ball) => ball.id === 'cue')
  if (cue && cue.on_table && !nearPocket(cue.x, cue.y)) {
    return { x: cue.x, y: cue.y }
  }
  return { x: TABLE_WIDTH / 2, y: CUE_HEAD_STRING - BALL_RADIUS * 2 }
}

// syncGhost 按当前模式决定虚影要不要出现、出现在哪。
//
// 母球还在台上时没有虚影；已经落袋了就摆到开球线后一点，
// 免得虚影一出来就在袋口里（那里既放不下球，也看不清）。
function syncGhost() {
  if (!cueInHand.value) {
    ghost.value = null
    return
  }
  if (ghost.value) {
    return
  }
  ghost.value = ghostSeed()
}

// ballInHandTip 控制「自由球」那块大牌子的显示。
//
// 只在拿到手中球的那一刻弹一次、几秒后收起：牌子会盖住半个球台，
// 一直挂着就没法看球位了。底栏那一条会一直留着，说明不会丢。
const ballInHandTip = ref(false)
let ballInHandTimer: ReturnType<typeof setTimeout> | null = null

// ballInHandText 是牌子上那句话：说的是「下一步做什么」。
// 母球还在台上时不能让他去点台面——那时点台面是锁方向。
const ballInHandText = computed(() => {
  if (!placingMode.value) {
    return '白球可以随时重摆'
  }
  if (behindHead.value) {
    return '白球可以摆在开球线后的任意位置'
  }
  return '点一下台面，把白球放在那里'
})

// placingTip 是底栏那行小字。开球与犯规判罚的规则不一样，说法也要分开：
// 开球时「线后」是硬限制，玩家一旦被夹住，得知道是被什么夹的。
const placingTip = computed(() =>
  behindHead.value ? '那条虚线就是开球线，白球只能摆在它后面' : '点台面挪白球，可以反复挪',
)

// 拿到手中球的那一刻：进摆球模式，并弹一次大牌子——
// 这是「为什么现在打不出杆」的唯一解释。
//
// 两种情况都直接把母球收进手里。摔袋时它本来就不在台上；犯规但没摔袋时
// 也要从台面上取下来——只在原地留一颗实球的话，指针怎么移动都只是改瞄准方向，
// 玩家会以为白球被钉住了，还得先去点一次「重摆」才能挪，等于同一件事做两遍。
watch(ballInHand, (has) => {
  if (ballInHandTimer) {
    clearTimeout(ballInHandTimer)
    ballInHandTimer = null
  }
  if (!has) {
    placingMode.value = false
    holdCue.value = false
    ballInHandTip.value = false
    return
  }
  // 拿到自由球：两声上行提示。玩家这时多半在看球台，
  // 大牌子是视觉上的解释，声音是让他抬头的那个引子。
  sfx.freeBall()
  const cue = balls.value.find((ball) => ball.id === 'cue')
  if (cue && cue.on_table) {
    holdCue.value = true
    ghost.value = ghostSeed()
  }
  placingMode.value = true
  ballInHandTip.value = true
  ballInHandTimer = setTimeout(() => {
    ballInHandTip.value = false
    ballInHandTimer = null
  }, 3200)
})

// 母球刚落到台上的那一刻回瞄准模式：摆下去之后玩家要的是瞄准线，
// 而不是继续跟着指针跑的虚影。想再挪由底栏的「自由球」按钮负责。
watch(cueOnTable, (onTable) => {
  if (onTable && placingMode.value && !holdCue.value) {
    placingMode.value = false
  }
})

watch([placingMode, cueOnTable], syncGhost)

// finishPlacing 收起摆球模式去瞄准。母球不在台上时按钮是禁用的，这里不再判。
// 收手时把「拿着球」也一并放掉，否则下次进摆球模式会直接是持球态。
function finishPlacing() {
  placingMode.value = false
  holdCue.value = false
}

// reopenPlacing 回到摆球模式，把母球再挪一挪。
//
// 进去的一瞬间就把母球收进手里：虚影从它当前的位置起，指针一动就跟着走。
// 不从台面中央凭空冒出来——那样看起来像球被谁挪走了。
// 顺手解锁方向：摆完球的方向和刚才瞄的不是同一条线。
function reopenPlacing() {
  const cue = balls.value.find((ball) => ball.id === 'cue')
  if (cue && cue.on_table) {
    holdCue.value = true
    ghost.value = ghostSeed()
  }
  placingMode.value = true
  aimLocked.value = false
}

// ===== 出杆：本地算、本地播、服务端采用 =====
//
// 松手那一刻就在本地把整杆算完（predict.ts），画面立刻开始动；算完的结果
// 随出杆一起发给服务端，服务端不再跑物理，只做校验与规则判定，再把同一份帧
// 转给对手与观战者（见 internal/pool/match.go 的 handleShoot）。
//
// 全链路只有一份结果，所以没有「预测落点」与「权威落点」的差别，
// 也就不需要补间把球滑过去。早先那套补间在开球时会明显看到球堆整体挪位：
// 两边各算一次，落点必然不同，最后只能靠 260 毫秒的滑动糊过去。

// localPlaying 表示屏幕上正在放本地算的这一杆。
const localPlaying = ref(false)
// localHandle 是本地轨迹的播放句柄。它与 handle 分开持有：handle 放的是
// 服务端转回来的帧，两者不会同时存在（自己的那一杆只播本地这份）。
let localHandle: PlayHandle | null = null

// shotOrigin 是出杆前那一刻的球位。
//
// 本地已经把整盘球滚出去了，而服务端有可能根本不认这一杆（不是你回合、
// 断线时请求在路上丢了）。那时屏幕上会停着一个服务端从未承认过的局面，
// 只能靠这份快照还原——只把母球挪回原位是不够的，被撞开的球也得回去。
let shotOrigin: BallState[] | null = null

// captureShotOrigin 在出杆瞬间记下球位。
function captureShotOrigin() {
  const copy: BallState[] = []
  for (const ball of balls.value) {
    copy.push({ id: ball.id, x: ball.x, y: ball.y, z: ball.z, on_table: ball.on_table })
  }
  shotOrigin = copy
}

// restoreShotOrigin 把画面还原到出杆前。没有快照时什么都不做。
function restoreShotOrigin() {
  const origin = shotOrigin
  shotOrigin = null
  if (!origin) {
    return
  }
  const byId = new Map<string, BallState>()
  for (const saved of origin) {
    byId.set(saved.id, saved)
  }
  for (const ball of balls.value) {
    const saved = byId.get(ball.id)
    if (!saved) {
      continue
    }
    ball.x = saved.x
    ball.y = saved.y
    ball.z = saved.z
    ball.on_table = saved.on_table
  }
  refreshPotted()
}

// 音效队列：开播一杆之前按事件流算好「第几秒响什么」，播放时按时间往外放。
//
// 不在每帧里扫事件表：一杆几百帧、几十个事件，逐帧比对是纯浪费，
// 而且帧间隔一抖就会同一颗球响两次。游标只往前走，掉帧也只是把攒下的
// 几条一口气放出来（听起来正好像球堆连撞）。
let sfxQueue: SfxCue[] = []
let sfxIndex = 0

// armSfx 换上一杆的音效表。本地预测与服务端帧两种来源都要走它。
function armSfx(frames: ShotFrame[], events: ShotEvent[], power: number) {
  sfxQueue = buildShotSfx(frames, events, power)
  sfxIndex = 0
}

// playSfxUpTo 放出时间轴走到 t 之前的所有音效。
function playSfxUpTo(t: number) {
  while (sfxIndex < sfxQueue.length && sfxQueue[sfxIndex].t <= t) {
    sfx.play(sfxQueue[sfxIndex])
    sfxIndex += 1
  }
}

// soundOn 是音效开关的界面态，与 sfx 内部那份设置保持同步。
const soundOn = ref(sfx.enabled)

// toggleSound 开关音效。打开时补一声轻响：不然玩家按下去没有任何反馈，
// 会以为按钮坏了（对局页这时候本来就安静）。
function toggleSound() {
  soundOn.value = !soundOn.value
  sfx.setEnabled(soundOn.value)
  if (soundOn.value) {
    sfx.placeCue()
  }
}

// ===== 对局内聊天 =====
//
// 只实时转发、不留历史：服务端不存，重连之后列表就是空的。
// 消息放在页面内存里而不是 store——它属于「这一局」，换局就该清掉。

// ChatLine 是列表里的一行。seat 是发送者，id 用来当 key。
interface ChatLine {
  id: number
  seat: Seat
  text: string
  at: number
}

// chatOpen 是面板是否展开。收起时收到消息只在气泡上亮一个未读点，
// 不飘提示条：玩家正在瞄准，球台上方飞过一条横幅只会挡住要看的地方。
const chatOpen = ref(false)
const chatText = ref('')
const chatUnread = ref(0)
const chatBox = ref<HTMLElement | null>(null)

// chatLog 是这一局的聊天记录。id 用服务端发的号；自己发的那条本地先落，
// 号取负数（服务端不回显给发送方，两边不会撞号）。
const chatLog = ref<ChatLine[]>([])
let localChatId = -1

// 面板里那一排表情。不做图片表情包：那要一整套素材与审核，
// emoji 是系统字库里的，发出去在对面也一定是同一个字。
const CHAT_EMOJIS = ['😂', '👍', '🎯', '🔥', '😅', '😭', '🤝', '👏', '😱', '🙏', '💪', '😎']

// 表情的码点范围。变体选择符（\uFE0F）与零宽连接（\u200D）不进这个字符类：
// 它们在 eslint 眼里是「组合字符」，塞进字符类会被判成可能误读的写法。
// 这两个符号在 stripEmoji 里单独剥一次——它们只跟在别的表情后面，自己不成字。
const EMOJI_RANGES = /[\u{1F000}-\u{1FAFF}\u{2600}-\u{27BF}\u{2B00}-\u{2BFF}]/gu

// 悬在对手头像下面的短提示：最近几句话在这里各露几秒，然后自己走开。
//
// 和面板里的列表是两套东西：列表留全量、要打开才看得到，
// 这里只放刚发生的两三条，不占屏、不用点。
interface ChatToast {
  id: number
  text: string
  // mine 决定配色：自己发出去的那句用台呢绿，对面发来的用深色。
  mine: boolean
}

const chatToasts = ref<ChatToast[]>([])

// 两侧各取自己那一组：自己的挂左上（我头像下），对手的挂右上（他头像下）。
// 用 filter 而不是两个数组：过期撤条只认 id，分两处存会让 dropToast 要试两次。
const myToasts = computed(() => chatToasts.value.filter((item) => item.mine))
const peerToasts = computed(() => chatToasts.value.filter((item) => !item.mine))

// 停留时长与数量上限。4 秒够读完一句短话，再多就会在头像下面排队；
// 连发三条以上时丢最早的，屏幕上始终是最新的三条。
const CHAT_TOAST_MS = 4000
const CHAT_TOAST_MAX = 3

// toastSeq 只用来给 key 编号：提示条的 id 与消息 id 无关（同一条能被显示一次）。
let toastSeq = 0

// toastTimers 存着每条提示的定时器，卸载时要一并清掉——
// 页面已经销毁，回调再去改一个不存在的数组只会报错。
const toastTimers = new Map<number, ReturnType<typeof setTimeout>>()

// canChat 判定能不能发：观战与断线都不行。
//
// 观战者收得到消息（他们看的是同一块屏），但没有座位，服务端也会挡掉。
const canChat = computed(() => connState.value === 'open' && mySeat.value !== null)

function toggleChat() {
  chatOpen.value = !chatOpen.value
  if (chatOpen.value) {
    chatUnread.value = 0
    scrollChatToEnd()
  }
}

// pushChat 追加一条，超过 60 条就丢掉最早的。
//
// 一局打半小时能聊出几百条，全留着既占内存，滚回顶部也没有意义。
function pushChat(line: ChatLine) {
  chatLog.value.push(line)
  if (chatLog.value.length > 60) {
    chatLog.value.splice(0, chatLog.value.length - 60)
  }
}

// scrollChatToEnd 滚到最新一条。要等一帧：
// 刚 push 的那条还没进 DOM，这时读 scrollHeight 拿到的是旧高度。
async function scrollChatToEnd() {
  await nextTick()
  const box = chatBox.value
  if (box) {
    box.scrollTop = box.scrollHeight
  }
}

// sendChat 把输入框里的内容发出去。
//
// 先落本地再上行：服务端只转给别人、不回显给发送方，
// 等回音的话自己那条要等对手收到才出现，看起来像没发出去。
function sendChat() {
  const text = chatText.value.trim()
  if (text === '' || !canChat.value) {
    return
  }
  chatText.value = ''
  emitChat(text)
}

// onChatEnter 是输入框的回车键：回车直接发，Shift+回车留给换行。
//
// 不写成模板修饰符 .enter.exact：那样挡不住输入法。中文候选词还没上屏时
// 按的回车是「选这个字」，被当成发送的话，打「好球」就会飞出半个词。
// 所以这里先看 isComposing 与 shiftKey，再看要不要拦默认行为——
// 不拦的话浏览器会先插一个换行，字发出去了框里还留个空行。
function onChatEnter(e: KeyboardEvent) {
  if (e.isComposing || e.shiftKey) {
    return
  }
  e.preventDefault()
  sendChat()
}

// sendEmoji 点表情直接发，不经过输入框：发表情图的就是快。
function sendEmoji(emoji: string) {
  if (!canChat.value) {
    return
  }
  emitChat(emoji)
}

// emitChat 是发送的公共尾巴：落本地、飘提示、上行、音效、滚到底。
function emitChat(text: string) {
  const seat = mySeat.value
  if (seat === null) {
    return
  }
  pushChat({ id: localChatId, seat, text, at: Date.now() })
  localChatId -= 1
  pushToast(text, true)
  socket?.sendChat(text)
  sfx.chatOut()
  scrollChatToEnd()
}

// onChat 处理对面发来的一条消息。
//
// 面板收起时仍要让人看到内容：头像下面飘一条，同时亮未读点。
// 不弹跨屏横幅——玩家在瞄准，一条横幅飞过球台正好挡住他要看的地方。
function onChat(data: ChatData) {
  pushChat({ id: data.id, seat: data.seat, text: data.text, at: data.ts })
  sfx.chatIn()
  pushToast(data.text, false)
  if (chatOpen.value) {
    scrollChatToEnd()
    return
  }
  chatUnread.value += 1
}

// pushToast 把一句聊天挂到说话人那一侧的头像下面，到点自己撤掉。
//
// 只留最近三条：连发表情时头像下面不会堆成一列，
// 屏幕上永远是最后说的那几句。上限按侧各算一条队列——
// 对方连发三条时，我刚发出去那句不该被顶掉（那是两个人在各自说话）。
function pushToast(text: string, mine: boolean) {
  const id = ++toastSeq
  chatToasts.value.push({ id, text, mine })
  const sameSide = chatToasts.value.filter((item) => item.mine === mine)
  if (sameSide.length > CHAT_TOAST_MAX) {
    dropToast(sameSide[0].id)
  }
  toastTimers.set(
    id,
    setTimeout(() => dropToast(id), CHAT_TOAST_MS),
  )
}

// dropToast 撤掉一条（到点或被挤掉），顺手清掉它的定时器。
//
// 被挤掉的那条如果不清定时器，4 秒后它还会来敲一次门——
// 那时候数组里早就没有它了，白跑一趟。
function dropToast(id: number) {
  const timer = toastTimers.get(id)
  if (timer !== undefined) {
    clearTimeout(timer)
    toastTimers.delete(id)
  }
  const index = chatToasts.value.findIndex((item) => item.id === id)
  if (index >= 0) {
    chatToasts.value.splice(index, 1)
  }
}

// chatTime 把时间戳换成 HH:MM。列表里只有几十条，不需要日期。
function chatTime(at: number): string {
  const d = new Date(at)
  return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0')
}

// stripEmoji 把表情从一句话里剥掉，只留文字。
//
// 现在只有「是不是纯表情」用它：一条只有 🎯 的消息，
// 剥完什么都不剩，就按贴纸放大画，而不是当成一句话。
function stripEmoji(text: string): string {
  return text
    .replace(EMOJI_RANGES, '')
    .replace(/\uFE0F/g, '')
    .replace(/\u200D/g, '')
}

// isSticker 判断这条消息是不是纯表情，是的话气泡放大成贴纸。
// 判据是「剥掉表情与空白之后什么都不剩」，而不是看长度——
// 「好🎯」仍然是字，不该被当成贴纸。
function isSticker(text: string): boolean {
  return text.trim() !== '' && stripEmoji(text).trim() === ''
}

// startLocalShot 把本地算好的这一杆立刻播起来。
//
// 播完只收掉自己的句柄，不决定「能不能打下一杆」：那是 shotPending 的事，
// 要等服务端的 shot 帧到了才解开。
function startLocalShot(shot: PredictedShot, shotPower: number) {
  if (localHandle) {
    localHandle.stop()
    localHandle = null
  }
  localPlaying.value = true
  shooting.value = true
  armSfx(shot.frames, shot.events, shotPower)
  localHandle = playShot(shot, {
    speed: SHOT_PLAY_RATE,
    onSample: (sample, elapsed) => {
      applySample(balls.value, sample)
      // 这一杆里落袋的球要立刻进桌下那一排：
      // 等动画结束才更新的话，进球那一下完全没有反馈。
      refreshPotted()
      playSfxUpTo(elapsed)
    },
    onDone: () => {
      localHandle = null
      localPlaying.value = false
      shooting.value = false
      // 服务端的 shot 帧可能还在路上。pending 那几条要等它到了才排队，
      // 所以这里的 flush 多数时候是空转；真的赶上了就顺手放出去。
      flushAfterShot()
    },
  })
}

// stopLocalShot 停掉本地播放并收掉动画状态，返回刚才是否真的在播。
//
// 被拒的一杆、重连拿到的快照都要走它：本地已经把球滚出去了，
// 不停下来画面会继续往前走，停在服务端从未承认过的局面上。
function stopLocalShot(): boolean {
  if (localHandle) {
    localHandle.stop()
    localHandle = null
  }
  if (!localPlaying.value) {
    return false
  }
  localPlaying.value = false
  shooting.value = false
  return true
}

// tableBalls 是画到球台上的球。
//
// 母球还在手上时把它去掉：那颗球现在在玩家手上，台面上由虚影代表。
// 不去掉的话会在台上看到两颗白球，一颗虚的跟着指针、一颗实的停在原处，
// 分不清哪颗才是真的要打的那颗。
const tableBalls = computed(() => {
  if (!cueInHand.value) {
    return balls.value
  }
  const rest: BallState[] = []
  for (const ball of balls.value) {
    if (ball.id === 'cue') {
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

// turnTotalMs 是一杆读秒的总时长，取服务端给过的最长 deadline。
//
// 不写死 40 秒：DefaultTurnTimeout 在服务端是可配的，写死之后换一个配置
// 整条燃烧条就全错了。每一杆开局都是满额，所以「见过的最大值」就是总时长，
// 它会自己跟着服务端走。
const turnTotalMs = ref(40_000)

// deadlineFrom 把服务端给的「还剩多少毫秒」换成绝对截止时刻。
//
// 顺手记一次总时长：换人的那一帧永远是满额，所以最大值就是总时长。
// 用绝对时刻而不是自己倒数，是因为 1Hz 的 tick 有抖动，
// 累减会让读秒越走越慢。
function deadlineFrom(ms: number): number {
  if (ms <= 0) {
    return 0
  }
  if (ms > turnTotalMs.value) {
    turnTotalMs.value = ms
  }
  return Date.now() + ms
}

// fuseVisible 表示该画那条燃烧条。
//
// 三种情况不画：还没拿到快照、还没开局（waiting）、已经结算。
// 这三种状态下服务端不发读秒，画一条满格会让人以为时间刚开始。
const fuseVisible = computed(
  () =>
    snapshot.value !== null &&
    snapshot.value.status === 'playing' &&
    result.value === null &&
    deadlineAt.value > 0,
)

// fusePct 是这一杆还剩多少比例的时间，0 到 100。
const fusePct = computed(() => {
  const left = deadlineAt.value - nowTick.value
  if (left <= 0) {
    return 0
  }
  const pct = (left / turnTotalMs.value) * 100
  return pct > 100 ? 100 : pct
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
  // 先碰到的是对手的球（或还没轮到的 8 号）就打一个禁止符号：
  // 规则判定在服务端，这里只是让玩家在出杆前就知道这一杆会犯规。
  // 座位还没认出来时不判：那时连「我的球是哪一组」都不知道，判了也不准。
  const snap = snapshot.value
  const seat = mySeat.value
  const forbidden = snap !== null && seat !== null ? aimForbidden(snap, seat, hit.targetId) : false
  return {
    cue: { x: cue.x, y: cue.y },
    hit: { x: hit.x, y: hit.y, kind: hit.kind },
    // 球杆的朝向与后拉量都在渲染层算：这一层只把「往哪打、多大力、锁没锁」交出去。
    phi: phi.value,
    power: power.value,
    locked: aimLocked.value,
    forbidden,
    guides: forbidden
      ? { object: null, cue: null }
      : aimGuides(balls.value, phi.value, hit, {
          a: spinA.value,
          b: spinB.value,
          speed: speedOfPower(power.value),
        }),
  }
})

// peerAimLive 是「对面那一帧，且还新鲜、且不是自己发回来的」。
//
// 显示层好几个地方都要这一份（球台上的杆、右侧的力度条、加塞球），
// 判据写在三处就会有一处漏掉超时，球台上没杆了、右边的力度条还挂着。
const peerAimLive = computed(() => {
  // 台面上还有球在滚的时候，一律不画对家的杆。
  //
  // 两边的动画不是同时开始的：出杆那一侧从松手那一刻就开播，另一侧要等
  // 服务端把 shot 帧转过来。先播完的那一方如果继续打（进了球就还是他），
  // 他新一杆的瞄准帧会在我这边还滚着球的时候到达——照画就是
  // 「球没停，杆已经架上了」。帧照收（早就存进 peerAimState 了），只是先不画：
  // 动画一结束，最新那一帧立刻就能显示出来，不会漏掉他停手不动的姿势。
  if (shooting.value) {
    return null
  }
  const state = peerAimState.value
  if (!state || state.seat === mySeat.value) {
    return null
  }
  if (Date.now() - state.at > AIM_STALE_MS) {
    return null
  }
  return state
})

// powerShown 是右侧力度条此刻该显示的值。
//
// 轮到自己就绑自己的 power，轮到对手就绑他那一帧里的 power：
// 位置固定在同一根滑轨上，对面推到哪里、我这边就涨到哪里。
const powerShown = computed(() => {
  if (isMyTurn.value) {
    return power.value
  }
  const state = peerAimLive.value
  if (!state) {
    return 0
  }
  return state.power
})

// spinShownA / spinShownB 同理，给加塞球那颗红点用。
//
// 没有对手帧时回中杆，而不是沿用自己上一杆的塞：换人的瞬间自己那份塞
// 已经随出杆清零，再把旧的画上去会看成「对方也在打这个塞」。
const spinShownA = computed(() => {
  if (isMyTurn.value) {
    return spinA.value
  }
  const state = peerAimLive.value
  if (!state) {
    return 0
  }
  return state.a
})

const spinShownB = computed(() => {
  if (isMyTurn.value) {
    return spinB.value
  }
  const state = peerAimLive.value
  if (!state) {
    return 0
  }
  return state.b
})

// onPowerInput / onSpinInputA / onSpinInputB 是右侧控件写回的入口。
//
// 对手回合这一条是只读镜像，手指按上去不该把对面的值写成自己的：
// 写进来之后下一杆出杆就会用上这个力度，玩家完全不知道它从哪来的。
function onPowerInput(next: number) {
  if (!canShoot.value) {
    return
  }
  power.value = next
}

function onSpinInputA(next: number) {
  if (!canAim.value) {
    return
  }
  spinA.value = next
}

function onSpinInputB(next: number) {
  if (!canAim.value) {
    return
  }
  spinB.value = next
}

// peerAim 是对面那一方的瞄准层，画在自己这一层下面、淡一档。
//
// 用本地同一套 aimFromCue 现算：服务端只转发角度与力度，杆子落在哪、
// 会撞到哪颗球，都是几何问题，本端算比多传一份数据更省也更不会不同步。
const peerAim = computed<AimLayer | null>(() => {
  const state = peerAimLive.value
  if (!state || !snapshot.value || snapshot.value.status !== 'playing') {
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
    guides: aimGuides(balls.value, state.phi, hit, {
      a: state.a,
      b: state.b,
      speed: speedOfPower(state.power),
    }),
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
  peerAimState.value = {
    seat: data.seat,
    phi: data.phi,
    power: data.power,
    a: data.a,
    b: data.b,
    at: Date.now(),
  }
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
  socket.sendAim(phi.value, power.value, spinA.value, spinB.value)
}

watch([phi, power, spinA, spinB], () => {
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
  // 还在打的新快照说明这是新的一局（再约之后），结算帧里那份比分已经过期，
  // 不清掉的话顶栏会一直停在上一局结算时的数字。
  if (snap.status === 'playing') {
    resultSeries.value = null
  }
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
  //
  // 主动重摆是另一条收尾路径：母球一直在台上，cueOnTable 不会翻转，
  // 上面那个 watch 收不掉摆球模式，只能在这里放掉手里的球并回瞄准。
  // 摔袋落位不受影响（那时 holdCue 为 false）。
  const placedByHand = placing.value && holdCue.value
  // 这一份快照就是对上一次落位请求的答复：放一声轻响，说明「摆上了」。
  // 摆球是唯一一个「点了以后没有任何位移反馈」的操作，全靠这一声确认。
  if (placing.value) {
    sfx.placeCue()
  }
  placing.value = false
  if (placedByHand) {
    holdCue.value = false
    placingMode.value = false
  }
  // 出杆在途标记同理：快照是权威状态，它能到就说明那一杆已经有结论了
  // （服务端要么算完广播了 shot，要么把请求拒了）。
  // 断线重连时正是靠这一句把标记解开——否则 canShoot 永远是 false，只能刷新页面。
  shotPending.value = false
  clearShotTimeout()
  // 本地那一杆也要收掉：快照里的球位才是真的，让本地轨迹继续写坐标会画错。
  stopLocalShot()
  shotOrigin = null
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
      deadlineAt: deadlineFrom(msg.data.deadline_ms),
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
      deadlineAt: deadlineFrom(msg.data.deadline_ms),
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
  // 聊天永远立刻处理，不像 turn / result 那样等动画播完：
  // 一句话等五秒才出现，对面会以为你没理他。
  if (msg.type === 'chat') {
    onChat(msg.data)
    return
  }
  if (msg.type === 'error') {
    onServerError(msg.data)
  }
}

// playShotFrame 播放一杆的轨迹。
//
// 服务端把出杆方算好的那份结果原样转给了所有人（见 handleShoot），
// 所以这里有两种完全不同的处境：
//   - 自己那一杆：屏幕上放的已经是同一份帧，不能再播一遍；
//   - 对手那一杆：本地没算过，老老实实播服务端给的帧。
function playShotFrame(data: ShotData) {
  // 收到任何一杆的轨迹，都说明服务端已经受理了出杆，兜底计时可以撤掉。
  // 这一句必须留在最前面：自己那一杆的回音会被下面直接丢掉，
  // 长杆（二十来秒）的兜底计时当时还没到点，不在这里撤就会误报「没有回应」。
  clearShotTimeout()
  // 自己那一杆的回音：本地正在播同一份帧，不能再播一遍，
  // 也不能排队——排进去就等于把这一杆放两次。
  // 但「这一杆是谁打的」要记下来：后面那条 turn 帧靠它判断连击还是换人。
  if (data.seat === mySeat.value && localPlaying.value) {
    lastShooter = data.seat
    return
  }
  // 别的杆还在播（对手的轨迹，或自己那段本地轨迹还没播完）：先排一杆。
  // 这一段必须放在下面所有状态改动之前：那些写的是「这一杆开始播」的副作用，
  // 现在播的还不是它。
  if (shooting.value) {
    pendingShot = data
    return
  }

  // 服务端已经收下这一杆，在途标记可以放开。
  shotPending.value = false
  placing.value = false
  // 这一杆已经成型，出杆前的球位不再需要（只有被拒时才回滚）。
  shotOrigin = null
  // 记下这一杆是谁打的：紧跟着的 turn 帧要靠它判断「连击还是换人」。
  lastShooter = data.seat
  // 球已经打出去了，对面那根杆要立刻收起来，不能跟着轨迹一起挂在台上。
  peerAimState.value = null

  if (data.seat === mySeat.value) {
    // 走到这里说明本地轨迹已经播完（还在播的那一种上面已经返回了），
    // 这一杆只是服务端的回音：把终局对齐一次就够。
    applyFinal(data)
    shooting.value = false
    flushAfterShot()
    return
  }

  if (handle) {
    handle.stop()
    handle = null
  }
  shooting.value = true
  armSfx(data.frames, data.events, data.params.power)
  handle = playShot(
    { frames: data.frames, duration: data.duration },
    {
      speed: SHOT_PLAY_RATE,
      onSample: (sample, elapsed) => {
        applySample(balls.value, sample)
        // 每帧对一次：这一杆里刚掉袋的球要立刻进桌下那一排，
        // 等动画结束才更新的话，进球那一下完全没有反馈。
        refreshPotted()
        playSfxUpTo(elapsed)
      },
      onDone: () => {
        handle = null
        // 动画是插值播的，最后一帧不一定正好落在静止位置
        // （物理服务砍掉了慢爬的尾巴）。直接 applyFinal 就是那一下瞬移，
        // 先把差距平滑走完，再按权威球位收尾。
        settleBalls(data.final, () => {
          applyFinal(data)
          shooting.value = false
          flushAfterShot()
        })
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
    // 快照里的球位是权威的，但它和刚播完的画面常常差几厘米：本地预测是近似的，
    // 服务端那边还砍掉了「球以毫米级速度爬行」的尾巴。直接把 balls 换成新数组，
    // 就是玩家看到的「球慢慢停下之后又瞬移一小段」。先平滑搬过去再换。
    // 搬完再递归一次，把排在这一条后面的犯规 / 换人 / 下一杆依次放出去。
    settleBalls(next.snap.balls, () => {
      applySnapshot(next.snap, next.deadlineAt)
      flushAfterShot()
    })
    return
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
  // 排队的那一杆放到最后播：上面的状态与换人已经落地，
  // 它才是从正确球位（也就是服务端算它时的球位）出发的。
  // 递归调用一次，下次动画结束时又会走到这里。
  if (pendingShot) {
    const next = pendingShot
    pendingShot = null
    playShotFrame(next)
  }
}

// SETTLE_MIN_MS / SETTLE_MAX_MS 是「帧尾到权威球位」之间补间的时长范围。
//
// 这段差距是真实存在的：物理服务会把「球还在以毫米级速度爬行」的那几秒砍掉
// （pool-sim 的 trim_still_tail），而 final 是彻底停下的位置；自己那一杆则是
// 本地预测与服务端复算的细微差异。直接赋值就是那一下瞬移。
const SETTLE_MIN_MS = 160
const SETTLE_MAX_MS = 520

let settleHandle = 0

// settleBalls 把画面上的球平滑搬到目标球位，到位后执行 done。
//
// 只搬「两边都还在台面上」的球：刚落袋的球该立刻消失，
// 把它也挪过去反而会在袋口外看到一颗球滑进来。
function settleBalls(target: ReadonlyArray<BallState>, done: () => void) {
  const goals = new Map<string, BallState>()
  for (const ball of target) {
    goals.set(ball.id, ball)
  }

  const starts = new Map<string, { x: number; y: number; z: number }>()
  let maxDelta = 0
  for (const ball of balls.value) {
    const goal = goals.get(ball.id)
    if (!goal || !goal.on_table || !ball.on_table) {
      continue
    }
    starts.set(ball.id, { x: ball.x, y: ball.y, z: ball.z })
    const distance = Math.hypot(goal.x - ball.x, goal.y - ball.y)
    if (distance > maxDelta) {
      maxDelta = distance
    }
  }

  // 差不到 1 毫米（手机上不到零点几像素）就别多播一段动画，直接对齐。
  if (maxDelta < 0.001) {
    done()
    return
  }

  // 时长跟着差距走：差几厘米就用最短的一小段，差十几厘米就多走一会儿。
  const duration = Math.min(SETTLE_MAX_MS, Math.max(SETTLE_MIN_MS, 120 + maxDelta * 1400))
  const startedAt = performance.now()
  const step = () => {
    if (disposed) {
      settleHandle = 0
      return
    }
    const k = Math.min(1, (performance.now() - startedAt) / duration)
    // easeOut：起步接着刚才球的速度，末尾自然收到 0，接上静止不顿。
    const w = 1 - (1 - k) * (1 - k)
    for (const ball of balls.value) {
      const start = starts.get(ball.id)
      const goal = goals.get(ball.id)
      if (!start || !goal) {
        continue
      }
      ball.x = start.x + (goal.x - start.x) * w
      ball.y = start.y + (goal.y - start.y) * w
      ball.z = start.z + (goal.z - start.z) * w
    }
    if (k < 1) {
      settleHandle = requestAnimationFrame(step)
      return
    }
    settleHandle = 0
    done()
  }
  settleHandle = requestAnimationFrame(step)
}

// applyFoul 显示犯规文案，并顺手采纳「这一杆之后我是不是拿到手中球」。
//
// 只认「对手犯规」这一种：那时下一杆是我，而 foul 帧里没有轮次，
// 等 turn 帧到了 ballInHand 才算得出来。反过来自己犯规时绝不能采纳——
// 快照里的轮次还是我，采纳之后界面会立刻以为「我可以自由摆球」，
// 而服务端已经把这一杆记成自己的犯规、手中球给了对手，
// 一摆就是 ErrNotBallInHand（「这一杆不能自由摆放母球」）。
//
// 正常情况下紧随其后的 state 会用权威值覆盖这一项；保留它是因为 WS 不保证不丢包，
// state 恰好丢掉时它就是唯一来源，而漏掉「有手中球」会让接手方既摆不了球
// 也出不了杆（彻底卡死）。
function applyFoul(data: FoulData) {
  const snap = snapshot.value
  if (snap && data.ball_in_hand && data.seat !== mySeat.value) {
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
  const won = data.winner === mySeat.value
  result.value = { won, reason: data.reason }
  resultSeries.value = [data.series[0], data.series[1]]
  // 结算音跟着结算帧走，与那块结算卡同一时刻出现：先弹卡再响会慢半拍。
  if (won) {
    sfx.win()
    return
  }
  sfx.lose()
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
  // 这一杆被拒了，画面要回到出杆前：本地已经把整盘球滚出去了，
  // 只把母球挪回原位会被撞开的球堆留在半路上。
  stopLocalShot()
  restoreShotOrigin()
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
  if (!hoverResume(point)) {
    return
  }
  aimTowards(point)
}

// hoverResume 决定「刚关掉加塞面板」之后的悬停要不要跟随。
//
// 关面板那一下被吃掉了（见 SpinPad 的 onDocPointerDown），指针没有到达台面，
// 但它此刻就停在台面某处；下一次 mousemove 会立刻把方向拽过去——观感正是
// 「点一下外面，杆自己跳了一下」。所以先吞掉一次悬停，再要求指针真的走开
// 一个球径以上才恢复跟随：手抖那几像素不算「要重新瞄准」。
let hoverArmed = false
let hoverAnchor: Point | null = null

function hoverResume(point: Point): boolean {
  if (!hoverArmed) {
    return true
  }
  if (!hoverAnchor) {
    hoverAnchor = point
    return false
  }
  if (Math.hypot(point.x - hoverAnchor.x, point.y - hoverAnchor.y) < BALL_RADIUS * 1.5) {
    return false
  }
  hoverArmed = false
  hoverAnchor = null
  return true
}

// onSpinOpenChange 接加塞面板的开合通知。
function onSpinOpenChange(open: boolean) {
  spinOpen.value = open
  if (open) {
    return
  }
  // 刚关掉：先把下一次悬停冻住，理由见 hoverResume。
  hoverArmed = true
  hoverAnchor = null
}

// aimTowards 把瞄准线指向台面上的某一点。
//
// 桌面端靠鼠标悬停、手机端靠手指拖动，两条路都走这里：手指/指针在哪，
// 球就往哪走。以前手机端按水平位移换算角度，斜着划或者在屏幕中间来回擦时
// 方向会和手感对不上——有时反着走，有时干脆不动，因为竖直分量被丢掉了。
function aimTowards(point: Point) {
  // 加塞面板铺开时谁的指针位置都不算数：悬停、拖动两条路都在这里汇合，
  // 拦一处就够。玩家这时盯着的是球面上的红点，指针只是路过台面。
  if (spinOpen.value) {
    return
  }
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

// moveGhost 把手中球的虚影挪到指针处，并夹进台面内沿、避开别的球。
//
// 夹在本地做而不是等服务端拒绝：越界的虚影会画到库边木框外面，
// 看起来像球穿进了桌子。真正落位时服务端仍会校验一次。
function moveGhost(point: Point) {
  ghost.value = cueSpot(point)
}

// onDrag 处理画布拖动：拖动即瞄准，方向指向手指所在的位置。
//
// 触摸屏上没有悬停，这是手机端唯一的瞄准方式；鼠标上它等同于按住跟手。
// 锁定之后一律不理，理由同 onHover。
//
// 抬手（phase 为 end）不再重算角度。手指离开台面那一刻没有新的位置信息，
// pointerup 的坐标只是机械上的最后落点，还常带几像素漂移（手指抬起时会
// 侧向带一下；个别移动端浏览器在 pointercancel 时直接给 0,0）。
// 拿它再 aimTowards 一次，玩家看到的就是「瞄准好了，一松手角度跳一下」。
// 方向就停在最后一次 move 的位置，那才是玩家真正指过的地方。
// 鼠标端不受影响：点击的位移小于 4px，走的是 tap 分支，本来就不会转角度。
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
  if (payload.phase === 'end') {
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
  const spot = cueSpot(point)
  const seq = socket?.placeCue(spot.x, spot.y) ?? 0
  if (seq === 0) {
    showNotice('连接已断开，正在重连')
    return
  }
  placing.value = true
}

// cueSpot 是最终落点：先收进可摆区域，再从别的球身上推开。
//
// 摆球时最难受的一件事是「松手之后白球跟另一颗球叠在一起」——虚影跟着手指走，
// 手指落在一颗球上，那颗球就压住了。这里把落点沿球心连线推到相切的位置，
// 玩家看到的是虚影被顶开，手一松就落在那里，不用再挪一次。
//
// 两轮「推开 + 收进区域」：推开有可能把球顶到开球线外，收回来又可能压上另一颗。
// 两轮之后仍然压着（球贴着开球线排成一排这种极端摆位）就交给服务端拒，
// 那时玩家看到的是「摆不上去」，而不是两颗球叠在一起。
function cueSpot(point: Point): Point {
  let spot = clampIntoCueArea(point)
  for (let round = 0; round < 2; round += 1) {
    const pushed = pushOutOfBalls(spot)
    const clamped = clampIntoCueArea(pushed)
    if (clamped.x === pushed.x && clamped.y === pushed.y) {
      return clamped
    }
    spot = clamped
  }
  return spot
}

// pushOutOfBalls 把落点从每一颗挡路的球身上推到相切的位置。
function pushOutOfBalls(point: Point): Point {
  let x = point.x
  let y = point.y
  const contact = BALL_RADIUS * 2
  for (const ball of balls.value) {
    // 母球自己不算：它马上要挪到这个点上。
    if (ball.id === 'cue' || !ball.on_table) {
      continue
    }
    let dx = x - ball.x
    let dy = y - ball.y
    const distance = Math.hypot(dx, dy)
    if (distance >= contact) {
      continue
    }
    if (distance < 1e-6) {
      // 正好点在球心上：没有方向可以推，往台面中线方向让开一个球径。
      dx = 0
      dy = 1
    } else {
      dx = dx / distance
      dy = dy / distance
    }
    x = ball.x + dx * contact
    y = ball.y + dy * contact
  }
  return { x, y }
}

// clampIntoCueArea 把落点收进允许摆放的区域。
//
// 服务端也会校验并拒绝越界，这里先夹一次是为了不让球被画到库边外面去。
// 中心留一个球半径的余量：球心压在库边上时球身会穿过木框。
// 开球时上界换成开球线——线外那一半是球堆，母球摆过去等于偷跑。
function clampIntoCueArea(point: Point): Point {
  const minX = BALL_RADIUS
  const maxX = TABLE_WIDTH - BALL_RADIUS
  const minY = BALL_RADIUS
  const maxY = behindHead.value ? CUE_HEAD_STRING : TABLE_LENGTH - BALL_RADIUS
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

// shoot 出杆：先在本地把这一杆算完并立刻开播，再把整份结果随出杆发上去。
//
// 顺序不能反。先发后算的话，本地要等服务端把结果转回来才动得起来，
// 又回到了「松手之后空一段、然后球突然开始跑」的老问题。
function shoot() {
  if (!canShoot.value || power.value < 0.02) {
    return
  }
  const params: ShotParams = {
    phi: phi.value,
    power: power.value,
    // 抬杆仍然不开放：二维模型画不出腾空，predictShot 遇到 theta > 0.12 直接返回 null。
    theta: 0,
    a: spinA.value,
    b: spinB.value,
  }
  const shot = predictShot(balls.value, params)
  if (!shot) {
    // 走不到这里：界面只暴露角度与力度，theta 恒为 0，母球也必然在台上
    // （canShoot 已经查过）。真到了说明别处改了杆法参数，而这一杆没有结果
    // 可以发给服务端，硬发只会被拒。
    showNotice('这一杆没能算出来，请重新出杆')
    return
  }
  // 连接已经断了时 shoot 返回 0，那就别把在途标记立起来——
  // 立了之后没有 shot 帧回来解锁，力度条会永久变灰。
  const seq = socket?.shoot(params, shot) ?? 0
  if (seq === 0) {
    showNotice('连接已断开，正在重连')
    return
  }
  shotPending.value = true
  captureShotOrigin()
  startLocalShot(shot, params.power)
  // 兜底：正常一杆来回只有几十毫秒（服务端不再跑物理），12 秒没有任何帧回来
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
    // 本地这一杆也要一起收：请求既然丢了，继续放画面会让玩家以为球真的
    // 打出去了，然后看着球位和被服务端拒掉的结果对不上。
    stopLocalShot()
    restoreShotOrigin()
    showNotice('这一杆没有收到结果，可以重新出杆')
  }, SHOT_TIMEOUT_MS)
  // 出杆之后力度归零、方向解锁、加塞回中：下一杆从「自由瞄准 + 中杆」开始。
  // 塞不回中是最容易出错的一件事——上一杆拉低杆，下一杆照着打出去，
  // 明明瞄得一模一样，球却往回缩，玩家会怀疑是瞄准坏了。
  power.value = 0
  aimLocked.value = false
  spinA.value = 0
  spinB.value = 0
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

// rematchPeer 是「对方已经点了再约」时对方的昵称，空串表示对方还没点。
//
// 按 match_id 过滤：全局推送不区分页面，上一局留下的那条不该在新一局的
// 结算卡上冒出来。
const rematchPeer = computed(() => {
  const want = pool.rematchFrom
  const snap = snapshot.value
  if (!want || !snap || want.match_id !== snap.match_id) {
    return ''
  }
  return want.from.nickname || '对方'
})

// isBotMatch 表示这一局是打人机。结算、再开一局都要按它分两条路：
// 人机那边没有第二个人，既等不到「对方也想再打」，也没有人能替你开局。
const isBotMatch = computed(() => {
  const snap = snapshot.value
  if (!snap) {
    return false
  }
  // typeof 兜一下：老服务端不发这个字段，undefined 会绕过「不是 -1」的判断，
  // 于是每一局都被当成打人机，「再约一局」直接变成开新局。
  return typeof snap.bot === 'number' && snap.bot !== SEAT_SPECTATOR
})

// rematch 再约一局：只发一个「我想再打」的表态，留在结算界面等对方。
//
// 不再回大厅发邀请：双方都已经在结算界面上，回大厅那一步只是把两个人
// 从同一屏拆到两个页面，再让他们各自点一次准备。对方的表态由推送带回，
// 这张卡会自己变成「开局」；两个人都点过时服务端直接把新局开起来。
//
// 人机局走另一条：直接叫服务端开一局新的。原来那条路会拿到
// 「对手不在了，回大厅重新约一个」——对着一台机器说这句话很怪。
async function rematch() {
  const snap = snapshot.value
  if (!snap) {
    return
  }
  if (isBotMatch.value) {
    try {
      const room = await startBotMatch()
      await router.replace('/pool/match/' + String(room.match_id))
    } catch {
      // 开局失败的说明由拦截器弹出，这里不动任何状态。
    }
    return
  }
  if (pool.rematchWaiting) {
    return
  }
  try {
    await pool.requestRematchAgain(snap.match_id)
  } catch {
    // 对方下线、这一局已经不在了、网络抖了一下，都由拦截器弹出说明。
    // 这里不清任何状态：按钮还要能再点一次。
  }
}

// 双方都点了再约：新局已经开起来，直接换到那一局。
//
// 这一页不套全局布局，邀请弹层（它平时负责这类跳转）不在这儿，
// 所以对局页要自己接一次。与弹层里那个 watch 互斥：两者不会同时挂载。
watch(
  () => pool.rematchStarted,
  (started) => {
    if (!started) {
      return
    }
    pool.rematchStarted = null
    void router.replace('/pool/match/' + String(started.match_id))
  },
)

// onKey 让桌面端用方向键微调：0.02°，按住 Shift 是 0.2°（粗调一档）。
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
  // 方向键与底部按钮同一档 0.05°；按住 Shift 是 0.5°，留给「差小半度」的粗调。
  const step = e.shiftKey ? 0.5 : 0.05
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
// onFirstGesture 是页面上的第一次按下/触摸：解锁音频并起环境音。
//
// 只挂在 window 上、只跑一次：手机浏览器要求「声音必须由用户手势发起」，
// 没有这一步，后面所有 sfx 调用都只会在控制台留一句警告。
function onFirstGesture() {
  sfx.unlock()
}

function cleanup() {
  disposed = true
  if (handle) {
    handle.stop()
    handle = null
  }
  // 收尾补间可能正跑到一半：留着它会往已经卸掉的组件里写球位。
  if (settleHandle) {
    cancelAnimationFrame(settleHandle)
    settleHandle = 0
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
  // 头像下面那几条聊天提示的定时器一并清掉：页面已经走了，
  // 4 秒后的回调再去改一个不存在的列表只会报错。
  for (const timer of toastTimers.values()) {
    clearTimeout(timer)
  }
  toastTimers.clear()
  window.removeEventListener('pointerdown', onFirstGesture)
  // 环境音跟着页面走：不主动停的话，退回大厅还在响。
  sfx.suspend()
  // 出杆兜底计时与本地轨迹都要一起收：留着的话组件已经销毁，
  // 超时回调仍会去改 ref、本地轨迹的 rAF 仍会继续往球位上写坐标。
  clearShotTimeout()
  stopLocalShot()
  // 排队等着播的那一杆也丢掉：组件已经没了，onDone 不会再触发，
  // 留着只会让下一次进对局时莫名其妙冒出一段旧轨迹。
  pendingShot = null
  // 「再约」那几个信号跟着这一局一起收：不清的话，下一局刚进结算界面就会
  // 显示上一局留下的「某某想再打一局」，而那件事早就不成立了。
  pool.clearRematch()
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

  // 这一页不套全局布局，布局里那套「建连 + 注册帧处理」不会跑。
  //
  // 少了它，直接打开或刷新对局页就收不到任何全局推送。而「再约一局」的
  // 开局信号只走推送：对面答应了，这一屏会一直停在「等对方回应」上。
  // 两个调用都是幂等的：从大厅过来（连接已经在了）什么都不会发生。
  pool.initWS()
  connectWS(token)

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
  // 200 毫秒一次而不是 1 秒：读秒那行字仍按整秒变，
  // 但燃烧条要跟着时间连续缩短，1Hz 走起来是一跳一跳的。
  tickTimer = setInterval(() => {
    nowTick.value = Date.now()
  }, 200)

  window.addEventListener('keydown', onKey)
  // 浏览器不给没有用户手势的页面出声：把解锁挂在第一次 pointerdown 上。
  // once 就够——context 一旦创建并恢复，之后一直是通的。
  window.addEventListener('pointerdown', onFirstGesture, { once: true })
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
   用 vh 会让底部一截被工具栏压住。

   overflow: hidden 与 user-select: none 是两件必须一起做的事：
   拖力度条时浏览器会把台面上的文字当成可选中内容，一边选一边把页面
   滚起来——鼠标一动，整张球台就跟着上下滑；选中的那一段还会让
   后续的指针拖动变成「扩展选区」，力度条直接不响应。
   这一页没有需要复制的东西，整页禁选是最省事的解法。 */
.pm {
  display: flex;
  flex-direction: column;
  height: 100dvh;
  overflow: hidden;
  user-select: none;
  -webkit-user-select: none;
  /* 全屏（从主屏图标启动）时页面铺到刘海与底部指示条下面，
     这两条安全区要自己让出来，否则顶栏会钻到状态栏里。
     在浏览器里打开时两个值都是 0，不影响现在的样子。 */
  padding-top: env(safe-area-inset-top);
  padding-bottom: env(safe-area-inset-bottom);
  /* 参考视频的底色不是纯黑，是一层带一点紫的深灰，中间偏亮。
     纯黑会把中灰的外框与绿呢一起压死，看着像贴纸。 */
  background: radial-gradient(120% 80% at 50% 40%, #2d2e3b 0%, #20212c 58%, #171720 100%);
}

/* 顶栏三列：左边一张卡、中间读秒、右边另一张卡。
   用 grid 的 1fr auto 1fr 而不是 flex：读秒要钉在屏幕正中，
   两侧内容宽度不一样时 flex 会让它偏，grid 的两条 1fr 把它夹在中间。

   高度必须写死，不能只给 min-height。
   卡内的内容是会变的：没定组时是一行「未定组」，定组之后换成最多 7 颗球点，
   进球后球点少一颗、行数就可能少一行。用 min-height 时顶栏会跟着这些内容
   长高或缩回，下面的 .pm__stage 是 flex: 1，一高一矮就把球台重新等比缩放一遍
   ——玩家看到的就是「打完一杆球桌上下动了一下」。钉死 68px 之后，
   卡内再怎么变都只在这 68px 里挪，球台一个像素都不会动。
   68 是按「头像 42 + 上下内边距 12 + 名字行 21 + 球点行 14」再留一点余量取的。 */
.pm__bar {
  display: grid;
  flex: none;
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
  gap: 8px;
  width: 100%;
  max-width: 820px;
  height: 68px;
  margin: 0 auto;
  padding: 6px 10px;
  color: #e8eaed;
}

/* 左上角：返回键贴着左边，座位卡紧跟着。
   position: relative 是给 .pm__toasts--left 用的（自己发的提示挂在卡片下沿）。 */
.pm__corner {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

/* 右上角：卡片靠到右边缘，聊天提示挂在它下面。
   position: relative 是给 .pm__toasts 定位用的（它要贴着卡片下沿）。 */
.pm__corner--right {
  position: relative;
  justify-content: flex-end;
}

/* 头像下面那几条聊天提示。
   从卡片的右下角开始往下排，右对齐：新的一条接在最下面，
   整块贴着屏幕右边缘，读起来像从对手头像里冒出来的一串气泡。
   往下让 16px：卡片下沿紧接着是出杆倒计时的燃烧条（6px + 2px 上边距），
   紧贴着排会正好盖住那条橙线，提示一出现就像把倒计时掐了。
   不拦点击（pointer-events: none）：那儿是球台上沿，压住了会影响瞄准。 */
.pm__toasts {
  position: absolute;
  top: calc(100% + 16px);
  right: 0;
  z-index: 12;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 6px;
  max-width: 280px;
  /* 整体往中间挪 56px：原来贴着屏幕右边缘，手机上离头像比离中线还远，
     看着像系统通知而不是「刚刚谁说了句话」。 */
  margin-right: 56px;
  pointer-events: none;
}

/* 自己发的那几条：位置与上面整套镜像对称，改成从卡片左下角往右排。
   左边离屏幕边只有 10px 的页边距，56px 的缩进在这里会顶到中间的读秒，
   所以缩进改成 40px，刚好让气泡左沿对齐到座位卡的头像外沿。 */
.pm__toasts--left {
  right: auto;
  left: 0;
  align-items: flex-start;
  margin-right: 0;
  margin-left: 40px;
}

/* 圆角跟着换边：右侧那套尖角朝左下（朝头像下沿），左侧镜像成朝右下。
   自己那侧的绿色气泡原来是「右下尖」，换到左边之后尖角朝左下才对。 */
.pm__toasts--left .pm__toast {
  border-radius: 14px 14px 5px 14px;
}

.pm__toasts--left .pm__toast.is-mine {
  border-radius: 14px 14px 14px 5px;
}

/* 一条提示：深色小气泡，最多两行，超了截断。
   字号 15px 而不是 12px：它只在屏幕上停四秒，读不完就等于没显示。
   4 秒的生命周期写进动画里：前 10% 淡入上浮，最后 12% 淡出，
   到点（CHAT_TOAST_MS）节点正好透明，这时从数组里撤掉看不出跳变。 */
.pm__toast {
  max-width: 280px;
  padding: 8px 14px;
  overflow: hidden;
  font-size: 15px;
  line-height: 1.45;
  color: #eef1f5;
  text-overflow: ellipsis;
  white-space: nowrap;
  background: rgba(12, 18, 24, 0.92);
  border: 1px solid rgba(255, 255, 255, 0.12);
  border-radius: 14px 14px 14px 5px;
  animation: pm-toast 4s both;
}

/* 自己发出去的那句换个配色（台呢绿），一眼分得清谁说的。
   圆角也跟着反过来：尾巴朝上、朝头像那侧。 */
.pm__toast.is-mine {
  color: #eafff5;
  background: rgba(32, 108, 82, 0.94);
  border-color: rgba(120, 220, 180, 0.3);
  border-radius: 14px 14px 5px 14px;
}

/* 纯表情按贴纸画：同样去掉底色放大字号。
   留着深色圆角底的话，一颗 🎯 会躺在一小块方板里，像发错了格式。 */
.pm__toast.is-sticker {
  padding: 0 2px;
  font-size: 34px;
  line-height: 1.1;
  background: transparent;
  border-color: transparent;
}

@keyframes pm-toast {
  0% {
    opacity: 0;
    transform: translateY(-4px);
  }
  10%,
  88% {
    opacity: 1;
    transform: translateY(0);
  }
  100% {
    opacity: 0;
  }
}

/* 出杆倒计时的燃烧条。
   高 6px：再高就会和球台抢注意力，再矮在手机上就看不见了。
   坑在下面铺一层深色，烧掉的部分留下暗痕，看得出「刚刚烧到哪」。
   is-hidden 只藏不撤：这 8px 的高度是布局的一部分，撤掉球台就要缩放。 */
.pm__fuse {
  position: relative;
  flex: none;
  height: 6px;
  margin: 2px 10px 0;
  overflow: hidden;
  background: rgba(255, 255, 255, 0.07);
  border-radius: 999px;
}

.pm__fuse.is-hidden {
  visibility: hidden;
}

/* 余下时间这一段：从暖黄烧到橙红，右端就是正在燃烧的那道边。
   transition 跟 tick 的间隔（200ms）对齐，看起来是连续缩短的。 */
.pm__fuse-fill {
  position: relative;
  height: 100%;
  background: linear-gradient(90deg, #ffd35a, #ff9a3c 62%, #f2612b);
  border-radius: 999px;
  box-shadow: 0 0 8px rgba(255, 150, 60, 0.55);
  transition: width 0.28s linear;
}

/* 燃烧的那一端：一小团亮到发白的火星，快烧完时跳得更急。 */
.pm__fuse-fill::after {
  content: '';
  position: absolute;
  top: -4px;
  right: -6px;
  width: 16px;
  height: 14px;
  background: radial-gradient(closest-side, #fff8dc, rgba(255, 160, 70, 0.8) 55%, rgba(255, 120, 40, 0) 100%);
  border-radius: 50%;
  animation: pm-ember 0.8s ease-in-out infinite alternate;
}

@keyframes pm-ember {
  from {
    opacity: 0.7;
    transform: scale(0.85);
  }
  to {
    opacity: 1;
    transform: scale(1.1);
  }
}

/* 只剩四分之一：换成红热的配色，整条微微跳一下。
   读秒快没了这件事，靠颜色变化比靠数字更容易被余光抓住。 */
.pm__fuse.is-urgent .pm__fuse-fill {
  background: linear-gradient(90deg, #ff9a3c, #f2402b 70%, #d61f1f);
  box-shadow: 0 0 12px rgba(242, 64, 43, 0.75);
  animation: pm-fuse-hot 0.7s ease-in-out infinite alternate;
}

@keyframes pm-fuse-hot {
  from {
    filter: brightness(1);
  }
  to {
    filter: brightness(1.25);
  }
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
/* 大比分：顶栏正中、读秒上面一行。
   数字比读秒大一号并加粗——连着打了好几局时，「3 : 2」是这一屏最该被
   一眼看到的信息；冒号压暗，让两个数字各自成组。
   顶栏总高 68px 不变，这一行只占 16px，连击标记出现时也不会把它挤下去。 */
.pm__series {
  display: flex;
  align-items: baseline;
  gap: 2px;
  line-height: 16px;
  font-variant-numeric: tabular-nums;
  color: rgba(255, 255, 255, 0.94);
}

.pm__series-num {
  font-size: 16px;
  font-weight: 700;
}

.pm__series-colon {
  /* 冒号不能太淡：0.45 的白在深色顶栏上几乎看不见，两个数字会读成「0 0」。 */
  padding: 0 2px;
  font-size: 14px;
  font-style: normal;
  color: rgba(255, 255, 255, 0.62);
}

.pm__clock {
  flex: none;
  /* 读秒与「对手回合」是玩家一直在瞥的一行字，12px 在深色底上要凑近才认得出，
     放大到 15px 并提亮一档。 */
  font-size: 15px;
  font-variant-numeric: tabular-nums;
  color: #9aa2ae;
  white-space: nowrap;
}

/* 中间一格：上面读秒，下面可能挂一枚「继续击打」。
   竖排而不是横排：读秒本身占满这一格了，横着再塞一枚胶囊会把两边的卡挤窄。 */
.pm__mid {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  min-width: 0;
}

/* 连击标记：黄底深字的小胶囊。
   用底色而不是只有一行字，是因为它在余光里要能被看见——玩家正盯着球台，
   不会去读顶栏。它不是三秒就滑走的横幅，而是一直挂到这一杆打完。 */
.pm__cont {
  padding: 0 7px;
  font-size: 11px;
  font-weight: 600;
  line-height: 16px;
  color: #2a2100;
  white-space: nowrap;
  background: #f0c02f;
  border-radius: 999px;
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

/* 对手回合时压在力度条顶上的小标签。
   不加这一条，对面推力度时我这边也会有一条在动，会以为是自己那根。

   位置：让到聊天入口上面，与滑轨同一根竖轴。
   聊天入口占着「力度条往上 10~38px」那一段（`.pm__chat-btn`），
   标签原来也在这段（top: -34px），于是那颗蓝气泡被「对方」压掉半个，
   两个都读不成完整的形状。现在标签底边抬到 44px（入口顶再往上 6px），
   横向用 left: 50% + 负半个自身宽度居中，正好落在滑轨这根轴上。
   拉杆为 0 时手柄露在滑轨顶部 10px，标签与手柄之间照旧留着余量。 */
.pm__power-tag {
  position: absolute;
  bottom: calc(100% + 44px);
  left: 50%;
  padding: 1px 7px;
  font-size: 12px;
  line-height: 16px;
  color: rgba(255, 255, 255, 0.86);
  white-space: nowrap;
  background: rgba(12, 18, 24, 0.8);
  border-radius: 8px;
  transform: translateX(-50%);
  pointer-events: none;
}

.pm__foot {
  position: relative;
  flex: none;
  width: 100%;
  max-width: 820px;
  margin: 0 auto;
  padding: 2px 10px 8px;
}

/* 音效开关：钉在底栏右下角，绝对定位，不占行高。
   早先试过这么放，结果正好压在微调尺的 +0.05° 按钮上——那时尺子更宽。
   现在尺子收窄到 232px（390 宽屏幕居中后右端在 323），右下角到屏幕边
   还有四十来像素，30px 的按钮塞得进去，而且不再占那 28px 高度。 */
.pm__sound {
  position: absolute;
  right: 10px;
  bottom: 6px;
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  padding: 0;
  color: rgba(255, 255, 255, 0.62);
  background: rgba(255, 255, 255, 0.06);
  border: none;
  border-radius: 8px;
  cursor: pointer;
}

/* 关掉时压得更暗：一眼能读出来「现在是静的」。 */
.pm__sound[aria-pressed='false'] {
  color: rgba(255, 255, 255, 0.34);
  background: rgba(255, 255, 255, 0.03);
}

.pm__sound:active {
  background: rgba(255, 255, 255, 0.16);
}

/* 聊天入口：挂在力度条正上方的一颗实心蓝气泡。
   它是 .pm__power 的子元素，所以永远跟着力度条走。
   横轴按 34px 的槽居中（left 50% + 负半个按钮宽），与滑轨同一根竖轴。
   以前贴在左侧库的 46% 高度上，那儿正好是长库中间的袋口，压掉半个洞。

   尺寸用 bottom 而不是 top：按钮要的是「底边离滑轨固定 10px」，
   用 top 的话按钮高度一改（44 → 34）间距就跟着变，
   矮屏上还会顶到顶栏。
   44px 那版用户反馈「压到球桌了」——球台右上角的木框就在这一带，
   34px 收进留白里，视觉上也仍然是最跳的一颗实心蓝。 */
.pm__chat-btn {
  position: absolute;
  bottom: calc(100% + 10px);
  /* 与滑轨同一根竖轴：滑轨 12px 在 44px 的 .pm__power 里居中，
     中心离右沿 22px；按钮 28px 要让中心落在那儿，右沿就得进来 8px。
     以前是 right: 0（贴列的外沿），实测中心比滑轨偏右 8px，
     看着像两颗东西各站一边。
     居中会让按钮左沿压到球台木框 6px——比偏 8px 更容易被读成「歪」，
     而且下方加塞球（46px）本来也压着同一段框 15px，这里不算新问题。 */
  right: 8px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  color: #fff;
  /* 实心蓝 + 外发光：绿色台呢与深灰底上都是最跳的那一档颜色，
     又不至于像红色那样被读成警告。 */
  background: linear-gradient(160deg, #3b95ff, #1659cf);
  border: 1px solid rgba(255, 255, 255, 0.28);
  border-radius: 50%;
  box-shadow:
    0 5px 14px rgba(20, 88, 190, 0.48),
    0 0 0 2px rgba(59, 149, 255, 0.14);
  cursor: pointer;
}

.pm__chat-btn:active {
  background: linear-gradient(160deg, #2f86f0, #124dbb);
}

/* 有未读时轻轻「呼吸」一下，余光就能看到。
   box-shadow 动画不触发布局，代价可以忽略；开了减少动效的机器上直接关掉。 */
.pm__chat-btn.has-unread {
  animation: pm-chat-breathe 1.8s ease-out infinite;
}

@keyframes pm-chat-breathe {
  0%,
  100% {
    box-shadow:
      0 5px 14px rgba(20, 88, 190, 0.48),
      0 0 0 2px rgba(59, 149, 255, 0.14);
  }

  55% {
    box-shadow:
      0 5px 14px rgba(20, 88, 190, 0.48),
      0 0 0 8px rgba(59, 149, 255, 0);
  }
}

@media (prefers-reduced-motion: reduce) {
  .pm__chat-btn.has-unread {
    animation: none;
  }
}

/* 图标在 flex 容器里会被压缩：不给 flex:none 的话 16px 的气泡只剩一颗白点。 */
.pm__chat-btn svg {
  display: block;
  flex: none;
}

/* 矮屏（横过来的手机）：球台被压到两百来像素，力度条上方挤不下
   「入口 + 对方」这一摞。
   这一档把标签收起来——顶栏正中本来就写着「对手回合」，标签是重复的；
   入口收近一点贴着滑轨，两者都不至于压到顶栏下面那条倒计时。 */
@media (max-height: 520px) {
  .pm__power-tag {
    display: none;
  }

  .pm__chat-btn {
    bottom: calc(100% + 6px);
  }
}

/* 未读点压在气泡右上角，不挡图标本身。
   按钮收到 28px 之后红点也跟着从 15 收到 12，
   否则一颗 15px 的点会盖掉按钮小半个圆。 */
.pm__chat-badge {
  position: absolute;
  top: -3px;
  right: -3px;
  min-width: 12px;
  height: 12px;
  padding: 0 3px;
  font-size: 9px;
  font-weight: 700;
  line-height: 12px;
  color: #fff;
  text-align: center;
  background: #d2453a;
  border-radius: 8px;
}

/* 聊天面板：底部抽屉。
   max-height 而不是固定高度：输入框随内容长高时面板跟着长，
   到了 62vh 就不再长，多出来的由列表自己滚动承担。 */
.pm__chat {
  position: fixed;
  right: 0;
  bottom: 0;
  left: 0;
  z-index: 30;
  display: flex;
  flex-direction: column;
  max-height: 62vh;
  padding: 10px 12px calc(12px + env(safe-area-inset-bottom));
  background: #161a21;
  border-top: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 16px 16px 0 0;
  box-shadow: 0 -18px 40px rgba(0, 0, 0, 0.45);
}

/* 桌面端不做通栏：一条横跨 1400px 的深色板子压在球台上太出戏，
   而聊天气泡本身只有两三百像素宽，右边全空着。
   宽度只比球台宽一点（900 高的屏幕上球台约 404px，1080 高约 485px），
   居中浮在底部，四角都圆。
   居中用 margin: auto 而不是 transform：进出动画动的就是 transform，
   两者叠在一起会让面板在滑入时横向跳一下。 */
@media (min-width: 721px) {
  .pm__chat {
    bottom: 18px;
    width: min(560px, calc(100% - 40px));
    max-height: min(62vh, 520px);
    margin: 0 auto;
    border: 1px solid rgba(255, 255, 255, 0.1);
    border-radius: 16px;
  }
}

.pm__chat-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding-bottom: 8px;
}

.pm__chat-title {
  flex: 1;
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 0.2px;
  color: rgba(255, 255, 255, 0.92);
}

/* 收起按钮：标题占满左边，它顶在最右。 */
.pm__chat-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  color: rgba(255, 255, 255, 0.6);
  background: transparent;
  border: none;
  cursor: pointer;
}

/* 消息列表：占满面板剩下的高度，自己滚。
   overscroll-behavior 挡住「滚到头继续拽，把整个页面带着动」。 */
.pm__chat-log {
  flex: 1;
  min-height: 96px;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 4px 2px 8px;
}

.pm__chat-empty {
  margin: 24px 0;
  font-size: 12px;
  color: rgba(255, 255, 255, 0.34);
  text-align: center;
}

/* 一行消息：气泡 + 时间。自己的靠右，对手的靠左。 */
.pm__chat-row {
  display: flex;
  align-items: flex-end;
  gap: 6px;
  margin: 6px 0;
}

.pm__chat-row.is-mine {
  flex-direction: row-reverse;
}

.pm__chat-bubble {
  max-width: 74%;
  padding: 7px 11px;
  font-size: 13px;
  line-height: 1.4;
  color: rgba(255, 255, 255, 0.9);
  overflow-wrap: anywhere;
  background: rgba(255, 255, 255, 0.09);
  border-radius: 12px 12px 12px 4px;
}

.pm__chat-row.is-mine .pm__chat-bubble {
  color: #fff;
  background: var(--fs-primary);
  border-radius: 12px 12px 4px 12px;
}

/* 纯表情：去掉气泡底色，放大成一颗「贴纸」。
   底色留着的话，一个 20px 的表情会躺在一块大圆角里，像发错了格式。 */
.pm__chat-bubble.is-sticker {
  padding: 0 2px;
  font-size: 30px;
  line-height: 1.1;
  background: transparent;
  border-radius: 0;
}

.pm__chat-clock {
  flex: none;
  font-size: 10px;
  color: rgba(255, 255, 255, 0.28);
}

/* 表情条：横向排一行，放不下就滚，不折行——折行会把输入框顶下去。 */
.pm__chat-emojis {
  display: flex;
  flex-wrap: nowrap;
  gap: 6px;
  padding: 6px 0 8px;
  overflow-x: auto;
  border-top: 1px solid rgba(255, 255, 255, 0.07);
  /* 一行放不下十来颗表情，靠横滑看后面几个。滚动条要藏掉：
     它会在面板底部横一条浅灰的实心条，看着像输入框上面多长了一块；
     手指横滑本来就不需要它。被切掉半颗的表情已经说明「右边还有」。 */
  scrollbar-width: none;
}

.pm__chat-emojis::-webkit-scrollbar {
  display: none;
}

.pm__chat-emoji {
  flex: none;
  width: 34px;
  height: 34px;
  padding: 0;
  font-size: 19px;
  line-height: 1;
  background: rgba(255, 255, 255, 0.06);
  border: none;
  border-radius: 9px;
  cursor: pointer;
}

.pm__chat-emoji:active {
  background: rgba(255, 255, 255, 0.2);
}

/* 输入区：输入框吃掉剩余宽度，发送按钮定宽贴在右边。 */
.pm__chat-input {
  display: flex;
  align-items: flex-end;
  gap: 8px;
}

.pm__chat-input :deep(.el-textarea__inner) {
  padding: 8px 10px;
  font-size: 13px;
  color: rgba(255, 255, 255, 0.9);
  background: rgba(255, 255, 255, 0.07);
  border: none;
  border-radius: 10px;
  box-shadow: none;
  resize: none;
}

.pm__chat-input :deep(.el-textarea__inner::placeholder) {
  color: rgba(255, 255, 255, 0.32);
}

.pm__chat-send {
  flex: none;
  height: 34px;
  padding: 0 16px;
  font-size: 13px;
  font-weight: 600;
  color: #fff;
  background: var(--fs-primary);
  border: none;
  border-radius: 10px;
  cursor: pointer;
}

.pm__chat-send:disabled {
  color: rgba(255, 255, 255, 0.34);
  background: rgba(255, 255, 255, 0.08);
}

/* 抽屉的进出：只做位移与淡出，不做高度动画（高度动画要读布局，手机上会掉帧）。 */
.pm-chat-enter-active,
.pm-chat-leave-active {
  transition:
    transform 0.22s ease,
    opacity 0.22s ease;
}

.pm-chat-enter-from,
.pm-chat-leave-to {
  opacity: 0;
  transform: translateY(14%);
}

/* 桌下那一排已进袋的球：按落袋先后从左往右排，新进来的有一个落下的动作。
   min-height 恰好是一颗球加下内边距：这一排从「空」变成「有一颗」时高度不变，
   球台就不会在进球那一刻缩一下。

   不能折行（nowrap + 允许缩小）：一局最多 15 颗球，26px 的球在手机上只能放 11 颗，
   第 12 颗一进来就换到第二行，底栏长高 31px，球台整块往上跳。
   现在超出时按比例缩小、始终保持一行，一行的高度不会因为颗数变化而变。 */
.pm__potted {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  gap: 5px;
  min-height: 28px;
  justify-content: center;
  padding: 0 4px 2px;
}

.pm__potted-ball {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  /* 26px、11px：20px 时球号只有 9px，深色底上要凑近才看得清是几号。
     缩到最小 15px 是因为一局 15 颗球要在手机一行里排完（见上）。
     aspect-ratio 让球在缩小后仍是正圆，不会压成椭圆。 */
  flex: 0 1 26px;
  min-width: 15px;
  width: 26px;
  aspect-ratio: 1 / 1;
  height: auto;
  font-size: clamp(8px, 1.7vw, 11px);
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
/* 底栏这一行的高度必须写死。
   摆球模式换成提示条（.pm__place 高 44），瞄准模式换成微调尺（.ft 高 42），
   两种形态高度差 2px，切一次球台就跟着缩 2px——就是玩家说的
   「打完一杆画面动了一下」。这里把行高钉在 44，两种形态都在里面垂直居中。 */
.pm__ctls {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  /* 跟着微调尺一起收到 36：这一行的高度直接决定球台能多高。 */
  height: 36px;
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

/* 竖屏手机 + 屏幕不够高（浏览器工具栏吃掉一百多像素的那种）：这一档是
   「球台尽量大」专用的收紧。球台的宽度完全由「屏幕高 − 顶栏 − 燃烧条 − 底栏」
   决定，而它是 1.2022 : 2.1928 的竖版画法，每省下 1px 高度只能换 0.55px 宽度，
   所以三处一起收：顶栏、已进袋那排、底栏留白。
   判据 780px：再高的屏幕上球台已经是被宽度卡住的（占满整栏），
   这时收高度只会多出上下留白，那排已进袋的球就不该被牺牲。 */
@media (max-width: 720px) and (max-height: 780px) {
  /* 座位卡本身 54 高（头像 42 + 上下内边距 12），顶栏 58 刚好放得下。 */
  .pm__bar {
    height: 58px;
    padding: 0 10px;
  }

  /* 这一排只是「这一局打到哪了」的补充：席位卡上已经在写各自剩几颗。
     矮屏上它值 30px 高度，换算成球台宽度就是 16px，这里让位。 */
  .pm__potted {
    display: none;
  }

  .pm__foot {
    padding: 0 10px 2px;
  }

  /* 去掉球台下面那 4px 的内边距：它下面紧接着就是底栏，留白没有意义。 */
  .pm__stage {
    padding: 0 34px 0 12px;
  }
}
</style>
