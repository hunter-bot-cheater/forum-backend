<template>
  <!-- 整卡可点，但「作者那一组」要能单独进主页：
       外层不能再是 router-link——a 里套 a 是非法嵌套，浏览器会把标签拆开，
       内层链接的点击区域就散了。改成 div + 点击跳转，
       标题仍然是一根真链接，中键新开、右键复制链接、Tab 聚焦都还能用。 -->
  <div class="ac" @click="goDetail">
    <el-card shadow="hover" class="ac__card" :body-style="{ padding: '18px 20px' }">
      <!-- 标题与标签同一行：标签是「这篇属于什么」的速览信息，
           放在标题右侧能让人扫列表时一眼看到分类，不用把视线移到卡片底部。
           标题先占位，标签排在它右边；窄屏放不下时整条标签换到下一行。 -->
      <div class="ac__head">
        <h3 class="ac__title">
          <!-- 链接自己 stop：同一次点击如果继续冒泡，
               外层会再跳一次详情，路由里出现两次一样的导航。 -->
          <router-link :to="detailHref" class="ac__title-link" @click.stop>
            {{ item.title }}
          </router-link>
        </h3>

        <!-- 点标签进「同标签」的筛选列表。
             @click.stop 必须留着——外层整卡都在跳文章，不拦的话点标签会同时进文章。 -->
        <div v-if="item.tags.length > 0" class="ac__tags">
          <router-link
            v-for="name in item.tags"
            :key="name"
            :to="{ path: '/articles', query: { tag: name } }"
            class="ac__tag"
            @click.stop
          >
            {{ name }}
          </router-link>
        </div>
      </div>
      <!-- 摘要是服务端截好的，可能为空（正文只有一两行或全是空白）。
           空的时候整段不渲染，不用留一块只有间距的空白。 -->
      <p v-if="item.excerpt" class="ac__excerpt">{{ item.excerpt }}</p>

      <div class="ac__meta">
        <!-- 头像和昵称包成一组来点：只有名字那几个字可点时，
             鼠标稍微偏一点就落到卡片上，变成了「进文章」而不是「进主页」。
             author 缺失（账号注销）时退化成 span，不生成 /users/undefined。 -->
        <component
          :is="authorId > 0 ? RouterLink : 'span'"
          v-bind="authorId > 0 ? { to: '/users/' + String(authorId) } : {}"
          class="ac__author"
          @click.stop
        >
          <el-avatar
            :size="24"
            :src="authorAvatar"
            :style="{ backgroundColor: avatarColor(authorName) }"
            class="ac__avatar"
          >
            {{ authorInitial }}
          </el-avatar>
          <span class="ac__name">{{ authorName }}</span>
        </component>
        <span class="ac__dot">·</span>
        <span class="ac__time">{{ formatRelativeTime(item.created_at) }}</span>
        <span class="ac__dot">·</span>
        <!-- 阅读数放最后：它是三个信息里最不受关注的，
             放在昵称和时间后面，窄屏挤掉时也不会影响「谁发的、什么时候」。 -->
        <span class="ac__views">{{ formatCount(item.view_count) }} 阅读</span>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import type { ArticleListItem } from '@/api/article'
import { avatarColor } from '@/utils/avatar'
import { formatCount, formatRelativeTime, initials } from '@/utils/format'

// 列表里的一篇文章。只负责展示，取数与分页交给 article store：
// 卡片不该知道「第几页」「怎么翻页」，它只认一条数据。
const props = defineProps<{ item: ArticleListItem }>()

const router = useRouter()

const detailHref = computed(() => '/articles/' + String(props.item.id))

// authorId 为 0 表示拿不到作者（列表里 author 整个字段缺失）。
// 用它同时控制「能不能点」和「拼不拼链接」，避免两处判断走岔。
const authorId = computed(() => {
  if (props.item.author && props.item.author.id > 0) {
    return props.item.author.id
  }
  return 0
})

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

// goDetail 处理卡片空白处的点击。内层链接都已经 stop，
// 能冒泡到这里的一定不是链接，直接跳文章即可。
function goDetail() {
  void router.push(detailHref.value)
}
</script>

