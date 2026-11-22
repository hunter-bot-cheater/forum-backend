<template>
  <!-- 邀请弹层。挂在布局里，任何页面收到 pool_invite 都会弹出来。
       不做成路由跳转：把用户从正在读的文章上踢走，只为了问一句「打一局吗」，
       被拒之后还得自己找回来，代价太高。 -->
  <div v-if="current" class="pi" role="dialog" aria-modal="true" aria-labelledby="pi-title">
    <div class="pi__mask" @click="onLater"></div>

    <div class="pi__card">
      <div class="pi__accent" aria-hidden="true"></div>

      <p id="pi-title" class="pi__kicker">对局邀请</p>

      <div class="pi__who">
        <el-avatar :size="52" :src="current.from.avatar || undefined" :style="{ backgroundColor: avatarColor(current.from.nickname) }">
          {{ initial }}
        </el-avatar>
        <div class="pi__meta">
          <span class="pi__name">{{ current.from.nickname || '有人' }}</span>
          <span class="pi__desc">邀请你打一局中式八球</span>
        </div>
      </div>

      <dl class="pi__facts">
        <div class="pi__fact">
          <dt>房号</dt>
          <dd class="pi__code">{{ current.room_code }}</dd>
        </div>
        <div class="pi__fact">
          <dt>有效</dt>
          <!-- 倒计时按 store 里记的本地截止时刻算：协议给的 expires_in 只是
               「服务端发这一帧时还剩多少秒」，排队到现在早已不准。 -->
          <dd>{{ remain }} 秒</dd>
        </div>
      </dl>

      <div class="pi__actions">
        <button class="pi__btn pi__btn--ghost" type="button" :disabled="busy" @click="onDecline">拒绝</button>
        <button class="pi__btn pi__btn--primary" type="button" :disabled="busy" @click="onAccept">
          {{ busy ? '处理中…' : '接受，进房间' }}
        </button>
      </div>

      <button class="pi__later" type="button" @click="onLater">稍后再说</button>
    </div>
  </div>
</template>

<script setup lang="ts">
// 全局邀请弹层。
//
// 三件事：展示当前该处理的邀请、把「对方接受了」变成一次跳转、把一次性提示弹出来。
// 放在布局里而不是大厅页：邀请到达时用户可能在任何页面，弹层必须到处都在。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { usePoolStore } from '@/store/modules/pool'
import { avatarColor } from '@/utils/avatar'
import { initials } from '@/utils/format'

const router = useRouter()
const pool = usePoolStore()

// current 是当前展示的邀请。用 store 的 computed 而不是本地再存一份：
// 接受一条之后会立刻轮到下一条，本地副本要额外做同步，很容易漏。
const current = computed(() => pool.incoming)

const busy = ref(false)
const remain = ref(0)

let timer: ReturnType<typeof setInterval> | null = null

const initial = computed(() => {
  const name = current.value?.from.nickname
  if (!name) {
    return ''
  }
  return initials(name)
})

// startCountdown 起一支 1Hz 的表，每次按本地截止时刻重算剩余秒数。
//
// 不逐秒减：轮到排队的第二条邀请时，它入队已经过去十几秒了，
// 用推送时刻的 expires_in 重新起表会显示「还剩 55 秒」，而服务端那边已经过期。
function startCountdown() {
  stopCountdown()
  tick()
  if (!current.value) {
    return
  }
  timer = setInterval(tick, 1000)
}

// tick 重算剩余秒数，走到 0 就把这条本地收起。
function tick() {
  const invite = current.value
  if (!invite) {
    stopCountdown()
    remain.value = 0
    return
  }
  const left = Math.max(0, Math.ceil((invite.expires_at - Date.now()) / 1000))
  remain.value = left
  if (left === 0) {
    stopCountdown()
    // 到点就本地收起。服务端此时也已经把它标成过期，
    // 继续留在页面上只会诱使用户点一个必然失败的按钮。
    pool.dismiss(invite.invite_id)
  }
}

function stopCountdown() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

// 邀请换成另一条时重新起表。immediate 让首条到达时也能起上。
watch(
  () => current.value?.invite_id,
  () => {
    if (!current.value) {
      stopCountdown()
      return
    }
    startCountdown()
  },
  { immediate: true },
)

// 对方接受了我的邀请：把人带回大厅，也就是他自己的房间。
//
// 大厅现在就是房间页：进去之后对面已经坐着刚才那个人，房号还是那串数字。
// 用 replace 而不是 push，是为了不把「等待卡」留在历史里——
// 那条邀请已经被接受，退回去看到它只会让人以为又发了一次。
watch(
  () => pool.acceptedRoom,
  (room) => {
    if (!room) {
      return
    }
    pool.acceptedRoom = null
    void router.replace('/pool')
  },
)

// 双方都点了「再约一局」：新局已经开起来，两边同时跳到球台上。
//
// 这里用 match 页而不是 /pool：新局是直接从 playing 开始的，中间没有房间页，
// 跳到大厅只会看到一个和这一局无关的空房间。
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

