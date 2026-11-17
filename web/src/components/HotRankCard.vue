<template>
  <el-card shadow="never" class="hr" :body-style="{ padding: '0' }">
    <div class="hr__head">
      <span class="hr__title">{{ title }}</span>
    </div>

    <!-- 加载态用骨架屏而不是转圈：骨架的行高与真实条目一致，
         加载完不会有一次高度跳变。 -->
    <div v-if="loading && items.length === 0" class="hr__skeleton">
      <el-skeleton :rows="3" animated />
    </div>

    <div v-else-if="items.length === 0" class="hr__empty">榜单还是空的</div>

    <ol v-else class="hr__list">
      <li v-for="(item, index) in shown" :key="item.article_id" class="hr__item">
        <router-link :to="'/articles/' + String(item.article_id)" class="hr__link">
          <span class="hr__rank" :class="{ 'is-top': index < 3 }">{{ index + 1 }}</span>
          <span class="hr__text">{{ item.title }}</span>
          <span class="hr__like">
            <span class="hr__heart" :class="{ 'is-liked': item.liked }">♥</span>
            {{ item.like_count }}
          </span>
        </router-link>
      </li>
    </ol>

    <router-link v-if="more" to="/hot" class="hr__more">查看完整榜单</router-link>
  </el-card>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { HotArticle } from '@/api/hot'

// 热榜单卡片。右侧栏放 5 条、热榜页放 10 条，同一份组件只改 limit。
//
// 做成哑组件（数据由 props 传进来）而不是自己读 store：
// 热榜页要显示加载失败后的降级内容、右侧栏只要前 5 条，
// 组件内部读 store 会让「谁在什么时候拉数据」散在组件里，不好排查。
const props = withDefaults(
  defineProps<{
    items: HotArticle[]
    loading?: boolean
    limit?: number
    title?: string
  }>(),
  { loading: false, limit: 5, title: '热榜' },
)

const shown = computed(() => props.items.slice(0, props.limit))

// 榜单条数多于当前展示时才给「查看完整榜单」：
// 右侧栏只有 5 条却给一个「看全部」的入口，点进热榜页还是这 5 条，会让人以为没生效。
const more = computed(() => props.items.length > props.limit)
</script>

<style scoped>
.hr {
  border: none;
  border-radius: var(--fs-radius-lg);
}

.hr__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px 10px;
}

.hr__title {
  font-size: 15px;
  font-weight: 600;
  color: var(--fs-text-1);
}

.hr__skeleton {
  padding: 4px 16px 16px;
}

.hr__empty {
  padding: 4px 16px 18px;
  font-size: 13px;
  color: var(--fs-text-3);
}

.hr__list {
  margin: 0;
  padding: 0 8px 6px;
  list-style: none;
}

/* 每行 hover 加一层浅底：榜单条目之间的分割靠间距，
   但点击区域要明确，否则不知道哪一行能点。
   上下给到 9px：13px 字号的行高只有 31px，鼠标扫过去容易点到缝里。 */
.hr__link {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 8px;
  border-radius: var(--fs-radius-sm);
  transition: background-color 0.15s;
}

.hr__link:hover {
  background: var(--fs-surface-hover);
}

.hr__rank {
  flex: none;
  width: 18px;
  font-size: 13px;
  font-weight: 600;
  text-align: center;
  color: var(--fs-text-3);
}

/* 前三名用强调色：榜单的名次本身就是信息，
   全用一个灰度色的话前五名看起来一样重要。 */
.hr__rank.is-top {
  color: #f5552d;
}

/* 单行省略：侧栏宽度固定，标题换行会把整张卡片撑高。 */
.hr__text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  color: var(--fs-text-2);
}

.hr__link:hover .hr__text {
  color: var(--fs-text-1);
}

.hr__like {
  flex: none;
  font-size: 12px;
  color: var(--fs-text-3);
}

/* 只有当前访问者赞过才点亮。判据是 item.liked（后端按 token 算的），
   不是 like_count——「有多少人赞过」和「我赞没赞」是两个不同的问题，
   混用会让游客看到满屏红心。 */
.hr__heart {
  margin-right: 2px;
  color: var(--fs-text-3);
}

.hr__heart.is-liked {
  color: #f5552d;
}

.hr__more {
  display: block;
  padding: 10px 16px 14px;
  border-top: 1px solid var(--fs-line-soft);
  font-size: 13px;
  color: var(--fs-text-3);
  text-align: center;
}

.hr__more:hover {
  color: var(--fs-primary);
}
</style>
