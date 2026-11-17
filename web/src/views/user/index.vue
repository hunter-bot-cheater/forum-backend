<template>
  <div class="fs-container up">
    <button class="up__back" @click="goBack">← 返回</button>

    <div v-if="loading && !profile" v-loading="true" class="up__loading" />

    <!-- 用户不存在、账号已注销都落到这一屏。
         404 之外（限流、网络）也走这里：整页没有主体数据，留着空壳只会让人以为加载失败。 -->
    <el-empty
      v-else-if="!profile"
      description="这个用户不存在或已注销"
      :image-size="96"
    />

    <template v-else>
      <!-- 头部：身份 + 计数 + 操作。计数直接取主页接口的返回值，
           不额外查文章数以外的统计——文章总数来自下面的列表接口。 -->
      <section class="fs-card up__head">
        <el-avatar :size="76" :src="avatarURL" class="up__avatar">{{ initial }}</el-avatar>

        <div class="up__info">
          <div class="up__name-row">
            <h1 class="up__name">{{ profile.nickname }}</h1>
            <span v-if="isSelf" class="up__self">这是你的主页</span>
          </div>
          <!-- 没设昵称时接口回的就是用户名，此时上面那行已经是它了，
               再显示一遍等于把同一个名字印两次。 -->
          <div v-if="profile.nickname !== profile.username" class="up__handle">
            {{ profile.username }}
          </div>
          <!-- 没填简介时整行不渲染：留一个空段落会让头部下方莫名多出一条空隙。 -->
          <p v-if="profile.bio" class="up__bio">{{ profile.bio }}</p>

          <div class="up__stats">
            <button class="up__stat" :class="{ 'is-active': tab === 'following' }" @click="onTab('following')">
              <b>{{ profile.following_count }}</b> 关注
            </button>
            <button class="up__stat" :class="{ 'is-active': tab === 'followers' }" @click="onTab('followers')">
              <b>{{ profile.follower_count }}</b> 粉丝
            </button>
            <button class="up__stat" :class="{ 'is-active': tab === 'articles' }" @click="onTab('articles')">
              <b>{{ articleTotal }}</b> 文章
            </button>
            <span class="up__joined">{{ joinedText }}</span>
          </div>
        </div>

        <!-- 自己的主页不给关注按钮（后端会拒），也不给私信入口，
             换成「编辑资料」——这是本人在这一页唯一能做的事。 -->
        <div v-if="!isSelf" class="up__actions">
          <el-button
            :type="profile.is_following ? 'default' : 'primary'"
            :loading="followBusy"
            class="up__follow"
            @click="toggleFollow"
          >
            {{ followLabel }}
          </el-button>
          <el-button class="up__chat" @click="goChat(profile.id)">发私信</el-button>
        </div>
        <div v-else class="up__actions">
          <el-button class="up__follow" @click="goSettings">编辑资料</el-button>
        </div>
      </section>

      <!-- 标签切换。样式与通知页的分类条一致，两页的「切一块内容」长得一样。 -->
      <div class="up__tabs">
        <button
          v-for="item in tabs"
          :key="item.kind"
          class="up__tab"
          :class="{ 'is-active': tab === item.kind }"
          @click="onTab(item.kind)"
        >
          <span>{{ item.label }}</span>
        </button>
      </div>

      <!-- 文章标签：复用列表页的卡片，保证「同一种东西在任何页面长得一样」。 -->
      <template v-if="tab === 'articles'">
        <div v-if="articleLoading && articles.length === 0" class="up__skeleton">
          <el-card v-for="n in 2" :key="n" shadow="never" :body-style="{ padding: '18px 20px' }">
            <el-skeleton :rows="2" animated />
          </el-card>
        </div>

        <el-empty
          v-else-if="articles.length === 0"
          class="fs-card"
          :description="emptyArticleText"
          :image-size="88"
        />

        <div v-else v-loading="articleLoading" class="up__list pin-scroll">
          <ArticleCard v-for="item in articles" :key="item.id" :item="item" />
        </div>

        <div v-if="articleTotal > articlePageSize" ref="articlePager" class="up__pager">
          <el-pagination
            background
            layout="prev, pager, next"
            :current-page="articlePage"
            :page-size="articlePageSize"
            :total="articleTotal"
            @current-change="onArticlePage"
          />
        </div>
      </template>

      <!-- 关注 / 粉丝：同一个列表组件，只是点击按钮的语义不同。 -->
      <div v-else class="fs-card up__people">
        <div v-loading="followLoading" class="pin-scroll">
          <el-empty
            v-if="!followLoading && follows.length === 0"
            :description="emptyFollowText"
            :image-size="88"
          />

          <div v-for="item in follows" :key="item.id" class="person">
            <!-- 头像和昵称都能点，落到那个人的主页：从粉丝列表继续往下逛是最常见的路径。 -->
            <el-avatar
              :size="44"
              :src="avatarOf(item)"
              class="person__avatar"
              @click="goUser(item.id)"
            >
              {{ initials(item.nickname) }}
            </el-avatar>

            <div class="person__body" @click="goUser(item.id)">
              <div class="person__name">{{ item.nickname }}</div>
              <div class="person__meta">{{ item.username }} · {{ formatRelativeTime(item.followed_at) }}关注</div>
            </div>

            <div v-if="item.id !== user.userId" class="person__actions">
              <el-button
                size="small"
                :type="item.followed_by_me ? 'default' : 'primary'"
                :loading="busyId === item.id"
                @click="toggleRowFollow(item)"
              >
                {{ rowFollowLabel(item) }}
              </el-button>
              <el-button size="small" text @click="goChat(item.id)">私信</el-button>
            </div>
          </div>
        </div>

        <div v-if="followTotal > followPageSize" ref="followPager" class="up__pager up__pager--inner">
          <el-pagination
            background
            layout="prev, pager, next"
            :current-page="followPage"
            :page-size="followPageSize"
            :total="followTotal"
            @current-change="onFollowPage"
          />
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ArticleCard from '@/components/ArticleCard.vue'
import { listArticles, type ArticleListItem } from '@/api/article'
import {
  followUser,
  getProfile,
  listFollows,
  unfollowUser,
  type FollowItem,
  type FollowKind,
  type UserProfile,
} from '@/api/user'
import { useUserStore } from '@/store/modules/user'
import { formatRelativeTime, initials } from '@/utils/format'
import { goBack as goBackTo } from '@/utils/nav'
import { keepInPlace } from '@/utils/scroll'