// 一次性提示（对方拒绝等）。
watch(
  () => pool.notice,
  () => {
    const text = pool.takeNotice()
    if (text) {
      ElMessage.info(text)
    }
  },
)

// accept 接受邀请：成功后回大厅，也就是那间「已经有对手」的房间。
// 失败交给 request 拦截器弹提示，这里只负责把按钮解锁。
async function accept() {
  if (!current.value || busy.value) {
    return
  }
  busy.value = true
  const inviteId = current.value.invite_id
  try {
    await pool.accept(inviteId)
    stopCountdown()
    await router.replace('/pool')
  } catch {
    // 不 dismiss：失败可能只是网络抖了一下（限流、超时），这条邀请还活着。
    // 一旦 dismiss 会把它记进 ignored，本会话再也不会弹出来，
    // 用户就只剩下「刷新页面」这一条路。留在原地可以直接再点一次，
    // 真的过期了由倒计时自己收起来。
  } finally {
    busy.value = false
  }
}

async function onAccept() {
  await accept()
}

// decline 拒绝：通知服务端解散那个房间。
// 不等待结果——拒绝这件事在本地已经生效，接口失败最多是房间多挂 90 秒。
function onDecline() {
  if (!current.value || busy.value) {
    return
  }
  const inviteId = current.value.invite_id
  stopCountdown()
  void pool.decline(inviteId)
}

// onLater 先放一放：本地收起，不通知服务端。
// 邀请 60 秒后自然过期，用户可以继续读手上的文章。
function onLater() {
  if (!current.value) {
    return
  }
  stopCountdown()
  pool.dismiss(current.value.invite_id)
}

// ESC 等同「稍后再说」。弹层没有关闭按钮，有个键盘出口更贴近系统弹窗的习惯。
function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    onLater()
  }
}

window.addEventListener('keydown', onKeydown)

onBeforeUnmount(() => {
  stopCountdown()
  window.removeEventListener('keydown', onKeydown)
})
</script>

<style scoped>
/* 固定铺满整屏，z-index 高于顶栏（100）但低于 Element Plus 的弹窗（2000+），
   这样对局中弹出的确认框不会被邀请卡盖住。 */
.pi {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}

.pi__mask {
  position: absolute;
  inset: 0;
  background: rgba(9, 12, 18, 0.42);
  backdrop-filter: blur(2px);
}

.pi__card {
  position: relative;
  width: 100%;
  max-width: 320px;
  padding: 26px 22px 14px;
  overflow: hidden;
  background: var(--fs-surface);
  border-radius: 18px;
  box-shadow: 0 18px 50px rgba(9, 12, 18, 0.32);
  animation: pi-in 0.18s ease-out;
}

/* 顶部一条主色渐变：这张卡是打断性的，
   和站内其它白卡长得一样会让人以为是可以忽略的提示条。 */
.pi__accent {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 3px;
  background: linear-gradient(90deg, var(--fs-primary), #7aa3ff);
}

.pi__kicker {
  margin: 0 0 16px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 2px;
  color: var(--fs-text-3);
}

.pi__who {
  display: flex;
  align-items: center;
  gap: 12px;
}

.pi__meta {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.pi__name {
  font-size: 16px;
  font-weight: 600;
  color: var(--fs-text-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pi__desc {
  font-size: 13px;
  color: var(--fs-text-2);
}

.pi__facts {
  display: flex;
  gap: 10px;
  margin: 18px 0 20px;
}

.pi__fact {
  flex: 1;
  padding: 8px 10px;
  background: var(--fs-primary-weak);
  border-radius: 10px;
}

.pi__fact dt {
  font-size: 11px;
  color: var(--fs-text-3);
}

.pi__fact dd {
  margin: 2px 0 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-text-1);
}

/* 房号用等宽字体并拉开字距：这串数字是要念给对面听的，
   常规字体的 0 与 6、1 与 7 在小字号下容易读错。 */
.pi__code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  letter-spacing: 2px;
}

.pi__actions {
  display: flex;
  gap: 10px;
}

.pi__btn {
  flex: 1;
  height: 42px;
  font-size: 15px;
  font-weight: 600;
  border: none;
  border-radius: 12px;
  cursor: pointer;
  transition: opacity 0.15s, background-color 0.15s;
}

.pi__btn:disabled {
  opacity: 0.6;
  cursor: default;
}

.pi__btn--ghost {
  color: var(--fs-text-2);
  background: var(--fs-surface-hover);
}

.pi__btn--primary {
  color: #fff;
  background: var(--fs-primary);
}

.pi__later {
  width: 100%;
  margin-top: 10px;
  padding: 8px 0;
  font-size: 13px;
  color: var(--fs-text-3);
  background: none;
  border: none;
  cursor: pointer;
}

@keyframes pi-in {
  from {
    opacity: 0;
    transform: translateY(10px) scale(0.98);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
</style>
