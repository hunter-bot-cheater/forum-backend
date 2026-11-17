<template>
  <div class="fs-container feed">
    <!-- 主列：文章流 -->
    <div class="feed__main">
      <div class="feed__head">
        <h1 class="feed__title">文章</h1>
        <!-- 搜索态换成「找到 N 篇」：用户此刻关心的是「这个关键词有多少结果」，
             「共 N 篇」会被误读成站内文章总数。 -->
        <span v-if="store.total > 0" class="feed__count">
          {{ store.keyword ? `找到 ${store.total} 篇` : `共 ${store.total} 篇` }}
        </span>

        <!-- 搜索框放在标题行右侧：搜索是列表页的次要动作，
             不该单独占一行把文章流往下推。窄屏时折到下一行并占满宽度。 -->
        <el-input
          v-model="draft"
          class="feed__search"
          placeholder="搜索标题或正文"
          :prefix-icon="Search"
          clearable
          @keyup.enter="submitSearch"
          @clear="clearSearch"
        />
      </div>

      <!-- 标签云已经去掉：标签一多会撑成好几排，把文章流整体往下推。
           每篇文章的标签现在显示在它自己的卡片里（见 ArticleCard），
           点击卡片上的标签同样进这个筛选。

           但筛选态必须可见——列表突然变短、又没有任何提示，
           用户会以为文章丢了。所以只在真的按标签筛着的时候留一条窄提示。 -->
      <div v-if="store.tag" class="feed__filter">
        <span class="feed__filter-label">标签</span>
        <button class="feed__filter-chip" @click="clearTag">
          {{ store.tag }}
          <el-icon class="feed__filter-x"><Close /></el-icon>
        </button>
      </div>

      <!-- 首屏用骨架屏：三张卡片的轮廓，加载完不会有一次高度跳变。
           条件里加了 list 为空：发布文章后列表被标记失效（loaded=false），
           此时列表里还有上一次的内容，不该整块塌成骨架再长回来。
           翻页时也不用骨架，用 v-loading 盖住现有列表即可——
           整页塌掉再重建，滚动位置会跳。 -->
      <div v-if="!store.loaded && store.list.length === 0" class="feed__skeleton">
        <el-card
          v-for="n in 3"
          :key="n"
          shadow="never"
          class="skeleton-card"
          :body-style="{ padding: '18px 20px' }"
        >
          <el-skeleton :rows="2" animated />
        </el-card>
      </div>

      <!-- 空态分三种：搜索没结果、标签下没文章、一篇都没有。
           三种情况该给的按钮不同，用同一个会让人莫名其妙。 -->
      <el-empty
        v-else-if="store.list.length === 0"
        class="fs-card feed__empty"
        :description="emptyText"
        :image-size="96"
      >
        <el-button v-if="store.tag" @click="clearTag">清除标签筛选</el-button>
        <el-button v-else-if="store.keyword" @click="clearSearch">清除搜索</el-button>
        <el-button v-else type="primary" @click="goWrite">写第一篇</el-button>
      </el-empty>

      <div v-else v-loading="store.loading" class="feed__list pin-scroll">
        <ArticleCard v-for="item in store.list" :key="item.id" :item="item" />
      </div>

      <!-- 只有一页时不摆分页器：点了没反应，还会让人以为后面还有内容。 -->
      <div v-if="store.total > store.pageSize" ref="pager" class="feed__pager">
        <el-pagination
          background
          layout="prev, pager, next"
          :current-page="store.page"
          :page-size="store.pageSize"
          :total="store.total"
          @current-change="onPage"
        />
      </div>
    </div>

    <!-- 右侧栏：热榜 + 未读提醒。窄屏整栏收起，正文占满。 -->
    <aside class="feed__rail">
      <HotRankCard :items="hot.list" :loading="hot.loading" :limit="5" />

      <el-card shadow="never" class="rail-card" :body-style="{ padding: '0' }">
        <div class="rail-card__head">消息</div>
        <router-link to="/messages" class="rail-row">
          <span class="rail-row__label">私信</span>
          <span v-if="im.totalUnread > 0" class="rail-row__badge">{{ im.totalUnread }}</span>
          <span v-else class="rail-row__none">暂无未读</span>
        </router-link>
        <router-link to="/notifications" class="rail-row">
          <span class="rail-row__label">通知</span>
          <span v-if="notification.summary.total > 0" class="rail-row__badge">
            {{ notification.summary.total }}
          </span>
          <span v-else class="rail-row__none">暂无未读</span>
        </router-link>
      </el-card>
    </aside>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Close, Search } from '@element-plus/icons-vue'
