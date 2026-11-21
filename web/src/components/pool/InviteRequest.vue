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
          <!-- 倒计时是本地跑的：服务端给的 expires_in 只是「给这一帧时的剩余秒数」，
               每来一帧都重新算一遍也没有意义，本地倒数再加上一秒的粒度就够了。 -->
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

// startCountdown 按当前这条的剩余秒数重新起表。
//
// 每次都先清掉上一支：切到下一条邀请时若不复位，两支定时器会一起减，
// 倒计时会以两倍速走完。
function startCountdown(seconds: number) {
  stopCountdown()
  remain.value = Math.max(0, seconds)
  if (remain.value === 0) {
    return
  }
  timer = setInterval(() => {
    remain.value -= 1
    if (remain.value <= 0) {
      stopCountdown()
      // 到点就本地收起。服务端此时也已经把它标成过期，
      // 继续留在页面上只会诱使用户点一个必然失败的按钮。
      if (current.value) {
        pool.dismiss(current.value.invite_id)
      }
    }
  }, 1000)
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
    startCountdown(current.value.expires_in)
  },
  { immediate: true },
)

// 对方接受了我的邀请：跳房间页。
//
// 用 replace 而不是 push：大厅 → 房间是一次「往前走」的流程，
// 但用户从房间点返回时应该回到大厅而不是回到那张等待卡，
// 而等待卡此时已经没有意义了（邀请已被接受）。
watch(
  () => pool.acceptedRoom,
  (room) => {
    if (!room) {
      return
    }
    pool.acceptedRoom = null
    void router.replace('/pool/room/' + String(room.match_id))
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

// accept 接受邀请：成功后直接进房间页。
// 失败交给 request 拦截器弹提示，这里只负责把按钮解锁。
async function accept() {
  if (!current.value || busy.value) {
    return
  }
  busy.value = true
  const inviteId = current.value.invite_id
  try {
    const room = await pool.accept(inviteId)
    stopCountdown()
    await router.replace('/pool/room/' + String(room.match_id))
  } catch {
    // 邀请大概率已经过期或被撤销，本地这条也该收起来。
    pool.dismiss(inviteId)
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