// 用户主页。三段内容（他的文章、他关注的人、他的粉丝）共用一个页面，
// 靠 tab 切换而不是三个路由：切 tab 不该刷新整页，也不该在浏览器历史里堆三条记录。
const route = useRoute()
const router = useRouter()
const user = useUserStore()

// 同一个组件被复用时（从粉丝列表点进另一个人的主页），路由参数会变而实例不重建，
// 所以所有取数都挂在 userId 的 watch 上，不能只写 onMounted。
const userId = computed(() => {
  const raw = Number(route.params.id)
  if (Number.isFinite(raw)) {
    return raw
  }
  return 0
})
const isSelf = computed(() => user.userId > 0 && user.userId === userId.value)

type Tab = 'articles' | FollowKind

const tabs: { kind: Tab; label: string }[] = [
  { kind: 'articles', label: '文章' },
  { kind: 'following', label: '关注' },
  { kind: 'followers', label: '粉丝' },
]
const tab = ref<Tab>('articles')

const profile = ref<UserProfile | null>(null)
const loading = ref(false)

// 文章列表：分页与文章列表页保持一致（一页 10 条）。
const articles = ref<ArticleListItem[]>([])
const articleTotal = ref(0)
const articlePage = ref(1)
const articlePageSize = 10
const articleLoading = ref(false)
// 请求代次：快速切 tab、连点页码时，只认最后一次发出的请求。
// 少了这个，先发后到的旧响应会把新 tab 的内容覆盖回去。
let articleSeq = 0

// 关注 / 粉丝列表：一页 20 条，比文章列表密一些——人比文章好扫。
const follows = ref<FollowItem[]>([])
const followTotal = ref(0)
const followPage = ref(1)
const followPageSize = 20
const followLoading = ref(false)
let followSeq = 0

const followBusy = ref(false)
// 正在请求的那一行。用 id 而不是布尔值，避免所有行的按钮一起转圈。
const busyId = ref(0)

// 两个分页器的节点：翻页前测一次位置，渲染完把页面钉回原处（见 onArticlePage）。
const articlePager = ref<HTMLElement | null>(null)
const followPager = ref<HTMLElement | null>(null)