import ArticleCard from '@/components/ArticleCard.vue'
import HotRankCard from '@/components/HotRankCard.vue'
import { useArticleStore } from '@/store/modules/article'
import { useHotStore } from '@/store/modules/hot'
import { useImStore } from '@/store/modules/im'
import { useNotificationStore } from '@/store/modules/notification'
import { keepInPlace } from '@/utils/scroll'

const route = useRoute()
const router = useRouter()
const store = useArticleStore()
const hot = useHotStore()
const im = useImStore()
const notification = useNotificationStore()

// draft 是输入框里的即时内容，store.keyword 是「已经生效的搜索词」。
// 两者分开：边打字边发请求会让后端每敲一个字扫一次表。
const draft = ref('')

// 分页器节点：翻页前后各测一次它的位置，用来把页面钉在原处（见 onPage）。
const pager = ref<HTMLElement | null>(null)

// 空态文案按「先标签、后搜索」判断：标签是更窄的条件，
// 同时有搜索词和标签时，用户更想知道的是「这个标签下有没有东西」。
const emptyText = computed(() => {
  if (store.tag !== '') {
    return '标签「' + store.tag + '」下还没有文章'
  }
  if (store.keyword !== '') {
    return '没有找到与「' + store.keyword + '」相关的文章'
  }
  return '还没有文章'
})

// 地址栏是筛选态的唯一来源：首次进入、前进后退、粘一条带 ?q= / ?tag= 的链接，
// 都从 query 还原，不另外维护一份「当前筛选」。
// 页面内操作只负责改地址栏（见 submitSearch / selectTag），实际加载统一走这里，
// 只留一条路径就不会出现「点了能筛、按后退不筛」这种不一致。
//
// 两个 query 合并成一个 watch：分开监听的话，带 ?q= 且 ?tag= 的链接进来会触发两次加载。
watch(
  () => [route.query.q, route.query.tag],
  (values) => {
    const q = typeof values[0] === 'string' ? values[0] : ''
    const tag = typeof values[1] === 'string' ? values[1] : ''
    draft.value = q
    if (q.trim() !== store.keyword || tag.trim() !== store.tag) {
      void store.applyFilter({ keyword: q, tag })
      return
    }
    // 筛选没变、但列表还是空的（首次进入）：补一次首屏。
    // 已经加载过就不再拉：从详情页返回时保留原来的页码与滚动位置，
    // 否则每看一篇文章回来都要重新翻一遍。
    if (!store.loaded) {
      void store.load(1)
    }
  },
  { immediate: true },
)

onMounted(() => {
  // 榜单每次进页面都补一次：它按点赞数排，隔一段时间就旧了。
  void hot.ensure(5)
})

// buildQuery 拼地址栏参数：没传的项沿用当前值，空串则从 URL 里去掉。
// 用 undefined 而不是空串：vue-router 会把 undefined 的字段整个删掉，
// 留一个 tag= 在地址栏里会让人以为在按空标签筛选。
function buildQuery(next: { keyword?: string; tag?: string }) {
  const query: Record<string, string> = {}
  const keyword = next.keyword === undefined ? store.keyword : next.keyword
  const tag = next.tag === undefined ? store.tag : next.tag
  if (keyword !== '') {
    query.q = keyword
  }
  if (tag !== '') {
    query.tag = tag
  }
  return query
}

// submitSearch 只把关键词写进地址栏，加载交给上面的 watch。
// 用 replace 而不是 push：连搜三次不该在历史里留下三条记录，
// 否则按一次后退还在搜索页，用户会以为按钮没反应。
function submitSearch() {
  void router.replace({ path: '/articles', query: buildQuery({ keyword: draft.value.trim() }) })
}

// clearSearch 由输入框右侧的清空按钮和空态的「清除搜索」共用。
function clearSearch() {
  draft.value = ''
  void router.replace({ path: '/articles', query: buildQuery({ keyword: '' }) })
}

// clearTag 清掉标签筛选，保留搜索词：用户可能是在一次搜索里点的标签，
// 清标签不该顺带把他搜的词也清了。
//
// 标签入口现在在卡片和详情页上，进了列表就只剩这条筛选提示；
// 用 replace 不留在历史里，取消筛选后按返回不该又回到「筛着的列表」。
function clearTag() {
  void router.replace({ path: '/articles', query: buildQuery({ tag: '' }) })
}

function onPage(next: number) {
  // 翻页不把页面滚回顶部：分页器就在手指下面，跳回顶部等于每翻一页都要重新滑下来。
  // 记录它此刻在视口里的位置，数据回来、DOM 重排完再把它放回去——
  // 这样最后一页只有三条、页面变短时，分页器也不会被内容推着上下跑。
  const restore = keepInPlace(pager.value)
  void store.load(next).then(async () => {
    await nextTick()
    restore()
  })
}

