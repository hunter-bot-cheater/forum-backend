<template>
  <div class="fs-container nt">
    <!-- 页面标题放在卡片外，和文章列表「文章 / 共 N 篇」用同一套版式：
         内容页的标题属于页面层级，不该被包进某一块卡片里。 -->
    <div class="nt__head">
      <h1 class="nt__title">通知</h1>
      <span v-if="totalUnread > 0" class="nt__badge">{{ totalUnread }} 条未读</span>
      <el-button class="nt__read-all" text :disabled="totalUnread === 0" @click="onReadAll">
        全部已读
      </el-button>
    </div>

    <div class="fs-card nt__card">
      <!-- 分类切换。红点数字只表示「这一类还有几条没读」，已读消息照样留在列表里：
           已读只去掉红点，历史不会被清空。 -->
      <div class="nt__tabs">
        <button
          v-for="tab in tabs"
          :key="tab.kind"
          class="nt__tab"
          :class="{ 'is-active': store.kind === tab.kind }"
          @click="onSwitch(tab.kind)"
        >
          <span>{{ tab.label }}</span>
          <!-- 「全部」不带角标：头部已经写了「N 条未读」，
               同一个数字并排出现两次，看起来像两个不同的口径。 -->
          <span v-if="tab.kind !== '' && countOf(tab.kind) > 0" class="nt__tab-count">
            {{ countOf(tab.kind) }}
          </span>
        </button>
      </div>

      <div v-loading="store.loading" class="nt__list">
        <el-empty
          v-if="!store.loading && store.items.length === 0"
          :description="emptyText"
          :image-size="88"
        />

        <div
          v-for="item in store.items"
          :key="item.id"
          class="nt__item"
          :class="{ 'is-unread': !item.is_read }"
          @click="openTarget(item)"
        >
          <!-- 头像右下角挂一个类型角标：一列通知扫下去，
               不用读文字就能分出哪些是赞、哪些是评论。
               头像本身可点进触发者主页，所以用一个内层元素承接点击并 stop：
               否则点头像会同时触发整行的「标记已读 + 跳文章」。 -->
          <div class="nt__avatar">
            <el-avatar
              :size="40"
              :src="avatarOf(item)"
              class="nt__who"
              @click.stop="goUser(item)"
            >{{ initialOf(item) }}</el-avatar>
            <span
              v-if="kindOf(item)"
              class="nt__kind"
              :class="'is-' + kindOf(item)"
            >{{ kindOf(item) === 'like' ? '赞' : '评' }}</span>
          </div>

          <div class="nt__body">
            <p class="nt__line">
              <span class="nt__name nt__who" @click.stop="goUser(item)">{{ nameOf(item) }}</span>
              <span class="nt__action">{{ actionOf(item) }}</span>
            </p>

            <!-- 文章引用：没有这一行时用户只知道「赞了你的文章」，
                 必须点进去才看得出是哪篇，一页通知读下来没有任何信息量。
                 标题可能为空（文章已删），这时整块不渲染。 -->
            <p v-if="item.article_title" class="nt__ref">
              <span class="nt__ref-tag">文章</span>
              <span class="nt__ref-title">{{ item.article_title }}</span>
            </p>

            <p class="nt__meta">{{ formatListTime(item.created_at) }}</p>
          </div>

          <!-- 未读点放末端：一列点对齐后扫一眼就能数出还剩几条没看。 -->
          <span v-if="!item.is_read" class="nt__dot" aria-label="未读" />
        </div>
      </div>

      <!-- 只有一页时不摆分页器：点了没反应，还会让人以为后面还有内容。 -->
      <div v-if="store.total > store.pageSize" class="nt__foot">
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
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useNotificationStore } from '@/store/modules/notification'
import { formatListTime, initials } from '@/utils/format'
import type { NotificationKind, NotificationResp } from '@/api/notification'

const router = useRouter()
const store = useNotificationStore()

