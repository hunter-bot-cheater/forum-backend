<template>
  <!-- 整卡可点：用 router-link 包一层而不是 div + @click，
       这样中键新开标签页、右键复制链接、Tab 键聚焦都能用，浏览器原生行为不用自己补。 -->
  <router-link :to="'/articles/' + String(item.id)" class="ac">
    <el-card shadow="hover" class="ac__card" :body-style="{ padding: '18px 20px' }">
      <h3 class="ac__title">{{ item.title }}</h3>
      <!-- 摘要是服务端截好的，可能为空（正文只有一两行或全是空白）。
           空的时候整段不渲染，不用留一块只有间距的空白。 -->
      <p v-if="item.excerpt" class="ac__excerpt">{{ item.excerpt }}</p>

      <div class="ac__meta">
        <el-avatar :size="24" :src="authorAvatar" class="ac__avatar">
          {{ authorInitial }}
        </el-avatar>
        <span class="ac__name">{{ authorName }}</span>
        <span class="ac__dot">·</span>
        <span>{{ formatRelativeTime(item.created_at) }}</span>
      </div>
    </el-card>
  </router-link>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { ArticleListItem } from '@/api/article'
import { formatRelativeTime, initials } from '@/utils/format'

// 列表里的一篇文章。只负责展示，取数与分页交给 article store：
// 卡片不该知道「第几页」「怎么翻页」，它只认一条数据。
const props = defineProps<{ item: ArticleListItem }>()

// nameOf 取展示名，优先 nickname（服务端已做过空昵称回退用户名）。
// author 整个字段缺失时显示「匿名」，否则作者那一行会像渲染失败。
const authorName = computed(() => {
  if (props.item.author && props.item.author.nickname) {
    return props.item.author.nickname
  }
  return '匿名'
})

// 空头像返回 undefined 而不是空串：el-avatar 拿到空串会渲染 src="" 的 img
// （浏览器显示成破图），传 undefined 才走插槽兜底。
const authorAvatar = computed(() => {
  if (props.item.author && props.item.author.avatar_url) {
    return props.item.author.avatar_url
  }
  return undefined
})

const authorInitial = computed(() => initials(authorName.value))
</script>

<style scoped>
.ac {
  display: block;
}

/* el-card 默认带 1px 边框和 4px 圆角，压掉边框改用间距分段，
   圆角跟着全局基座走，否则它和右侧栏卡片会是两个弧度。 */
.ac__card {
  border: none;
  border-radius: var(--fs-radius-lg);
}

.ac__title {
  font-size: 16px;
  font-weight: 600;
  line-height: 1.5;
  color: var(--fs-text-1);
  transition: color 0.15s;
  /* 标题最多两行：帖子标题经常很长，放开会让卡片高度差出一倍，
     滚动时列表看着忽高忽低。 */
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
}

.ac:hover .ac__title {
  color: var(--fs-primary);
}

/* 摘要固定两行，理由同上：卡片高度要稳定。 */
.ac__excerpt {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
  margin-top: 8px;
  font-size: 14px;
  line-height: 1.7;
  color: var(--fs-text-2);
}

.ac__meta {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 14px;
  font-size: 13px;
  color: var(--fs-text-3);
}

.ac__avatar {
  flex: none;
}

/* 昵称过长时截断，不让它把时间挤到下一行。 */
.ac__name {
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--fs-text-2);
}

.ac__dot {
  color: var(--fs-text-3);
}
</style>