<style scoped>
.ac {
  display: block;
  /* 上下各 6px 的透明热区。列表里卡片之间原本靠容器 gap: 12px 留白，
     那段空白不属于任何一张卡，点在缝里没有任何反应；
     改成 gap: 0 + 这条内边距之后，缝的上半段归上面那张、下半段归下面那张，
     点空白也能进文章，hover 高亮和实际落点也终于是同一张卡。 */
  padding: 6px 0;
  /* 外层不再是 a 标签，手型光标要自己给，否则空白处看起来不可点。 */
  cursor: pointer;
}

/* 首尾各抵消一次内边距：不加的话列表整体比原来高 12px，
   标题与第一张卡片、最后一张与分页的间距都会被撑开。 */
.ac:first-child {
  margin-top: -6px;
}

.ac:last-child {
  margin-bottom: -6px;
}

/* 鼠标落在上下的透明热区里时，命中的是 .ac 而不是里面的 el-card，
   Element 的 shadow="hover" 只认 el-card 自己的 :hover，阴影不会亮。
   这里补一份同样的阴影，保证「亮起来的那张」就是「会被点开的那张」。 */
.ac:hover :deep(.el-card) {
  box-shadow: 0 0 12px rgba(0, 0, 0, 0.12);
}

/* el-card 默认带 1px 边框和 4px 圆角，压掉边框改用间距分段，
   圆角跟着全局基座走，否则它和右侧栏卡片会是两个弧度。 */
.ac__card {
  border: none;
  border-radius: var(--fs-radius-lg);
}

/* 标题与标签一行。标题按内容宽度排，标签紧跟在它后面；
   空间不够时（长标题 + 多个标签）整条标签换到下一行，
   而不是把标题挤成两三个字一行的竖条。 */
.ac__head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 6px 10px;
}

.ac__title {
  /* 不撑满整行：标签要紧跟在标题文字后面，而不是被推到卡片的右角。
     标题很长时 flex-shrink 会把它收窄（配合 min-width: 0 触发两行截断），
     标签放不下就整条换到下一行。 */
  flex: 0 1 auto;
  min-width: 0;
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

/* 链接继承 h3 的颜色：不清掉的话 <a> 会带出自己的蓝色，
   和整卡的标题色不是一个色号。行内元素还能让两行截断继续生效。 */
.ac__title-link {
  color: inherit;
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

/* 作者组从「24px 头像 + 文字」扩成 32px 高的热区：
   负 margin 抵掉 padding，meta 行高度与上下间距都不变。
   不做成块级，否则会把时间戳挤到下一行。 */
.ac__author {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  margin: -4px;
  padding: 4px;
  border-radius: var(--fs-radius-sm);
  transition: background-color 0.15s;
}

.ac__author:hover {
  background: var(--fs-surface-hover);
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

.ac__author:hover .ac__name {
  color: var(--fs-primary);
}

/* 时间和阅读数都不换行：断行会让 meta 变成两行，卡片高度跟着变。 */
.ac__time,
.ac__views {
  white-space: nowrap;
}

/* 阅读数在昵称很长时可以最后一个被挤掉，但仍保留时间。 */
.ac__views {
  overflow: hidden;
  text-overflow: ellipsis;
}

.ac__dot {
  color: var(--fs-text-3);
}

/* 标签行跟在标题右边。flex: none 不让它被标题挤扁；
   一篇文章最多 3 个标签，最宽也就是三个短词。 */
.ac__tags {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  gap: 6px;
}

/* 白卡上用 --fs-bg 做底：比描边更轻，也不会和标题抢注意力。
   字号比标题小两档，扫读时存在感低于标题本身。 */
.ac__tag {
  padding: 2px 9px;
  border-radius: 11px;
  background: var(--fs-bg);
  color: var(--fs-text-2);
  font-size: 12px;
  font-weight: 400;
  line-height: 18px;
  transition: background-color 0.15s, color 0.15s;
}

.ac__tag:hover {
  background: var(--fs-primary-weak);
  color: var(--fs-primary);
}
</style>
