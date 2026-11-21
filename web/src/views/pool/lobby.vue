<template>
  <div class="pl">
    <header class="pl__head">
      <div>
        <h1 class="pl__title">台球对战</h1>
        <p class="pl__sub">中式八球，一局定胜负。选一个你关注的人，等他进房间。</p>
      </div>
    </header>

    <!-- 等待对方接受。这条卡片比列表更重要，放在最上面：
         发出邀请后用户最想知道的就是「他到底看没看到」。 -->
    <section v-if="outgoing" class="pl__waiting">
      <div class="pl__waiting-main">
        <span class="pl__waiting-label">等待对方接受</span>
        <span class="pl__waiting-code">{{ outgoing.room_code }}</span>
      </div>
      <div class="pl__waiting-side">
        <span class="pl__waiting-tip">{{ waitLeft }} 秒后失效</span>
        <button class="pl__cancel" type="button" @click="pool.cancelOutgoing()">先不等了</button>
      </div>
    </section>

    <section class="pl__panel">
      <div class="pl__panel-head">
        <h2 class="pl__panel-title">我关注的人</h2>
        <span v-if="loaded" class="pl__panel-count">{{ total }} 人</span>
      </div>

      <div v-if="loading && items.length === 0" class="pl__skeleton">
        <el-skeleton :rows="4" animated />
      </div>

      <p v-else-if="items.length === 0" class="pl__empty">
        你还没有关注任何人。先去
        <router-link to="/articles" class="pl__link">文章页</router-link>
        看看别人写的东西，关注之后就能约球了。
      </p>

      <ul v-else class="pl__list">
        <li v-for="person in items" :key="person.id" class="pl__row">
          <el-avatar
            :size="40"
            :src="person.avatar_url || undefined"
            :style="{ backgroundColor: avatarColor(person.nickname || person.username) }"
          >
            {{ initialOf(person) }}
          </el-avatar>
          <div class="pl__who">
            <span class="pl__name">{{ person.nickname || person.username }}</span>
            <span class="pl__handle">{{ person.username }}</span>
          </div>
          <!-- 正在邀请的那个人按钮变成「已发送」并禁用。
               不禁用的话连点两下会拿到 409，用户看到一句「你已有一条待接受的邀请」
               却不知道说的是哪一条。 -->
          <button
            class="pl__invite"
            type="button"
            :disabled="busyId === person.id || isWaitingOn(person.id)"
            @click="onInvite(person)"
          >
            {{ isWaitingOn(person.id) ? '已发送' : busyId === person.id ? '发送中…' : '邀请' }}
          </button>
        </li>
      </ul>
    </section>
  </div>
</template>

<script setup lang="ts">
// 台球大厅：从「我关注的人」里挑一个发邀请。
//
// 只列关注的人，不做全站搜索匹配：约球需要一个对方也在场的对话语境，
// 随机匹配要做排队、超时、段位，那是另一套系统。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { listFollows, type FollowItem } from '@/api/user'
import { usePoolStore } from '@/store/modules/pool'
import { useUserStore } from '@/store/modules/user'
import { avatarColor } from '@/utils/avatar'
import { initials } from '@/utils/format'

const pool = usePoolStore()
const user = useUserStore()

const items = ref<FollowItem[]>([])
const total = ref(0)
const loading = ref(false)
const loaded = ref(false)

// busyId 是正在发送邀请的那个人的 id，用来只禁用一行而不是整张列表。
const busyId = ref(0)

// invitedId 记住这次邀请发给谁，用于把那一行按钮显示成「已发送」。
// 后端返回的 invite 响应里没有被邀请者 id，本地记一下最省事。
const invitedId = ref(0)

const outgoing = computed(() => pool.outgoing)

// waitLeft 是等待卡上的剩余秒数，由本地按 expires_in 倒数。
const waitLeft = ref(0)
let waitTimer: ReturnType<typeof setInterval> | null = null

function stopWaitTimer() {
  if (waitTimer) {
    clearInterval(waitTimer)
    waitTimer = null
  }
}

// 每次出现新的等待卡都重新起表。收到 accepted 推送时 outgoing 会被清空，
// 这里跟着停表并把「已发送」标记一起复位。
watch(outgoing, (value) => {
  stopWaitTimer()
  if (!value) {
    invitedId.value = 0
    waitLeft.value = 0
    return
  }
  waitLeft.value = value.expires_in
  waitTimer = setInterval(() => {
    waitLeft.value -= 1
    if (waitLeft.value <= 0) {
      stopWaitTimer()
      pool.cancelOutgoing()
    }
  }, 1000)
})

