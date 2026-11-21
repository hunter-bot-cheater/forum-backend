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
        <span v-if="items.length > 0" class="pl__panel-count">{{ total }} 人</span>
      </div>

      <div v-if="loading && items.length === 0" class="pl__skeleton">
        <el-skeleton :rows="4" animated />
      </div>

      <!-- 拉失败与「真的没关注人」必须分开说。
           只看 items 是否为空的话，请求失败时会写「你还没有关注任何人」，
           用户明明关注了一堆人，看到这句只会去查自己是不是点错了。 -->
      <p v-else-if="loadFailed" class="pl__empty">
        关注列表没加载出来。<button class="pl__retry" type="button" @click="load">重试</button>
      </p>

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
// loadFailed 表示关注列表这一次请求失败了。
// 与 items.length === 0 分开存：空列表是「关注了 0 个人」，失败是「不知道有几个人」。
const loadFailed = ref(false)

// busyId 是正在发送邀请的那个人的 id，用来只禁用一行而不是整张列表。
const busyId = ref(0)

const outgoing = computed(() => pool.outgoing)

// waitLeft 是等待卡上的剩余秒数，按 store 里记下的本地截止时刻倒数。
const waitLeft = ref(0)
let waitTimer: ReturnType<typeof setInterval> | null = null

function stopWaitTimer() {
  if (waitTimer) {
    clearInterval(waitTimer)
    waitTimer = null
  }
}

// tick 重算剩余秒数。
//
// 不从 expires_in 逐秒减：从别的页面回到大厅时这张卡可能已经挂了几十秒，
// 按剩余量重新起表会显示「还有 60 秒」而服务端那边早就过期了。
function tick() {
  const invite = outgoing.value
  if (!invite) {
    stopWaitTimer()
    waitLeft.value = 0
    return
  }
  const left = Math.max(0, Math.ceil((invite.expires_at - Date.now()) / 1000))
  waitLeft.value = left
  if (left === 0) {
    stopWaitTimer()
    pool.cancelOutgoing()
  }
}

// 关注列表要等 userId 落地才拉得到，所以在 watch 里等它。
//
// immediate 不能省：profile 是异步拉的，进页面那一刻 userId 还是 0，
// 不标 immediate 时这条 watch 永远不会被叫到（挂载时它还没变过），
// 大厅会一直显示「你还没有关注任何人」——哪怕关注了几十个人。
watch(
  () => user.userId,
  (id) => {
    if (id !== 0) {
      void load()
    }
  },
  { immediate: true },
)

// 等待卡出现与消失都走这一条：出现就起表，消失（对方接受/拒绝、自己点了不等）就停表。
//
// immediate 不能省：从对局页「再约一局」回到大厅时，outgoing 在挂载前就已经有值，
// 不标 immediate 会停在「0 秒后失效」且不再倒数，看起来像一条已经失效的邀请。
watch(
  outgoing,
  (value) => {
    stopWaitTimer()
    if (!value) {
      waitLeft.value = 0
      return
    }
    tick()
    waitTimer = setInterval(tick, 1000)
  },
  { immediate: true },
)

function isWaitingOn(userId: number): boolean {
  return outgoing.value !== null && pool.outgoingTo === userId
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
  // 资料没到位就先补一次：关注列表的地址要用自己的 id 拼，
  // 首屏 fetchMe 若被限流打过，userId 会一直是 0。
  // 不补的话「重试」按钮点了没有任何反应——比不显示按钮更让人困惑。
  if (user.userId === 0) {
    await user.fetchMe()
  }
  if (user.userId === 0) {
    loadFailed.value = true
    return
  }
  loading.value = true
  loadFailed.value = false
  try {
    const resp = await listFollows(user.userId, 'following', 1, 20)
    items.value = resp.list
    total.value = resp.total
  } catch (err) {
    // 撞上限流时自动再试一次。
    //
    // 大厅是首屏一次发出六七个请求的页面，最后那两条最容易被令牌桶拒掉
    // （见 middleware.RateLimit：容量 10、每秒补 2 个）。这里等一会儿再要一次，
    // 比直接让用户点「重试」体验好，也不需要把桶调大——桶调大等于给整站放水。
    if (statusOf(err) === 429) {
      await sleep(1200)
      try {
        const retry = await listFollows(user.userId, 'following', 1, 20)
        items.value = retry.list
        total.value = retry.total
        return
      } catch {
        // 还是失败，落到下面的失败态。
      }
    }
    // 拦截器已经弹过提示，这里只记下失败：页面要显示重试入口而不是空状态。
    loadFailed.value = true
  } finally {
    loading.value = false
  }
}

// sleep 只给上面那一次限流退避用，不抽到 utils：
// 全站只有这一处需要「等一秒再重试」的语义，抽出去反而要维护一个通用工具。
function sleep(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

// statusOf 从 axios 错误里取 HTTP 状态码，取不到返回 0。
function statusOf(err: unknown): number {
  const resp = (err as { response?: { status?: number } })?.response
  if (resp && typeof resp.status === 'number') {
    return resp.status
  }
  return 0
}

// onInvite 发邀请。成功就由 store 记住「发给谁」，
// 那一行据此显示成「已发送」；
// 失败（409 已有待接受邀请、400 对方已不在关注列表）交给拦截器提示。
async function onInvite(person: FollowItem) {
  if (busyId.value !== 0) {
    return
  }
  busyId.value = person.id
  try {
    await pool.invite(person.id)
  } catch {
    // 拦截器已提示。
  } finally {
    busyId.value = 0
  }
}

onMounted(() => {
  // 进大厅顺手补拉一次待接受邀请：万一推送在页面还没挂载时就到了，这里能捞回来。
  // 关注列表不在这里拉，交给上面的 watch：它要等 userId 到位。
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

.pl__retry {
  padding: 0;
  font-size: 13px;
  color: var(--fs-primary);
  background: none;
  border: none;
  cursor: pointer;
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
