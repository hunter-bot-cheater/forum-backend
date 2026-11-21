<template>
  <div class="rec">
    <header class="rec__head">
      <button class="rec__back" type="button" aria-label="返回" @click="back">
        <el-icon :size="18"><ArrowLeft /></el-icon>
      </button>
      <div>
        <h1 class="rec__title">我的战绩</h1>
        <p class="rec__sub">中式八球，一局定胜负。点开任意一局可以看回放。</p>
      </div>
    </header>

    <!-- 汇总条。三个数字是这一页最该被一眼看到的东西，
         所以做成一块独立面板，而不是塞进列表的表头。 -->
    <section class="rec__summary">
      <div class="rec__stat">
        <span class="rec__stat-label">积分</span>
        <span class="rec__stat-value">{{ eloText }}</span>
      </div>
      <div class="rec__stat">
        <span class="rec__stat-label">胜 / 负</span>
        <span class="rec__stat-value">{{ recordText }}</span>
      </div>
      <div class="rec__stat">
        <span class="rec__stat-label">胜率</span>
        <span class="rec__stat-value">{{ winRateText }}</span>
      </div>
    </section>

    <section class="rec__panel">
      <div v-if="loading && items.length === 0" class="rec__skeleton">
        <el-skeleton :rows="5" animated />
      </div>

      <!-- 拉失败与「真的没打过」必须分开：只看列表空，会把请求失败
           写成人没有战绩，用户只会以为自己白打了。 -->
      <p v-else-if="loadFailed" class="rec__empty">
        战绩没加载出来。<button class="rec__retry" type="button" @click="load">重试</button>
      </p>

      <p v-else-if="items.length === 0" class="rec__empty">
        还没有打完的对局。去
        <router-link to="/pool" class="rec__link">大厅</router-link>
        约一个关注的人打一局。
      </p>

      <ul v-else class="rec__list">
        <li v-for="item in items" :key="item.match_id" class="rec__row">
          <el-avatar
            :size="40"
            :src="item.opponent.avatar || undefined"
            :style="{ backgroundColor: avatarColor(item.opponent.nickname || '对手') }"
          >
            {{ initialOf(item) }}
          </el-avatar>

          <div class="rec__who">
            <span class="rec__name">{{ item.opponent.nickname || '对手' }}</span>
            <span class="rec__meta">
              {{ reasonShort(item.win_reason, item.won) }} · {{ item.shot_count }} 杆
            </span>
          </div>

          <div class="rec__result">
            <span class="rec__flag" :class="item.won ? 'is-win' : 'is-lose'">
              {{ item.won ? '胜' : '负' }}
            </span>
            <span class="rec__time">{{ timeOf(item) }}</span>
          </div>

          <router-link class="rec__replay" :to="'/pool/replay/' + String(item.match_id)">
            回放
          </router-link>
        </li>
      </ul>

      <!-- 分页器只在多于一页时出现：只有三条记录还摆一个「1」很占地方。 -->
      <div v-if="total > size" ref="pager" class="rec__pager">
        <el-pagination
          layout="prev, pager, next"
          :current-page="page"
          :page-size="size"
          :total="total"
          background
          small
          @current-change="onPage"
        />
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
// 战绩页：积分、胜负、最近的对局，每条可以点进回放。
//
// 数据全部读后端（对局结束就落库了），所以刷新页面、换设备都在；
// 对局结束后内存里的 actor 会被回收，战绩页拿不到内存状态也不需要。
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowLeft } from '@element-plus/icons-vue'
import { getRecords, type PoolRating, type PoolRecord } from '@/api/pool'
import { reasonShort } from '@/game/reasons'
import { avatarColor } from '@/utils/avatar'
import { initials, formatListTime } from '@/utils/format'
import { goBack } from '@/utils/nav'
import { keepInPlace } from '@/utils/scroll'

const router = useRouter()

const items = ref<PoolRecord[]>([])
// rating 为 null 表示还没拉到（首次加载中或失败）。
// 不给一个「1000 / 0 胜 0 负」的默认值：拉失败时那三个数字会显示成
// 真实数据，与下面那句「战绩没加载出来」互相打架，还容易让人以为分被清零了。
const rating = ref<PoolRating | null>(null)
const total = ref(0)
const page = ref(1)
const size = 10
const loading = ref(false)
// loadFailed 表示这一次请求失败了，与「真的没有战绩」分开。
const loadFailed = ref(false)

// pager 指到分页器本身，翻页时把它钉在原地。
const pager = ref<HTMLElement | null>(null)

// loadSeq 是请求序号。连点两页时分页控件不会禁用，
// 先发的请求可能后到，用旧页的数据覆盖掉新页——翻到第 3 页却显示第 2 页的内容。
let loadSeq = 0

// placeholder 是「这个数字还没有」的统一写法。用短横而不是 0：
// 0 分看起来像真的输光了，短横一眼就知道是还没拉到。
const placeholder = '—'

// eloText / recordText / winRateText 都从 rating 派生。
// 胜率一场没打时也显示短横：0% 看起来像「打了全输」。
const eloText = computed(() => {
  if (!rating.value) {
    return placeholder
  }
  return String(rating.value.elo)
})