const avatarURL = computed(() => {
  if (profile.value && profile.value.avatar_url) {
    return profile.value.avatar_url
  }
  return undefined
})

const initial = computed(() => {
  if (profile.value) {
    return initials(profile.value.nickname)
  }
  return '?'
})

// 关注按钮文案。已关注用「已关注」而不是「取关」：
// 按钮上写当前状态，用户点下去才知道会发生什么；鼠标悬停再变「取消关注」属于锦上添花，这里不做。
const followLabel = computed(() => {
  if (!profile.value) {
    return '关注'
  }
  if (profile.value.is_following) {
    return '已关注'
  }
  return '关注'
})

// 加入时间只到月份：精确到日对看主页的人没有意义，还会让这一行变长挤掉计数。
const joinedText = computed(() => {
  if (!profile.value) {
    return ''
  }
  const d = new Date(profile.value.created_at)
  if (Number.isNaN(d.getTime())) {
    return ''
  }
  return d.getFullYear() + ' 年 ' + (d.getMonth() + 1) + ' 月加入'
})

const emptyArticleText = computed(() => {
  if (isSelf.value) {
    return '你还没有发布文章'
  }
  return '这个人还没有发布文章'
})

const emptyFollowText = computed(() => {
  if (tab.value === 'followers') {
    return '还没有粉丝'
  }
  return '还没有关注任何人'
})

// avatarOf 给列表行算头像地址。
// 没上传头像时返回 undefined 而不是空串：el-avatar 拿到空串会渲染 src="" 的 img，
// 浏览器显示成破图；传 undefined 才走插槽里的首字兜底。
function avatarOf(item: FollowItem) {
  if (item.avatar_url) {
    return item.avatar_url
  }
  return undefined
}

// rowFollowLabel 是列表行里的按钮文案，与头部的 followLabel 分开写：
// 头部的状态在 profile 上，行内状态在每一行自己身上。
function rowFollowLabel(item: FollowItem) {
  if (item.followed_by_me) {
    return '已关注'
  }
  return '关注'
}

// loadProfile 拉主页资料。任何失败都把 profile 留空、整页切空态：
// 资料是这页所有内容的锚点，拿不到时下面任何一块都没有意义。
// 不再额外弹提示——拦截器已经按状态码给过文案，这里再弹一次就是两个框。
async function loadProfile() {
  loading.value = true
  try {
    profile.value = await getProfile(userId.value)
  } catch {
    profile.value = null
  } finally {
    loading.value = false
  }
}

// loadArticles 拉第一页或指定页的文章。
// seq 只在「最后一次请求」回来时写数据与关 loading，迟到的响应直接丢弃。
async function loadArticles(page: number) {
  articleSeq += 1
  const seq = articleSeq
  articleLoading.value = true
  try {
    const resp = await listArticles(page, articlePageSize, userId.value)
    if (seq !== articleSeq) {
      return
    }
    articles.value = resp.list
    articleTotal.value = resp.total
    articlePage.value = resp.page
  } catch {
    if (seq !== articleSeq) {
      return
    }
    // 失败时保留已有内容，只把 loading 关掉：整块清空再显示错误，
    // 用户点「重试」时还得重新滚动定位。
    articles.value = []
    articleTotal.value = 0
  } finally {
    if (seq === articleSeq) {
      articleLoading.value = false
    }
  }
}

// loadFollows 拉关注或粉丝列表，kind 取自当前 tab。
async function loadFollows(page: number) {
  if (tab.value === 'articles') {
    return
  }
  followSeq += 1
  const seq = followSeq
  followLoading.value = true
  try {
    const resp = await listFollows(userId.value, tab.value, page, followPageSize)
    if (seq !== followSeq) {
      return
    }
    follows.value = resp.list
    followTotal.value = resp.total
    followPage.value = resp.page
  } catch {
    if (seq !== followSeq) {
      return
    }
    follows.value = []
    followTotal.value = 0
  } finally {
    if (seq === followSeq) {
      followLoading.value = false
    }
  }
}

// boot 是「换了一个人」时的统一入口：把上一个人的数据清干净再拉新的，
// 否则新主页会先闪一下上一个人的文章与粉丝数。
async function boot() {
  articles.value = []
  articleTotal.value = 0
  articlePage.value = 1
  follows.value = []
  followTotal.value = 0
  followPage.value = 1
  profile.value = null
  tab.value = 'articles'
  await loadProfile()
  if (profile.value) {
    await loadArticles(1)
  }
}