// 分类固定三项：关注事件还没接，先不摆一个永远为空的 tab。
// 以后接入 follow.created 时，这里加一行即可，后端已经把分类算好了。
const tabs: { label: string; kind: NotificationKind }[] = [
  { label: '全部', kind: '' },
  { label: '赞', kind: 'like' },
  { label: '评论', kind: 'comment' },
]

// 顶部胶囊用全站未读，tab 用本分类未读，两个数字口径不同不能混用。
const totalUnread = computed(() => store.summary.total || 0)

const emptyText = computed(() => {
  if (store.kind === '') {
    return '还没有消息'
  }
  return '这个分类还没有消息'
})

onMounted(() => {
  // 每次进来都拉第一页：离开这段时间可能来了新通知，缓存里的列表会显示旧数据。
  // 计数由布局层的 refreshSummary 负责，这里只管列表。
  void store.refresh('', 1)
})

function countOf(kind: NotificationKind): number {
  if (kind === '') {
    return store.summary.total || 0
  }
  return store.summary[kind] || 0
}

// nameOf 取展示名，优先 actor.nickname（服务端已做过空昵称回退）。
// 账号注销后 actor 整个字段都不在，此时显示「有人」而不是空白，
// 否则整行读起来像渲染失败。
function nameOf(item: NotificationResp): string {
  if (item.actor && item.actor.nickname) {
    return item.actor.nickname
  }
  return '有人'
}

// avatarOf 空头像返回 undefined 而不是空串：
// el-avatar 拿到空串会渲染一个 src="" 的 img（浏览器显示成破图），
// 传 undefined 才走插槽兜底。
function avatarOf(item: NotificationResp): string | undefined {
  if (item.actor && item.actor.avatar_url) {
    return item.actor.avatar_url
  }
  return undefined
}

function initialOf(item: NotificationResp): string {
  return initials(nameOf(item))
}

// kindOf 把事件类型归成角标要显示的两种。
// 只用于角标的颜色与单字，文案仍由 actionOf 负责：
// 两者用途不同，合并会让「回复了你的评论」被压成「评论」两个字。
//
// 未识别的类型返回空串，不画角标：关注类事件没有「赞/评」可言，
// 硬归成评论会在头像上标一个错的字。
function kindOf(item: NotificationResp): 'like' | 'comment' | '' {
  if (item.type === 'like.created') {
    return 'like'
  }
  if (item.type === 'comment.created') {
    return 'comment'
  }
  return ''
}

// actionOf 把事件类型翻译成人话。
// 用事件类型而不是 target_type 判断：同属文章域，点赞和评论的文案不同，
// 单看 target_type 分不出来。未识别的类型给一句中性文案，
// 后端新增事件时前端不改也不会显示成空行。
//
// comment.created 有两种含义，必须再看 target_type：顶级评论作用在文章上
// （target_type=article），回复作用在评论上（target_type=comment）。
// 只看 type 会把「回复了你的评论」错写成「评论了你的文章」。
function actionOf(item: NotificationResp): string {
  if (item.type === 'like.created') {
    return '赞了你的文章'
  }
  if (item.type === 'comment.created') {
    if (item.target_type === 'comment') {
      return '回复了你的评论'
    }
    return '评论了你的文章'
  }
  return '和你互动了'
}

// goUser 进触发者主页。actor 可能缺失（注销后的账号），此时不跳：
// /users/undefined 会落到 404 页，比不响应更糟。
function goUser(item: NotificationResp) {
  if (!item.actor || !item.actor.id) {
    return
  }
  void router.push('/users/' + String(item.actor.id))
}

