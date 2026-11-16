<template>
  <div class="fs-container feed">
    <!-- 主列：文章流 -->
    <div class="feed__main">
      <div class="feed__head">
        <h1 class="feed__title">文章</h1>
        <span v-if="store.total > 0" class="feed__count">共 {{ store.total }} 篇</span>
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

      <!-- 空态给一个「写第一篇」的按钮：整站只有顶栏一个写文章入口，
           列表全空时读者更可能在找「怎么开始」，这里是最自然的落点。 -->
      <el-empty
        v-else-if="store.list.length === 0"
        class="fs-card feed__empty"
        description="还没有文章"
        :image-size="96"
      >
        <el-button type="primary" @click="goWrite">写第一篇</el-button>
      </el-empty>

      <div v-else v-loading="store.loading" class="feed__list">
        <ArticleCard v-for="item in store.list" :key="item.id" :item="item" />
      </div>

      <!-- 只有一页时不摆分页器：点了没反应，还会让人以为后面还有内容。 -->
      <div v-if="store.total > store.pageSize" class="feed__pager">
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
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import ArticleCard from '@/components/ArticleCard.vue'
import HotRankCard from '@/components/HotRankCard.vue'
import { useArticleStore } from '@/store/modules/article'
import { useHotStore } from '@/store/modules/hot'
import { useImStore } from '@/store/modules/im'
import { useNotificationStore } from '@/store/modules/notification'

const router = useRouter()
const store = useArticleStore()
const hot = useHotStore()
const im = useImStore()
const notification = useNotificationStore()

onMounted(() => {
  // 已经加载过就不再拉第一页：从详情页返回时保留原来的页码与滚动位置，
  // 否则每看一篇文章回来都要重新翻一遍。
  if (!store.loaded) {
    void store.load(1)
  }
  // 榜单每次进页面都补一次：它按点赞数排，隔一段时间就旧了。
  void hot.ensure(5)
})

function onPage(next: number) {
  void store.load(next)
  scrollToTop()
}

// 空态的「写第一篇」与顶栏按钮走同一个地址，不复制一份跳转逻辑。
function goWrite() {
  void router.push('/write')
}

// 翻页后回到顶部：一页十篇时当前视口可能已经滚到列表中部，
// 不回去会让读者从第三条开始看，误以为列表没换。
// 新版外壳下滚动容器是 body（主区域不自己滚），所以直接滚 window 就够。
function scrollToTop() {
  window.scrollTo({ top: 0, behavior: 'auto' })
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

.feed__list,
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
</style>