function onTab(kind: Tab) {
  if (tab.value === kind) {
    return
  }
  tab.value = kind
  if (kind === 'articles') {
    void loadArticles(1)
    return
  }
  void loadFollows(1)
}

function onArticlePage(page: number) {
  // 与文章列表页同一处理：翻页后页面别动，分页器钉在原位，用户能连点下一页。
  const restore = keepInPlace(articlePager.value)
  void loadArticles(page).then(async () => {
    await nextTick()
    restore()
  })
}

function onFollowPage(page: number) {
  const restore = keepInPlace(followPager.value)
  void loadFollows(page).then(async () => {
    await nextTick()
    restore()
  })
}

// toggleFollow 关注 / 取关头部这个人。
// 不预判结果，直接用返回值覆盖本地状态：重复关注、并发点击时后端返回 changed=false，
// 本地按 following 刷新仍然是对的。
async function toggleFollow() {
  if (!profile.value || followBusy.value) {
    return
  }
  followBusy.value = true
  try {
    let state
    if (profile.value.is_following) {
      state = await unfollowUser(profile.value.id)
    } else {
      state = await followUser(profile.value.id)
    }
    profile.value.is_following = state.following
    profile.value.follower_count = state.follower_count
  } catch {
    // 失败什么都不改：按钮保持原状态，拦截器已经给出原因。
  } finally {
    followBusy.value = false
  }
}

// toggleRowFollow 关注 / 取关列表里的某一行。
// 只改这一行的 followed_by_me，不重拉整页：重拉会让列表在点击后整体闪一下。
async function toggleRowFollow(item: FollowItem) {
  if (busyId.value !== 0) {
    return
  }
  busyId.value = item.id
  try {
    let state
    if (item.followed_by_me) {
      state = await unfollowUser(item.id)
    } else {
      state = await followUser(item.id)
    }
    item.followed_by_me = state.following
    // 被关注的正是当前主页主人时，头部的粉丝数也要跟着变，
    // 否则「点完关注，粉丝数还是旧的」，看起来像没生效。
    if (profile.value && item.id === profile.value.id) {
      profile.value.follower_count = state.follower_count
    }

    // 在「关注」标签里取消关注后，这个人不再属于这个列表，直接移除这一行；
    // 留着会变成「我的关注里有一个人，按钮却写着关注」，自相矛盾。
    // 用 changed 而不是无条件移除：重复点击时后端返回 changed=false，列表不该动。
    if (tab.value === 'following' && !state.following && state.changed) {
      dropRow(item)
    }
  } catch {
    // 同上：失败保持原状。
  } finally {
    busyId.value = 0
  }
}

// dropRow 从关注列表里摘掉一行，并同步总数与自己的关注数。
// 看自己主页时头部「N 关注」是同一份数据，不同步就会出现
// 「列表少了一行，上面还写着 1 关注」。
function dropRow(item: FollowItem) {
  const index = follows.value.findIndex((row) => row.id === item.id)
  if (index >= 0) {
    follows.value.splice(index, 1)
  }
  if (followTotal.value > 0) {
    followTotal.value -= 1
  }
  if (isSelf.value && profile.value && profile.value.following_count > 0) {
    profile.value.following_count -= 1
  }
}

function goUser(id: number) {
  if (id === userId.value) {
    return
  }
  void router.push('/users/' + String(id))
}

function goChat(id: number) {
  void router.push('/messages/' + String(id))
}

// 自己主页上的「编辑资料」入口，与顶栏下拉里的那一项指向同一个地址。
function goSettings() {
  void router.push('/settings/profile')
}

// 返回上一层。上一页是「编辑资料」时不能 back：
// 那一页的返回又指回本页，两页会互相弹，用户看到的就是死循环。
// 这种情况直接回文章列表，属于「从哪来回哪去」的合理近似。
function goBack() {
  goBackTo(router, '/articles', ['/settings'])
}

watch(userId, () => {
  void boot()
})

onMounted(() => {
  void boot()
})
</script>