// openTarget 跳到通知对应的位置。
//
// 回复类通知能指到具体那一条，所以带上 ?comment=<id>，由详情页翻页并高亮；
// 点赞、以及「评论了你的文章」（target_type=article，指整篇文章）没有具体评论可指，
// 直接进文章。comment.created 的两种含义看 target_type 分，见 actionOf。
function openTarget(item: NotificationResp) {
  // 点开即已读：只清这一条的红点，不像「全部已读」那样把没看的也一起清。
  // 不 await：紧接着就要跳转，红点本地已经先消掉了。
  void store.markRead(item.id)
  if (item.article_id <= 0) {
    return
  }
  const path = '/articles/' + String(item.article_id)
  if (item.target_type === 'comment' && item.target_id > 0) {
    router.push({ path, query: { comment: String(item.target_id) } })
    return
  }
  router.push(path)
}

async function onReadAll() {
  const affected = await store.markAllRead()
  if (affected < 0) {
    // 失败：拦截器已经弹过提示，这里不能再提示「暂无未读」，否则会误导。
    return
  }
  if (affected > 0) {
    ElMessage.success('已把 ' + String(affected) + ' 条通知标记为已读')
    return
  }
  ElMessage.info('暂无未读通知')
}

function onSwitch(kind: NotificationKind) {
  store.switchKind(kind)
}

function onPage(next: number) {
  void store.refresh(store.kind, next)
}
</script>

<style scoped>
/* 宽度与文章列表主列一致（820px）：两页之间来回切时，
   内容区的左右边界不该跳来跳去。 */
.nt {
  max-width: 820px;
  margin: 0 auto;
}

.nt__head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 14px;
  padding: 0 2px;
}

.nt__title {
  font-size: 22px;
  font-weight: 700;
  color: var(--fs-text-1);
}

/* 未读数是「可扫读」信息，做成胶囊而不是纯文字，扫视时更容易抓住。 */
.nt__badge {
  padding: 2px 8px;
  border-radius: 10px;
  background: var(--fs-like-weak);
  color: var(--fs-like);
  font-size: 12px;
  line-height: 18px;
}

/* 「全部已读」推到最右并压小字号：它是批量操作，
   视觉权重不该高过列表本身。 */
.nt__read-all {
  margin-left: auto;
  font-size: 13px;
}

.nt__card {
  overflow: hidden;
}

/* tab 用胶囊按钮而不是 el-tabs：Element Plus 的 tab 带下划线和大内边距，
   放在这里会比页面标题还重。 */
.nt__tabs {
  display: flex;
  gap: 8px;
  padding: 14px 20px;
  border-bottom: 1px solid var(--fs-line-soft);
}

.nt__tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 14px;
  border: none;
  border-radius: 15px;
  background: var(--fs-bg);
  color: var(--fs-text-2);
  font-size: 13px;
  cursor: pointer;
  transition: background-color 0.15s, color 0.15s;
}

.nt__tab:hover {
  background: var(--fs-line);
}

.nt__tab.is-active {
  background: var(--fs-primary-weak);
  color: var(--fs-primary);
  font-weight: 600;
}

/* tab 上的红点数字：未读才显示，为 0 时整块不渲染。 */
.nt__tab-count {
  min-width: 16px;
  padding: 0 4px;
  border-radius: 8px;
  background: var(--fs-like);
  color: #fff;
  font-size: 11px;
  line-height: 16px;
  text-align: center;
}

/* 空态给一个下限高度：否则加载完到列表出现之间，
   卡片会从「只有一个 tab」跳成几百像素高。 */
.nt__list {
  min-height: 240px;
}

.nt__item {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 14px 20px;
  border-bottom: 1px solid var(--fs-line-soft);
  cursor: pointer;
  transition: background-color 0.15s;
}

/* 最后一行不描线：卡片自己已经有圆角，多一条线贴着底边会显得像截断。 */
.nt__item:last-child {
  border-bottom: none;
}

.nt__item:hover {
  background: var(--fs-surface-hover);
}

/* 未读行给一层极浅的蓝底：比加粗字重更克制，
   也不会让时间戳看起来比正文更重要。已读行就是纯白，没有红点。 */