const recordText = computed(() => {
  if (!rating.value) {
    return placeholder + ' / ' + placeholder
  }
  return String(rating.value.wins) + ' / ' + String(rating.value.losses)
})

const winRateText = computed(() => {
  if (!rating.value) {
    return placeholder
  }
  const played = rating.value.wins + rating.value.losses
  if (played === 0) {
    return placeholder
  }
  const rate = (rating.value.wins / played) * 100
  return String(Math.round(rate)) + '%'
})

function initialOf(item: PoolRecord): string {
  const name = item.opponent.nickname
  if (!name) {
    return ''
  }
  return initials(name)
}

// timeOf 取结束时间的「日期 时:分」。
// 没有结束时间的老数据显示一个短横，不用当前时间顶上。
function timeOf(item: PoolRecord): string {
  if (!item.ended_at) {
    return '—'
  }
  return formatListTime(item.ended_at)
}

// load 拉当前这一页。
async function load() {
  loadSeq += 1
  const seq = loadSeq
  loading.value = true
  loadFailed.value = false
  try {
    const resp = await getRecords(page.value, size)
    // 期间又发了一次请求：这次的结果已经过时，直接丢掉，
    // 否则慢的那条后到，会把新页的内容盖回旧页。
    if (seq !== loadSeq) {
      return
    }
    items.value = resp.list
    rating.value = resp.rating
    total.value = resp.total
  } catch {
    if (seq !== loadSeq) {
      return
    }
    // 拦截器已经弹过提示，这里只负责把失败态显示出来。
    loadFailed.value = true
  } finally {
    if (seq === loadSeq) {
      loading.value = false
    }
  }
}

// onPage 翻页。先记住分页器的位置，渲染完再放回去，
// 否则条目数一变分页器就会跳走，用户没法连着点。
async function onPage(next: number) {
  const restore = keepInPlace(pager.value)
  page.value = next
  await load()
  await nextTick()
  restore()
}

// back 回上一页（一般是大厅）。走 goBack 是为了避免来回 push 把历史压满。
function back() {
  goBack(router, '/pool')
}

onMounted(load)
</script>

<style scoped>
.rec {
  max-width: 720px;
  margin: 0 auto;
}

.rec__head {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-bottom: 16px;
}

.rec__back {
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  margin-top: 2px;
  padding: 0;
  color: var(--fs-text-2);
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: 8px;
  cursor: pointer;
}

.rec__title {
  margin: 0 0 6px;
  font-size: 22px;
  font-weight: 700;
  color: var(--fs-text-1);
}

.rec__sub {
  margin: 0;
  font-size: 13px;
  color: var(--fs-text-2);
}

.rec__summary {
  display: flex;
  gap: 10px;
  margin-bottom: 14px;
}

.rec__stat {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 4px;
  padding: 12px 14px;
  background: var(--fs-primary-weak);
  border-radius: var(--fs-radius);
}

.rec__stat-label {
  font-size: 12px;
  color: var(--fs-text-3);
}

.rec__stat-value {
  font-size: 20px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  color: var(--fs-text-1);
}

.rec__panel {
  padding: 4px 16px 8px;
  background: var(--fs-surface);
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius);
}

.rec__skeleton {
  padding: 8px 0 16px;
}

.rec__empty {
  margin: 16px 0;
  font-size: 13px;
  line-height: 1.8;
  color: var(--fs-text-3);
}

.rec__link {
  color: var(--fs-primary);
}

.rec__retry {
  padding: 0;
  font-size: 13px;
  color: var(--fs-primary);
  background: none;
  border: none;
  cursor: pointer;
}

.rec__list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.rec__row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 0;
}

/* 分隔线用相邻兄弟：卡片里没有别的列表项，用 :not(:last-of-type)
   在以后插入分页块、提示条时会算错。 */
.rec__row + .rec__row {
  border-top: 1px solid var(--fs-line);
}

.rec__who {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.rec__name {
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-text-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rec__meta {
  font-size: 12px;
  color: var(--fs-text-3);
}

.rec__result {
  display: flex;
  flex: none;
  flex-direction: column;
  align-items: flex-end;
  gap: 2px;
}

/* 胜用红、负用绿：国内习惯，与股市一致，别按欧美的红绿反着来。 */
.rec__flag {
  min-width: 26px;
  padding: 1px 6px;
  font-size: 12px;
  font-weight: 700;
  text-align: center;
  border-radius: 6px;
}

.rec__flag.is-win {
  color: var(--fs-win);
  background: var(--fs-win-weak);
}

.rec__flag.is-lose {
  color: var(--fs-lose);
  background: var(--fs-lose-weak);
}

.rec__time {
  font-size: 11px;
  color: var(--fs-text-3);
  font-variant-numeric: tabular-nums;
}

.rec__replay {
  flex: none;
  padding: 5px 10px;
  font-size: 12px;
  color: var(--fs-primary);
  text-decoration: none;
  background: var(--fs-primary-weak);
  border-radius: var(--fs-radius-sm);
}

.rec__pager {
  display: flex;
  justify-content: center;
  padding: 10px 0 6px;
}

@media (max-width: 560px) {
  .rec__summary {
    flex-wrap: wrap;
  }

  .rec__stat {
    min-width: 30%;
  }
}
</style>
