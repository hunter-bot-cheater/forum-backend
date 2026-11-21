<template>
  <div class="pm">
    <!-- 顶部只留一条窄栏。对局页不套站点 Layout：60px 顶栏加内容区留白，
         在竖屏手机上会吃掉球台将近六分之一的高度。 -->
    <header class="pm__bar">
      <button class="pm__back" type="button" aria-label="返回" @click="leave">
        <el-icon :size="18"><ArrowLeft /></el-icon>
      </button>
      <span class="pm__title">对局 #{{ id }}</span>
      <button class="pm__reset" type="button" @click="resetRack">重摆</button>
      <span class="pm__state">{{ stateText }}</span>
    </header>

    <main class="pm__stage">
      <PoolTable :balls="balls" :aim="aim" @drag="onDrag" />
      <div class="pm__power">
        <PowerSlider v-model:value="power" :disabled="shooting" @shoot="shoot" />
      </div>
    </main>

    <footer class="pm__foot">
      <FineTuneRail :phi="phi" :disabled="shooting" @nudge="onNudge" />
    </footer>
  </div>
</template>

<script setup lang="ts">
// 对局页。M2 阶段是本地练习：球位来自开球摆球，轨迹由本地假物理生成，
// 不连服务端。M3 之后出杆改成发参数给 pool-sim，这里的交互层不用重写。
import { computed, onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowLeft } from '@element-plus/icons-vue'
import FineTuneRail from '@/components/pool/FineTuneRail.vue'
import PoolTable from '@/components/pool/PoolTable.vue'
import PowerSlider from '@/components/pool/PowerSlider.vue'
import { adjustPhi, aimFromCue, normalizePhi, rotateByDrag } from '@/game/aim'
import { applySample, playShot, type PlayHandle } from '@/game/animator'
import { fakeShot } from '@/game/fake'
import type { AimLayer } from '@/game/renderer'
import { RACK_EIGHTBALL } from '@/game/table'
import type { BallState, PoolDrag } from '@/game/types'
import { goBack } from '@/utils/nav'

// 对局 id 由路由 props 注入。M3 之后用它拉快照与建 WS。
defineProps<{
  id: string
}>()

const router = useRouter()

// rackBalls 生成一份开球摆球的球状态，每次调用都是新对象：
// applySample 会原地改坐标，共用同一份会让「重摆」失效。
function rackBalls(): BallState[] {
  const list: BallState[] = []
  for (const ball of RACK_EIGHTBALL) {
    list.push({ id: ball.id, x: ball.x, y: ball.y, z: 0, on_table: true })
  }
  return list
}

const balls = ref<BallState[]>(rackBalls())
const power = ref(0)
const shooting = ref(false)

// 初始瞄准方向：母球指向 8 号球。开球时玩家想看的就是这条线，
// 默认朝右或朝上都要先自己转一下。
const phi = ref(initialPhi())

// 拖动开始时的角度。整个拖动过程都基于它加总位移，
// 逐帧累加会把浮点误差攒起来，拖久了瞄准线会慢慢偏。
let dragBase = 0
let handle: PlayHandle | null = null

// initialPhi 算母球到 8 号球的方向。
function initialPhi(): number {
  const cue = RACK_EIGHTBALL.find((b) => b.id === 'cue')
  const eight = RACK_EIGHTBALL.find((b) => b.id === '8')
  if (!cue || !eight) {
    return 0
  }
  return normalizePhi(Math.atan2(eight.y - cue.y, eight.x - cue.x))
}

// aim 是当前瞄准线。出杆过程中不画：球在动的时候还挂一条虚线，
// 会让人以为下一杆方向已经定了。
const aim = computed<AimLayer | null>(() => {
  if (shooting.value) {
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

// stateText 是右上角的状态提示。
const stateText = computed(() => {
  if (shooting.value) {
    return '出杆中'
  }
  const cue = balls.value.find((b) => b.id === 'cue')
  if (!cue || !cue.on_table) {
    return '母球落袋'
  }
  return '你的回合'
})

// onDrag 处理画布拖动：只认水平位移，换算出新的瞄准角。
function onDrag(payload: PoolDrag) {
  if (shooting.value) {
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
  phi.value = adjustPhi(phi.value, deltaDeg)
}

// shoot 出杆：用当前力度与角度生成轨迹并播放。
//
// 轨迹是本地假物理算的，只为把「瞄准 → 力度 → 播放 → 球位更新」这条链路跑通。
// M3 换成服务端结果后，这里只把 fakeShot 换成请求，其余不动。
function shoot() {
  if (shooting.value || power.value < 0.02) {
    return
  }
  shooting.value = true

  const shot = fakeShot(balls.value, phi.value, power.value)
  // 出杆后力度归零：下一杆要从头拖，留着上一次的满力容易误出杆。
  power.value = 0

  handle = playShot(shot, {
    onSample: (sample) => {
      applySample(balls.value, sample)
    },
    onDone: () => {
      shooting.value = false
      handle = null
    },
  })
}

// resetRack 把球摆回开球位置，方便反复试手。
function resetRack() {
  if (handle) {
    handle.stop()
    handle = null
  }
  shooting.value = false
  power.value = 0
  balls.value = rackBalls()
  phi.value = initialPhi()
}

// leave 离开对局页。正在播的动画要先停，否则组件卸载后回调仍在改数组。
function leave() {
  if (handle) {
    handle.stop()
    handle = null
  }
  goBack(router, '/articles')
}

onBeforeUnmount(() => {
  if (handle) {
    handle.stop()
    handle = null
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
  gap: 10px;
  height: 44px;
  padding: 0 10px;
  color: #e8eaed;
}

.pm__back,
.pm__reset {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: 30px;
  padding: 0;
  color: inherit;
  background: rgba(255, 255, 255, 0.08);
  border: none;
  border-radius: 8px;
  cursor: pointer;
}

.pm__back {
  width: 30px;
}

.pm__reset {
  padding: 0 10px;
  font-size: 12px;
}

.pm__back:active,
.pm__reset:active {
  background: rgba(255, 255, 255, 0.18);
}

.pm__title {
  font-size: 13px;
  font-weight: 600;
}

.pm__state {
  margin-left: auto;
  font-size: 12px;
  color: #8a919c;
}

/* 剩余空间全给球台。min-height: 0 是 flex 子项能收缩的前提，
   少了它 canvas 会按内容高度撑开，把窄栏顶出屏幕。
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
</style>
