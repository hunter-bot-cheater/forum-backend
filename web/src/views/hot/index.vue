<template>
  <div class="fs-container hot">
    <div class="hot__head">
      <h1 class="hot__title">热榜</h1>
    </div>

    <!-- 整张榜单装进一张卡片：条目之间用细分隔线，而不是每条一张卡。
         榜单是「同一件事的排序」，拆成十张卡会让人以为是十条独立内容。 -->
    <el-card shadow="never" class="hot__card" :body-style="{ padding: '4px 0' }">
      <div v-if="!store.loading && store.list.length === 0" class="hot__empty">
        <el-empty description="还没有文章被打开或点赞，榜单是空的" :image-size="88" />
      </div>

      <el-skeleton v-else-if="store.list.length === 0" class="hot__skeleton" :rows="5" animated />

      <router-link
        v-for="(item, index) in store.list"
        :key="item.article_id"
        :to="'/articles/' + String(item.article_id)"
        class="hot-row"
      >
        <span class="hot-row__rank" :class="{ 'is-top': index < 3 }">{{ index + 1 }}</span>
        <span class="hot-row__title">{{ item.title }}</span>
        <span class="hot-row__view">{{ item.view_count }} 浏览</span>
        <span class="hot-row__like">
          <span class="hot-row__heart">♥</span>
          {{ item.like_count }}
        </span>
      </router-link>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { useHotStore } from '@/store/modules/hot'

const store = useHotStore()

// 榜单条数固定 10：一屏能看完，再多就得滚动，
// 而热榜的意义正是「扫一眼知道现在什么最热」。
const TOP_N = 10

onMounted(() => {
  // 从列表页右侧栏过来时缓存里只有 5 条，ensure(10) 会补拉一次；
  // 直接进热榜页则是一次完整拉取。
  void store.ensure(TOP_N)
})
</script>

<style scoped>
.hot {
  max-width: 820px;
}

/* 只剩标题，不再需要 flex 对齐；padding 与 h1 的全局 margin: 0 配合，
   让标题左边缘和下面卡片的文字起点对齐。 */
.hot__head {
  margin-bottom: 14px;
  padding: 0 2px;
}

.hot__title {
  font-size: 22px;
  font-weight: 700;
  color: var(--fs-text-1);
}

.hot__card {
  border: none;
  border-radius: var(--fs-radius-lg);
}

.hot__empty {
  padding: 24px 0;
}

.hot__skeleton {
  padding: 16px 20px;
}

.hot-row {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 14px 20px;
  transition: background-color 0.15s;
}

/* 用分隔线而不是给每行加边框：:not(:last-child) 保证最后一行不会多出一条
   与卡片圆角贴在一起的线。 */
.hot-row:not(:last-child) {
  border-bottom: 1px solid var(--fs-line-soft);
}

.hot-row:hover {
  background: var(--fs-surface-hover);
}

.hot-row__rank {
  flex: none;
  width: 24px;
  font-size: 15px;
  font-weight: 600;
  text-align: center;
  color: var(--fs-text-3);
}

/* 前三名换暖色加粗：名次是这一页的主要信息，
   全用一个灰度色的话前十名看起来一样重要。 */
.hot-row__rank.is-top {
  font-size: 17px;
  font-weight: 700;
  color: #f5552d;
}

.hot-row__title {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 15px;
  color: var(--fs-text-1);
}

.hot-row:hover .hot-row__title {
  color: var(--fs-primary);
}

/* 两个计数各自定宽并右对齐：浏览数会从 1 位涨到 5 位，
   不定宽的话每一行的「♥ N」会被前面的数字推着左右漂，
   一列数字看起来歪歪扭扭。 */
.hot-row__view {
  flex: none;
  min-width: 58px;
  text-align: right;
  font-size: 13px;
  color: var(--fs-text-3);
}

.hot-row__like {
  flex: none;
  min-width: 42px;
  text-align: right;
  font-size: 13px;
  color: var(--fs-text-3);
}

.hot-row__heart {
  margin-right: 2px;
  color: #f5552d;
}

@media (max-width: 720px) {
  .hot-row {
    padding: 12px 14px;
    gap: 10px;
  }
}
</style>