<style scoped>
.up {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

/* 返回按钮与详情页同款：同一类「回上一层」的动作在两页长得一样。 */
.up__back {
  align-self: flex-start;
  padding: 4px 10px;
  border: none;
  border-radius: var(--fs-radius);
  background: transparent;
  color: var(--fs-text-2);
  font-size: 14px;
  cursor: pointer;
}

.up__back:hover {
  background: var(--fs-surface-hover);
  color: var(--fs-text-1);
}

.up__loading {
  height: 220px;
}

.up__head {
  display: flex;
  align-items: flex-start;
  gap: 20px;
  padding: 24px 28px;
}

.up__avatar {
  flex: none;
  background: var(--fs-primary-weak);
  color: var(--fs-primary);
  font-size: 28px;
}

.up__info {
  flex: 1;
  min-width: 0;
}

.up__name-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.up__name {
  margin: 0;
  font-size: 22px;
  font-weight: 600;
  line-height: 1.3;
  color: var(--fs-text-1);
}

/* 自己主页的标记：不是按钮，只是一句说明，所以用浅底胶囊而不是 el-tag。 */
.up__self {
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--fs-bg);
  color: var(--fs-text-3);
  font-size: 12px;
}

.up__handle {
  margin-top: 2px;
  font-size: 13px;
  color: var(--fs-text-3);
}

.up__bio {
  margin: 10px 0 0;
  font-size: 14px;
  line-height: 1.7;
  color: var(--fs-text-2);
  /* 简介允许换行，但不允许撑破卡片：长串英文/链接强行断词。 */
  word-break: break-word;
}

.up__stats {
  display: flex;
  align-items: baseline;
  gap: 18px;
  margin-top: 14px;
}

/* 计数做成按钮：它们同时是「切到关注/粉丝/文章」的入口，
   纯文字需要额外一行 tab，这里一个元素干两件事。 */
.up__stat {
  padding: 0;
  border: none;
  background: transparent;
  color: var(--fs-text-3);
  font-size: 13px;
  cursor: pointer;
}

.up__stat b {
  margin-right: 2px;
  color: var(--fs-text-1);
  font-size: 15px;
  font-weight: 600;
}

.up__stat.is-active {
  color: var(--fs-primary);
}

.up__stat.is-active b {
  color: var(--fs-primary);
}

.up__joined {
  font-size: 13px;
  color: var(--fs-text-3);
}

.up__actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
}

.up__follow {
  min-width: 92px;
}

.up__tabs {
  display: flex;
  gap: 4px;
}

.up__tab {
  padding: 6px 14px;
  border: none;
  border-radius: 999px;
  background: transparent;
  color: var(--fs-text-2);
  font-size: 14px;
  cursor: pointer;
  transition:
    background 0.15s,
    color 0.15s;
}

.up__tab:hover {
  background: var(--fs-surface-hover);
}

.up__tab.is-active {
  background: var(--fs-primary-weak);
  color: var(--fs-primary);
  font-weight: 500;
}

/* 同文章列表：间距由卡片的上下内边距提供，容器不再留 gap，
   否则两张卡片之间的空白点不进任何一篇。 */
.up__list {
  display: flex;
  flex-direction: column;
}

.up__skeleton {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.up__pager {
  display: flex;
  justify-content: center;
  margin-top: 4px;
}

/* 卡片内部的分页要多一点上边距，否则贴着最后一行显得挤。 */
.up__pager--inner {
  margin: 4px 0 16px;
}

.up__people {
  padding: 6px 0;
}

.person {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 24px;
}

.person + .person {
  border-top: 1px solid var(--fs-line-soft);
}

.person__avatar {
  flex: none;
  cursor: pointer;
}

.person__body {
  flex: 1;
  min-width: 0;
  cursor: pointer;
}

.person__name {
  font-size: 14px;
  font-weight: 600;
  color: var(--fs-text-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.person__meta {
  margin-top: 2px;
  font-size: 12px;
  color: var(--fs-text-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.person__actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 4px;
}

/* 窄屏：头部改成纵向，头像与操作按钮各占一行，
   横向排会把昵称挤成两行、按钮压到简介上。 */
@media (max-width: 720px) {
  .up__head {
    flex-wrap: wrap;
    padding: 18px 16px;
  }

  .up__avatar {
    --el-avatar-size: 56px !important;
  }

  .up__actions {
    width: 100%;
  }

  .up__follow,
  .up__chat {
    flex: 1;
  }

  .up__stats {
    flex-wrap: wrap;
    gap: 12px;
  }

  .person {
    padding: 10px 14px;
  }
}
</style>