.nt__item.is-unread {
  background: var(--fs-primary-faint);
}

/* 角标要压住头像右下角，所以容器必须是定位上下文。 */
.nt__avatar {
  position: relative;
  flex: none;
  line-height: 0;
}

.nt__kind {
  position: absolute;
  right: -4px;
  bottom: -4px;
  display: flex;
  align-items: center;
  justify-content: center;
  /* 20px 而不是 18px：中文字在 10px 下笔画会糊成一个色块，
     11px 才能认清「赞」和「评」。 */
  width: 20px;
  height: 20px;
  /* 2px 白边把角标和头像隔开，压在深色头像上也分得清。 */
  border: 2px solid var(--fs-surface);
  border-radius: 50%;
  color: #fff;
  font-size: 11px;
  line-height: 1;
}

/* 赞用暖红、评论用主色：整站只有这两支有语义的颜色，不再引入第三种。 */
.nt__kind.is-like {
  background: var(--fs-like);
}

.nt__kind.is-comment {
  background: var(--fs-primary);
}

.nt__body {
  flex: 1;
  min-width: 0;
  /* 与 40px 头像顶部对齐：头像有 0 行高，文字首行需要轻微下压才不显得悬空。 */
  padding-top: 1px;
}

.nt__line {
  font-size: 14px;
  line-height: 1.5;
  color: var(--fs-text-1);
  /* 名字过长时只截一行，不让整行换行把列表撑高。 */
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.nt__name {
  font-weight: 600;
}

/* 可点的头像与昵称：给 hover 反馈，并和整行「点了看文章」区分开——
   点这里去的是人的主页，不是文章。 */
.nt__who {
  cursor: pointer;
}

.nt__name.nt__who:hover {
  color: var(--fs-primary);
}

/* 名字和动作之间留 4px：两个 inline 元素紧挨着渲染时，
   「123456789赞了你的文章」会粘成一串，读不出主语到哪里结束。 */
.nt__action {
  margin-left: 4px;
  color: var(--fs-text-2);
}

/* 文章引用做成浅底小条：它是这条通知的补充上下文，
   不该和「谁赞了你」抢同一行的视觉权重。
   inline-flex 让条子只包住标题，不铺满整行：
   铺满时右端会顶到未读点，读起来像一条横向色带而不是引用。 */
.nt__ref {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  max-width: 100%;
  margin-top: 6px;
  padding: 5px 10px;
  border-radius: var(--fs-radius-sm);
  background: var(--fs-bg);
  font-size: 13px;
  color: var(--fs-text-2);
}

.nt__ref-tag {
  flex: none;
  padding: 0 5px;
  border-radius: 3px;
  background: #fff;
  color: var(--fs-text-3);
  font-size: 11px;
  line-height: 16px;
}

/* 标题单行省略：列表里只需要认出是哪篇，全文在详情页。
   min-width: 0 是 flex 子项能收缩的前提，否则长标题会撑破引用条。 */
.nt__ref-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--fs-text-2);
}

.nt__meta {
  margin-top: 4px;
  font-size: 12px;
  color: var(--fs-text-3);
}

/* 未读点用 margin-left:auto 推到末端。
   不用绝对定位：行高随内容变化，绝对定位容易压到正文。 */
.nt__dot {
  flex: none;
  align-self: center;
  margin-left: auto;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--fs-like);
}

.nt__foot {
  display: flex;
  justify-content: center;
  padding: 16px 0 18px;
  border-top: 1px solid var(--fs-line-soft);
}

@media (max-width: 720px) {
  .nt__tabs {
    padding: 12px 14px;
  }

  .nt__item {
    padding: 13px 14px;
  }

  /* 窄屏收起文章引用下方的留白，但保留引用块：
     它决定用户要不要点进去，比省几十像素重要。 */
  .nt__ref {
    margin-top: 5px;
  }
}
</style>