// 空态的「写第一篇」与顶栏按钮走同一个地址，不复制一份跳转逻辑。
function goWrite() {
  void router.push('/write')
}
</script>

<style scoped>
.feed {
  display: grid;
  /* minmax(0, 1fr) 而不是 1fr：1fr 的最小宽度是内容的 min-content，
     遇到不换行的长串会把主列撑宽、把右侧栏挤扁。 */
  grid-template-columns: minmax(0, 1fr) var(--fs-rail-w);
  gap: 20px;
  align-items: start;
}

.feed__head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 14px;
  padding: 0 2px;
}

.feed__title {
  font-size: 22px;
  font-weight: 700;
  color: var(--fs-text-1);
}

.feed__count {
  font-size: 13px;
  color: var(--fs-text-3);
}

/* 筛选提示条：只在按标签筛着的时候出现，固定一行高。
   它不是一个入口（入口在卡片和详情页的标签上），而是「当前列表被筛过」的说明，
   所以做成主色浅底的胶囊 + 一个 ×，点掉就取消筛选。 */
.feed__filter {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  padding: 0 2px;
}

.feed__filter-label {
  flex: none;
  font-size: 12px;
  color: var(--fs-text-3);
}

.feed__filter-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 7px 3px 11px;
  border: 1px solid var(--fs-primary-weak);
  border-radius: 12px;
  background: var(--fs-primary-weak);
  color: var(--fs-primary);
  font-size: 12.5px;
  line-height: 18px;
  cursor: pointer;
  transition: color 0.15s, border-color 0.15s;
}

.feed__filter-chip:hover {
  border-color: var(--fs-primary);
  color: var(--fs-primary-hover);
}

.feed__filter-x {
  font-size: 12px;
}

/* 搜索框往右靠，与标题形成「左标题右工具」的一行。
   宽度写死是为了点进输入框时标题不再跟着重排。
   align-self 单独盖成 center：整行是 baseline 对齐，输入框跟着基线摆
   会比标题低几个像素，看着像没对齐。 */
.feed__search {
  width: 240px;
  margin-left: auto;
  align-self: center;
}

/* 列表的上下间距交给卡片自己的内边距（ArticleCard 的 .ac），
   这里不能再留 gap：两层间距叠起来会变成 24px，
   而且 gap 那 12px 不属于任何卡片，点在缝里点不开文章。 */
.feed__list {
  display: flex;
  flex-direction: column;
}

.feed__skeleton {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.skeleton-card {
  border: none;
  border-radius: var(--fs-radius-lg);
}

.feed__empty {
  padding: 40px 0;
}

.feed__pager {
  display: flex;
  justify-content: center;
  margin-top: 20px;
}

/* 右侧栏跟着滚：文章流很长时榜单要一直看得到。
   顶部偏移要让开吸顶栏，否则滚到一半会被压住。 */
.feed__rail {
  position: sticky;
  top: calc(var(--fs-header-h) + 20px);
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.rail-card {
  border: none;
  border-radius: var(--fs-radius-lg);
}

.rail-card__head {
  padding: 14px 16px 8px;
  font-size: 15px;
  font-weight: 600;
  color: var(--fs-text-1);
}

.rail-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 16px 12px;
  font-size: 13px;
  color: var(--fs-text-2);
  transition: background-color 0.15s;
}

.rail-row:hover {
  background: var(--fs-surface-hover);
}

.rail-row__badge {
  min-width: 20px;
  padding: 0 6px;
  border-radius: 10px;
  background: var(--fs-primary-weak);
  color: var(--fs-primary);
  font-size: 12px;
  font-weight: 600;
  text-align: center;
}

.rail-row__none {
  color: var(--fs-text-3);
}

@media (max-width: 960px) {
  .feed {
    grid-template-columns: minmax(0, 1fr);
  }

  /* 窄屏收起右侧栏而不是堆到列表下面：
     堆下去的话滚到一半会撞见一个「榜单」，读者会以为文章列表结束了。 */
  .feed__rail {
    display: none;
  }
}

@media (max-width: 640px) {
  /* 手机上一行放不下「标题 + 计数 + 搜索框」：
     让搜索框折到第二行并占满宽度，比把它挤成 120px 更好点。 */
  .feed__head {
    flex-wrap: wrap;
    align-items: center;
    row-gap: 10px;
  }

  .feed__search {
    order: 3;
    width: 100%;
    margin-left: 0;
  }
}
</style>
