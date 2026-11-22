<template>
  <div class="rp">
    <header class="rp__bar">
      <button class="rp__back" type="button" aria-label="返回" @click="back">
        <el-icon :size="18"><ArrowLeft /></el-icon>
      </button>
      <div class="rp__who">
        <span class="rp__name" :class="winnerSide === 0 ? 'is-win' : ''">{{ nameOf(0) }}</span>
        <span class="rp__vs">vs</span>
        <span class="rp__name" :class="winnerSide === 1 ? 'is-win' : ''">{{ nameOf(1) }}</span>
      </div>
      <span class="rp__reason">{{ reasonText }}</span>
    </header>

    <main class="rp__stage fs-table-stage">
      <div class="fs-table-box">
        <PoolTable :balls="balls" :aim="null" />
      </div>

      <!-- 三种「没有画面」要分开：这一局不归你看（重试也没用）、
           还没结算、以及拉失败（可以重试）。 -->
      <p v-if="blockedText" class="rp__hint">{{ blockedText }}</p>
      <p v-else-if="loadFailed" class="rp__hint">
        回放没加载出来。<button class="rp__retry" type="button" @click="load">重试</button>
      </p>
      <p v-else-if="!loaded" class="rp__hint">正在读取回放…</p>
      <p v-else-if="shots.length === 0" class="rp__hint">这一局没有可回放的杆。</p>
    </main>

    <footer class="rp__foot">
      <button class="rp__step" type="button" :disabled="!canStepBack" @click="stepBack">上一杆</button>
      <button class="rp__play" type="button" :disabled="shots.length === 0" @click="toggle">
        {{ playing ? '暂停' : '播放' }}
      </button>
      <button class="rp__step" type="button" :disabled="!canStepForward" @click="stepForward">下一杆</button>
      <span class="rp__count">{{ countText }}</span>
    </footer>
  </div>
</template>

<script setup lang="ts">
// 对局回放。
//
// 回放的数据就是落库的原始帧：与对局时的 shot 帧同形，所以这里复用同一个
// animator 和同一块球台，没有第二套渲染。
//
// 跳杆（上一杆/下一杆）不重播前面所有杆：从开局摆球起，把每一杆的最后一帧
// 依次套上去就够了——服务端的每帧都带全部在台面上的球，套一次即等于那一杆结束时的局面。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowLeft } from '@element-plus/icons-vue'
import PoolTable from '@/components/pool/PoolTable.vue'
import { getReplay, type ReplayResp } from '@/api/pool'
import { applySample, playShot, sampleAt, type PlayHandle } from '@/game/animator'
import { reasonNeutral } from '@/game/reasons'
import { RACK_EIGHTBALL } from '@/game/table'
import type { BallState, Seat } from '@/game/types'
import { goBack } from '@/utils/nav'

const props = defineProps<{
  id: string
}>()

const router = useRouter()

const replay = ref<ReplayResp | null>(null)
const balls = ref<BallState[]>([])
const loaded = ref(false)
const loadFailed = ref(false)
// blockedText 是「重试也不会变」的那几种情况（不是你的局、还没结算）。
// 与 loadFailed 分开：给一个必然失败的操作留按钮，只会让人反复点。
const blockedText = ref('')

// index 是「已经播完几杆」：0 表示还停在开局摆球上。
const index = ref(0)
const playing = ref(false)

let handle: PlayHandle | null = null
// disposed 挡住卸载后仍在回调里的动画帧。
let disposed = false

const shots = computed(() => replay.value?.shots ?? [])
// 还没拿到数据时是 -1，两边都不高亮。
// 用 0 兜底的话，加载中的那一瞬间左侧名字就已经被标成赢家了。
const winnerSide = computed(() => {
  if (!replay.value) {
    return -1
  }
  return replay.value.winner
})
// 回放页没有「我」这一侧，用不带主语的赢法说法（见 game/reasons.ts）。
const reasonText = computed(() => {
  const value = replay.value
  if (!value) {
    return ''
  }
  return reasonNeutral(value.win_reason)
})

const canStepBack = computed(() => loaded.value && index.value > 0)
const canStepForward = computed(() => loaded.value && index.value < shots.value.length)
const countText = computed(() => {
  const total = shots.value.length
  if (total === 0) {
    return ''
  }
  return '第 ' + String(Math.min(index.value + 1, total)) + ' / ' + String(total) + ' 杆'
})

// nameOf 取某一侧昵称，没资料时说「玩家 1 / 玩家 2」。
function nameOf(seat: Seat): string {
  const player = replay.value?.players[seat]
  if (!player || !player.nickname) {
    return '玩家 ' + String(seat + 1)
  }
  return player.nickname
}

// resetRack 把球放回开场摆位。
function resetRack() {
  const list: BallState[] = []
  for (const item of RACK_EIGHTBALL) {
    list.push({ id: item.id, x: item.x, y: item.y, z: 0, on_table: true })
  }
  balls.value = list
}

// stop 停掉正在播的那一杆。
function stop() {
  if (handle) {
    handle.stop()
    handle = null
  }
}

// seekTo 把画面定到「第 k 杆开始之前」。
//
// 先逐杆套前几杆的末帧，再单独套第 k 杆的首帧：
// 第 k 杆开打时的局面并不等于第 k-1 杆结束时那一帧——母球落袋后是下一杆
// 才摆回台面的，它的位置只存在于下一杆的首帧里。只套末帧的话，
// 停在下一杆开头时台面上没有母球，看着像球没了。
function seekTo(k: number) {
  stop()
  playing.value = false
  resetRack()
  const list = shots.value
  for (let i = 0; i < k && i < list.length; i += 1) {
    const frames = list[i].frames
    if (frames.length === 0) {
      continue
    }
    const last = frames[frames.length - 1]
    applySample(balls.value, sampleAt([last], last.t))
  }
  if (k < list.length) {
    const opening = list[k].frames
    if (opening.length > 0) {
      applySample(balls.value, sampleAt([opening[0]], opening[0].t))
    }
  }
  index.value = k
}