function isWaitingOn(userId: number): boolean {
  return outgoing.value !== null && invitedId.value === userId
}

function initialOf(person: FollowItem): string {
  const name = person.nickname || person.username
  if (!name) {
    return ''
  }
  return initials(name)
}

// load 拉关注列表的第一页。
//
// 只拉 20 条：大厅是「约一个人」而不是「管理关注关系」，
// 关注上百人的账号也不需要滚动找，直接去个人主页的关注列表看就行。
async function load() {
  if (user.userId === 0) {
    return
  }
  loading.value = true
  try {
    const resp = await listFollows(user.userId, 'following', 1, 20)
    items.value = resp.list
    total.value = resp.total
    loaded.value = true
  } catch {
    // 请求失败已经由拦截器提示过，这里保留空列表，页面自己会显示空状态。
  } finally {
    loading.value = false
  }
}

// onInvite 发邀请。成功就把这一行标成「已发送」，
// 失败（409 已有待接受邀请、400 对方已不在关注列表）交给拦截器提示。
async function onInvite(person: FollowItem) {
  if (busyId.value !== 0) {
    return
  }
  busyId.value = person.id
  try {
    await pool.invite(person.id)
    invitedId.value = person.id
  } catch {
    // 拦截器已提示。
  } finally {
    busyId.value = 0
  }
}

onMounted(() => {
  void load()
  // 进大厅顺手补拉一次待接受邀请：万一推送在页面还没挂载时就到了，这里能捞回来。
  void pool.refresh()
})

onBeforeUnmount(stopWaitTimer)
</script>

<style scoped>
.pl {
  max-width: 720px;
  margin: 0 auto;
}

.pl__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 20px;
}

.pl__title {
  margin: 0 0 6px;
  font-size: 22px;
  font-weight: 700;
  color: var(--fs-text-1);
}

.pl__sub {
  margin: 0;
  font-size: 13px;
  color: var(--fs-text-2);
}

.pl__waiting {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
  padding: 14px 16px;
  background: var(--fs-primary-weak);
  border-radius: var(--fs-radius);
}

.pl__waiting-main {
  display: flex;
  align-items: baseline;
  gap: 10px;
}

.pl__waiting-label {
  font-size: 13px;
  color: var(--fs-text-2);
}

.pl__waiting-code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 20px;
  font-weight: 700;
  letter-spacing: 3px;
  color: var(--fs-primary);
}

.pl__waiting-side {
  display: flex;
  align-items: center;
  gap: 12px;
}

.pl__waiting-tip {
  font-size: 12px;
  color: var(--fs-text-3);
}

.pl__cancel {
  padding: 4px 10px;
  font-size: 12px;
  color: var(--fs-text-2);
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius-sm);
  cursor: pointer;
}

.pl__panel {
  padding: 4px 16px 8px;
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius);
}

.pl__panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 46px;
}

.pl__panel-title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-text-1);
}

.pl__panel-count {
  font-size: 12px;
  color: var(--fs-text-3);
}

.pl__skeleton {
  padding: 4px 0 16px;
}

.pl__empty {
  margin: 0 0 16px;
  font-size: 13px;
  line-height: 1.8;
  color: var(--fs-text-3);
}

.pl__link {
  color: var(--fs-primary);
}

.pl__list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.pl__row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 0;
}

/* 分隔线用相邻兄弟而不是 :not(:last-of-type)：
   列表里全是 li，用伪类要额外算一次「最后一项」，相邻兄弟写法更直白，
   以后中间插别的元素也不会错位。 */
.pl__row + .pl__row {
  border-top: 1px solid var(--fs-line);
}

.pl__who {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  flex: 1;
}

.pl__name {
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-text-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pl__handle {
  font-size: 12px;
  color: var(--fs-text-3);
}

.pl__invite {
  flex: none;
  height: 32px;
  padding: 0 16px;
  font-size: 13px;
  font-weight: 600;
  color: var(--fs-primary);
  background: var(--fs-primary-weak);
  border: none;
  border-radius: var(--fs-radius-sm);
  cursor: pointer;
}

.pl__invite:disabled {
  color: var(--fs-text-3);
  background: var(--fs-surface-hover);
  cursor: default;
}

@media (max-width: 560px) {
  .pl__head {
    flex-direction: column;
    align-items: stretch;
  }
}
</style>
