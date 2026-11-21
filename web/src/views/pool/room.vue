<template>
  <div class="pr">
    <header class="pr__head">
      <button class="pr__back" type="button" @click="leave">返回大厅</button>
      <div class="pr__code">
        <span class="pr__code-label">房号</span>
        <span class="pr__code-value">{{ room?.room_code || '——' }}</span>
      </div>
    </header>

    <!-- 房间不存在 / 已解散 / 我不在房里。
         这三种情况在接口上都表现为失败，用户能做的事只有一件：回大厅。 -->
    <div v-if="failed" class="pr__error">
      <p class="pr__error-text">{{ failed }}</p>
      <button class="pr__error-btn" type="button" @click="leave">回大厅</button>
    </div>

    <template v-else-if="room">
      <div class="pr__seats">
        <article
          v-for="side in sides"
          :key="side.seat"
          class="pr__seat"
          :class="{ 'is-ready': room.ready[side.seat], 'is-me': side.seat === room.seat }"
        >
          <div class="pr__seat-tag">{{ side.seat === room.seat ? '你' : '对手' }}</div>
          <el-avatar
            :size="64"
            :src="side.player.avatar || undefined"
            :style="{ backgroundColor: avatarColor(side.player.nickname) }"
          >
            {{ initialOf(side.player.nickname) }}
          </el-avatar>
          <span class="pr__seat-name">{{ side.player.nickname || '对手' }}</span>
          <span class="pr__seat-ready">
            {{ room.ready[side.seat] ? '已准备' : '未准备' }}
          </span>
        </article>
      </div>

      <p class="pr__hint">{{ hint }}</p>

      <div class="pr__actions">
        <button
          class="pr__ready"
          type="button"
          :class="{ 'is-cancel': mine }"
          :disabled="busy || room.status !== 'waiting'"
          @click="toggleReady"
        >
          {{ mine ? '取消准备' : '准备' }}
        </button>
      </div>
    </template>

    <div v-else class="pr__loading">正在进入房间…</div>
  </div>
</template>

<script setup lang="ts">
// 房间页：两边都点准备之后才开始对局。
//
// M5 的第三层。它必须能单独刷新——被邀请方常常是「点开链接 → 顺手刷新一下」，
// 所以页面的一切都来自 GET /api/pool/rooms/:id，不依赖任何内存态。
//
// 不连 WebSocket：准备是低频动作，1.5 秒轮询足够，而且天然带「进页面就有一份状态」。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getRoom, setReady, type PoolRoom } from '@/api/pool'
import { avatarColor } from '@/utils/avatar'
import { initials } from '@/utils/format'

const props = defineProps<{
  id: string
}>()

const router = useRouter()

const room = ref<PoolRoom | null>(null)
// failed 是「进不了这个房间」的说明文案，非空就整页只显示它。
const failed = ref('')
const busy = ref(false)

// 轮询间隔 1.5 秒。
//
// 比它更快没有意义：准备是人的动作，双方差 1.5 秒看到结果完全可接受；
// 更慢则会出现「对方已经准备好几秒了，我这边还没变」的错位感。
const POLL_MS = 1500

let timer: ReturnType<typeof setInterval> | null = null
// polling 挡住重入：一次请求没回来就不再发下一次，
// 否则服务端慢的时候会堆起一串请求，最后按乱序覆盖状态。
let polling = false

// mine 是「我准备了没」。
const mine = computed(() => {
  if (!room.value) {
    return false
  }
  return room.value.ready[room.value.seat]
})

// sides 是两张座位卡的展示顺序：自己永远在左。
// 座位号是服务端分配的，直接按 0/1 顺序排会让一半用户看到自己在右边。
const sides = computed(() => {
  if (!room.value) {
    return []
  }
  const me = room.value.seat
  const other = me === 0 ? 1 : 0
  return [
    { seat: me, player: room.value.players[me] },
    { seat: other, player: room.value.players[other] },
  ]
})

// hint 是一句状态说明，跟着准备状态走。
//
// 不写「等待对手进房」：房间建立时双方资料就已经在快照里了，
// 陌生人是被 403 挡在外面的，所以「对手在不在房间里」这一页根本看不到。
// 能看到的只有「他准备了没」。
const hint = computed(() => {
  if (!room.value) {
    return ''
  }
  if (room.value.status === 'playing') {
    return '对局开始了，正在进入球台…'
  }
  if (room.value.status === 'finished') {
    return '这一局已经结束了'
  }
  if (room.value.status === 'expired') {
    return '房间因长时间没人就绪而解散了'
  }
  const otherSeat = room.value.seat === 0 ? 1 : 0
  if (!mine.value && !room.value.ready[otherSeat]) {
    return '双方都点准备后开始。'
  }
  if (!mine.value) {
    return '对手已经准备好了，等你了。'
  }
  if (!room.value.ready[otherSeat]) {
    return '你已准备好，等对手点准备。'
  }
  return '双方都准备好了。'
})

function initialOf(name?: string): string {
  if (!name) {
    return ''
  }
  return initials(name)
}

// apply 用服务端返回的快照整份替换本地状态。
//
// 不做增量合并：两份状态一旦分叉，页面会一直显示错的准备标记，
// 而这份数据本来就很小，整份换掉最省事也最不容易错。
function apply(next: PoolRoom) {
  room.value = next
  if (next.status === 'playing') {
    stopPolling()
    void router.replace('/pool/match/' + String(next.match_id))
  }
}