// playFrom 从第 k 杆开始顺序播到结束。
function playFrom(k: number) {
  const list = shots.value
  if (disposed || k >= list.length) {
    playing.value = false
    return
  }
  seekTo(k)
  playing.value = true
  const shot = list[k]
  handle = playShot(
    { frames: shot.frames, duration: shot.duration },
    {
      onSample: (sample) => {
        applySample(balls.value, sample)
      },
      onDone: () => {
        handle = null
        if (disposed) {
          return
        }
        index.value = k + 1
        if (playing.value) {
          playFrom(k + 1)
        }
      },
    },
  )
}

// toggle 播放 / 暂停。暂停时停在当前这一杆的开头（seekTo 已到位），
// 再点播放就从这一杆重新开始，不会从半路接上——半路接上会让人以为漏了半杆。
function toggle() {
  if (playing.value) {
    const resumeAt = Math.max(0, index.value)
    stop()
    playing.value = false
    seekTo(resumeAt)
    return
  }
  if (index.value >= shots.value.length) {
    playFrom(0)
    return
  }
  playFrom(index.value)
}

// stepBack 回到上一杆的开头。
function stepBack() {
  if (!canStepBack.value) {
    return
  }
  seekTo(index.value - 1)
}

// stepForward 跳到下一杆的开头；已经是最后一杆时把它播完。
function stepForward() {
  if (!canStepForward.value) {
    return
  }
  if (index.value + 1 >= shots.value.length) {
    playFrom(index.value)
    return
  }
  seekTo(index.value + 1)
}

// load 拉回放数据，成功后从第一杆开始播。
//
// 只有网络错误与 5xx 值得给重试按钮；403 / 404 / 409 是这一局的属性
// （不是你的局、没有这局、还没结算），重试一万次结果都一样。
async function load() {
  loadFailed.value = false
  blockedText.value = ''
  try {
    const resp = await getReplay(props.id)
    if (disposed) {
      return
    }
    replay.value = resp
    loaded.value = true
    resetRack()
    if (resp.shots.length > 0) {
      playFrom(0)
    }
  } catch (error) {
    if (disposed) {
      return
    }
    if (explainBlocked(error)) {
      return
    }
    // 拦截器已提示；页面显示重试入口。
    loadFailed.value = true
  }
}

// explainBlocked 把「重试也没用」的几种状态码翻成一句说明。
// 返回 true 表示已经接管了这次错误，页面不再显示重试按钮。
function explainBlocked(error: unknown): boolean {
  const status = (error as { response?: { status?: number } } | null)?.response?.status
  if (status === 403) {
    blockedText.value = '这一局不是你的对局，看不到回放。'
    return true
  }
  if (status === 404) {
    blockedText.value = '这一局没有回放，可能已经被清理了。'
    return true
  }
  if (status === 409) {
    blockedText.value = '这一局还没结束，结算之后再来看。'
    return true
  }
  return false
}

// back 回战绩页。
function back() {
  goBack(router, '/pool/records')
}

onMounted(load)

onBeforeUnmount(() => {
  disposed = true
  stop()
})
</script>

<style scoped>
/* 回放页与对局页同一套深色底：它们展示的是同一块球台，
   换成浅底会让人以为是另一个功能。 */
.rp {
  display: flex;
  flex-direction: column;
  height: 100dvh;
  background: #0e1116;
}

.rp__bar {
  display: flex;
  flex: none;
  align-items: center;
  gap: 10px;
  height: 48px;
  padding: 0 10px;
  color: #e8eaed;
}

.rp__back {
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

.rp__who {
  display: flex;
  flex: 1;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}

.rp__name {
  font-size: 13px;
  color: #c8ccd3;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 赢家名字用主色描出来：回放页没有比分条，光看球看不出谁赢了。 */
.rp__name.is-win {
  font-weight: 700;
  color: #7aa3ff;
}

.rp__vs {
  flex: none;
  font-size: 11px;
  color: #6a7079;
}

.rp__reason {
  flex: none;
  font-size: 12px;
  color: #8a919c;
}

.rp__stage {
  position: relative;
  flex: 1;
  min-height: 0;
  padding: 0 8px 4px;
}

.rp__hint {
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

.rp__retry {
  padding: 0;
  font-size: 13px;
  color: #7aa3ff;
  background: none;
  border: none;
  cursor: pointer;
}

.rp__foot {
  display: flex;
  flex: none;
  align-items: center;
  gap: 10px;
  padding: 4px 12px 12px;
}

.rp__step,
.rp__play {
  height: 36px;
  padding: 0 16px;
  font-size: 13px;
  color: #c8ccd3;
  background: rgba(255, 255, 255, 0.08);
  border: none;
  border-radius: 10px;
  cursor: pointer;
}

.rp__play {
  font-weight: 600;
  color: #fff;
  background: #4c7ef3;
}

.rp__step:disabled,
.rp__play:disabled {
  opacity: 0.45;
  cursor: default;
}

.rp__count {
  flex: 1;
  font-size: 12px;
  text-align: right;
  color: #8a919c;
  font-variant-numeric: tabular-nums;
}
</style>