// poll 拉一次房间状态。失败不是致命错误，等下一轮再试。
async function poll() {
  if (polling) {
    return
  }
  polling = true
  try {
    const next = await getRoom(props.id)
    apply(next)
  } catch (err) {
    // 404 与 403 意味着房间真的没了（被解散、我不在房里），
    // 继续轮询只会永远失败，直接停表并显示说明。
    const status = statusOf(err)
    if (status === 404 || status === 403) {
      stopPolling()
      failed.value = status === 403 ? '你不在这一局里' : '房间已经解散或结束了'
    }
  } finally {
    polling = false
  }
}

// statusOf 从 axios 错误里取 HTTP 状态码，取不到返回 0。
// 只在这一个地方用，不进 utils：判断「房间没了」是这一页自己的语义。
function statusOf(err: unknown): number {
  const resp = (err as { response?: { status?: number } })?.response
  if (resp && typeof resp.status === 'number') {
    return resp.status
  }
  return 0
}

function startPolling() {
  if (timer) {
    return
  }
  timer = setInterval(() => {
    void poll()
  }, POLL_MS)
}

function stopPolling() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

// toggleReady 切换自己的准备状态。
//
// 接口返回整份房间状态，直接 apply：不做「本地先翻转再等服务端确认」的乐观更新，
// 准备按钮点错一次只是等一下，而乐观更新在失败时会留下一个假的「已准备」。
async function toggleReady() {
  if (busy.value || !room.value) {
    return
  }
  busy.value = true
  try {
    const next = await setReady(props.id, !mine.value)
    apply(next)
  } catch {
    // 拦截器已提示。下一轮轮询会把真实状态拉回来。
  } finally {
    busy.value = false
  }
}

// leave 主动离开房间页。
//
// 只做导航，不通知服务端：房间会在 90 秒后自动解散，
// 而「点了返回」和「关掉页面」在服务端看起来没有区别，
// 为前者加一个接口只会让两边状态更难对齐。
function leave() {
  stopPolling()
  void router.replace('/pool')
}

onMounted(() => {
  // 先立刻拉一次再起表：等 1.5 秒才看到房间会让人以为卡住了。
  void poll()
  startPolling()
})

onBeforeUnmount(stopPolling)
</script>

<style scoped>
.pr {
  max-width: 480px;
  margin: 0 auto;
  padding: 8px 0 24px;
}

.pr__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 24px;
}

.pr__back {
  padding: 6px 0;
  font-size: 13px;
  color: var(--fs-text-3);
  background: none;
  border: none;
  cursor: pointer;
}

.pr__code {
  display: flex;
  align-items: baseline;
  gap: 6px;
}

.pr__code-label {
  font-size: 12px;
  color: var(--fs-text-3);
}

/* 房号是这一页最重要的信息，用等宽字体加字距：
   它经常要被念出来，普通字体下 0 与 6、1 与 7 容易混。 */
.pr__code-value {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 20px;
  font-weight: 700;
  letter-spacing: 3px;
  color: var(--fs-text-1);
}

.pr__seats {
  display: flex;
  align-items: stretch;
  gap: 12px;
}

.pr__seat {
  position: relative;
  display: flex;
  flex: 1;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 22px 12px 18px;
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius);
  transition: border-color 0.2s, background-color 0.2s;
}

/* 已准备的一侧边框变主色：双方都在等对方，谁能一眼看出「就差他了」很重要。
   只改边框不改背景，是为了不让两张卡片一块深一块浅，看起来像选中了谁。 */
.pr__seat.is-ready {
  border-color: var(--fs-primary);
  background: var(--fs-primary-weak);
}

.pr__seat-tag {
  font-size: 11px;
  letter-spacing: 1px;
  color: var(--fs-text-3);
}

.pr__seat.is-me .pr__seat-tag {
  color: var(--fs-primary);
  font-weight: 600;
}

.pr__seat-name {
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-text-1);
}

.pr__seat-ready {
  font-size: 12px;
  color: var(--fs-text-3);
}

.pr__seat.is-ready .pr__seat-ready {
  color: var(--fs-primary);
  font-weight: 600;
}

.pr__hint {
  margin: 20px 0 0;
  font-size: 13px;
  line-height: 1.8;
  text-align: center;
  color: var(--fs-text-2);
}

.pr__actions {
  display: flex;
  justify-content: center;
  margin-top: 24px;
}

.pr__ready {
  min-width: 180px;
  height: 46px;
  font-size: 16px;
  font-weight: 600;
  color: #fff;
  background: var(--fs-primary);
  border: none;
  border-radius: 14px;
  cursor: pointer;
  transition: opacity 0.15s;
}

/* 已准备状态下的按钮改成描边：主色实心再点一次就是「取消」，
   视觉上不该比「准备」更强势。 */
.pr__ready.is-cancel {
  color: var(--fs-primary);
  background: var(--fs-primary-weak);
}

.pr__ready:disabled {
  opacity: 0.5;
  cursor: default;
}

.pr__loading {
  padding: 60px 0;
  font-size: 13px;
  text-align: center;
  color: var(--fs-text-3);
}

.pr__error {
  padding: 40px 0;
  text-align: center;
}

.pr__error-text {
  margin: 0 0 18px;
  font-size: 14px;
  color: var(--fs-text-2);
}

.pr__error-btn {
  height: 38px;
  padding: 0 22px;
  font-size: 14px;
  color: var(--fs-primary);
  background: var(--fs-primary-weak);
  border: none;
  border-radius: var(--fs-radius-sm);
  cursor: pointer;
}
</style>
