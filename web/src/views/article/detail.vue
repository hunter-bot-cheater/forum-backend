<template>
  <div class="dt-page">
    <button class="dt-back" @click="goBack">← 返回</button>

    <div v-if="loading" v-loading="true" class="dt-loading" />

    <el-empty
      v-else-if="!article"
      description="文章不存在或已被删除"
      :image-size="96"
    />

    <template v-else>
      <h1 class="dt-title">{{ article.title }}</h1>

      <div class="dt-meta">
        <el-avatar :size="28" :src="authorAvatar">{{ authorInitial }}</el-avatar>
        <span class="dt-meta__name">{{ authorName }}</span>
        <span class="dt-meta__sep">·</span>
        <span>{{ formatRelativeTime(article.created_at) }}</span>
      </div>

      <!-- 正文原样输出：后端存的是纯文本，没有富文本或 markdown，
           pre-wrap 保留作者写的换行，break-word 防止长链接撑破版面。 -->
      <div class="dt-content">{{ article.content }}</div>

      <div class="dt-actions">
        <button
          class="dt-like"
          :class="{ 'is-liked': article.liked }"
          :disabled="liking"
          @click="toggleLike"
        >
          <span class="dt-like__icon">{{ article.liked ? '♥' : '♡' }}</span>
          <span>{{ article.like_count }}</span>
        </button>

        <!-- 删除只对作者本人出现。这不是安全边界（后端按 token 再判一次），
             只是不给别人看一个点了必然失败的按钮。 -->
        <button v-if="isAuthor" class="dt-delete" :disabled="deleting" @click="removeArticle">
          {{ deleting ? '删除中…' : '删除' }}
        </button>
      </div>

      <section class="dt-comments">
        <h2 class="dt-comments__title">
          评论
          <span v-if="commentTotal > 0" class="dt-comments__count">{{ commentTotal }}</span>
        </h2>

        <div class="dt-composer">
          <el-avatar :size="32" :src="myAvatar">{{ myInitial }}</el-avatar>
          <div class="dt-composer__body">
            <el-input
              v-model="newComment"
              type="textarea"
              :rows="2"
              maxlength="2000"
              show-word-limit
              resize="none"
              placeholder="说点什么…"
            />
            <div class="dt-composer__foot">
              <el-button
                type="primary"
                :disabled="newComment.trim() === ''"
                :loading="submitting"
                @click="submitComment"
              >
                发布
              </el-button>
            </div>
          </div>
        </div>

        <div v-loading="commentsLoading" class="dt-comments__body">
          <p v-if="anchorGone" class="dt-anchor-hint">
            通知里那条评论已经被删除了，下面是最新的评论。
          </p>

          <el-empty
            v-if="!commentsLoading && comments.length === 0"
            description="还没有评论，来说第一句"
            :image-size="80"
          />

          <div v-for="root in comments" :key="root.id" class="dt-item" :data-cid="root.id">
            <el-avatar :size="32" :src="avatarOf(root)">{{ initialOf(root) }}</el-avatar>

            <div class="dt-item__body">
              <div class="dt-item__head">
                <span class="dt-item__name">{{ nameOf(root) }}</span>
                <span class="dt-item__time">{{ formatRelativeTime(root.created_at) }}</span>
              </div>
              <p class="dt-item__text">{{ root.content }}</p>
              <div class="dt-item__ops">
                <button class="dt-op" @click="startReply(root.id)">回复</button>
                <button v-if="isMine(root)" class="dt-op" @click="removeComment(root)">删除</button>
              </div>

              <div v-if="replyTargetId === root.id" class="dt-reply-box">
                <el-input
                  v-model="replyContent"
                  type="textarea"
                  :rows="2"
                  maxlength="2000"
                  resize="none"
                  :placeholder="'回复 ' + nameOf(root)"
                />
                <div class="dt-reply-box__foot">
                  <el-button text @click="cancelReply">取消</el-button>
                  <el-button
                    type="primary"
                    :disabled="replyContent.trim() === ''"
                    :loading="replySubmitting"
                    @click="submitReply(root.id)"
                  >
                    回复
                  </el-button>
                </div>
              </div>

              <div v-if="(root.replies ?? []).length > 0" class="dt-replies">
                <div v-for="reply in root.replies" :key="reply.id" class="dt-reply" :data-cid="reply.id">
                  <el-avatar :size="24" :src="avatarOf(reply)">{{ initialOf(reply) }}</el-avatar>

                  <div class="dt-item__body">
                    <div class="dt-item__head">
                      <span class="dt-item__name">{{ nameOf(reply) }}</span>
                      <!-- 回复非顶级评论时显示「回复 @被回复者」，让人看清这条挂在谁名下。
                           被回复者注销后 reply_to 整个缺失，此时只显示名字不显示这段。 -->
                      <span v-if="reply.reply_to" class="dt-reply__to">
                        回复 @{{ reply.reply_to.nickname }}
                      </span>
                      <span class="dt-item__time">{{ formatRelativeTime(reply.created_at) }}</span>
                    </div>
                    <p class="dt-item__text">{{ reply.content }}</p>
                    <div class="dt-item__ops">
                      <button class="dt-op" @click="startReply(reply.id)">回复</button>
                      <button v-if="isMine(reply)" class="dt-op" @click="removeComment(reply)">删除</button>
                    </div>

                    <div v-if="replyTargetId === reply.id" class="dt-reply-box">
                      <el-input
                        v-model="replyContent"
                        type="textarea"
                        :rows="2"
                        maxlength="2000"
                        resize="none"
                        :placeholder="'回复 ' + nameOf(reply)"
                      />
                      <div class="dt-reply-box__foot">
                        <el-button text @click="cancelReply">取消</el-button>
                        <el-button
                          type="primary"
                          :disabled="replyContent.trim() === ''"
                          :loading="replySubmitting"
                          @click="submitReply(reply.id)"
                        >
                          回复
                        </el-button>
                      </div>
                    </div>
                  </div>
                </div>

                <!-- 后端每条根评论最多下发 200 条回复，超出的只给总数。
                     这里不做成按钮：没有「展开更多回复」的接口，点了只会让人以为坏了。 -->
                <p v-if="root.reply_count > (root.replies ?? []).length" class="dt-replies__more">
                  另有 {{ root.reply_count - (root.replies ?? []).length }} 条回复未展示
                </p>
              </div>
            </div>
          </div>
        </div>

        <div v-if="commentTotal > commentPageSize" class="dt-foot">
          <el-pagination
            background
            layout="prev, pager, next"
            :current-page="commentPage"
            :page-size="commentPageSize"
            :total="commentTotal"
            @current-change="onCommentPage"
          />
        </div>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  deleteArticle,
  getArticle,
  likeArticle,
  unlikeArticle,
  type ArticleDetailResp,
} from '@/api/article'
import {
  createComment,
  deleteComment,
  listComments,
  locateComment,
  type CommentAnchor,
  type CommentResp,
} from '@/api/comment'
import { useArticleStore } from '@/store/modules/article'
import { useUserStore } from '@/store/modules/user'
import { formatRelativeTime, initials } from '@/utils/format'

// 路由给这个组件配了 props: true，所以 id 从 props 拿而不是从 route.params 取，
// 少一次对 URL 结构的耦合。
const props = defineProps<{ id: string }>()

const router = useRouter()
// route 只用来读通知带过来的 ?comment= 锚点，其余参数仍走 props。
const route = useRoute()
const user = useUserStore()
// 列表页的缓存要在发布、删除后失效，否则回到列表还是旧数据。
const articleStore = useArticleStore()

const articleId = computed(() => Number(props.id))

const article = ref<ArticleDetailResp | null>(null)
const loading = ref(false)
// liking 只是防重复点击的闸门，不参与渲染判断：
// 按钮点亮与否完全由 article.liked 决定，那个值来自服务端。
const liking = ref(false)

const comments = ref<CommentResp[]>([])
const commentTotal = ref(0)
const commentPage = ref(1)
// 一页 20 条根评论。回复不算在分页里，跟着各自的根评论一起下发。
const commentPageSize = 20
const commentsLoading = ref(false)
// anchorGone 表示通知带过来的那条评论已经不在了（被删除）。
// 用评论区顶部的内联提示而不是弹窗：用户此刻正盯着评论区，
// 提示要落在他的视线里，而不是飘在屏幕角落几秒就消失。
const anchorGone = ref(false)

const newComment = ref('')
const submitting = ref(false)

// replyTargetId 是当前展开输入框的那条评论 id，0 表示没有展开。
// 用 id 而不是整个对象：列表刷新后对象会被替换，存 id 才不会指向旧数据。
const replyTargetId = ref(0)
const replyContent = ref('')
const replySubmitting = ref(false)

// 评论请求序号，用法与列表页一致：快速翻页时丢弃迟到的响应。
let commentSeq = 0

onMounted(() => {
  void loadArticle()
  void initComments()
})

async function loadArticle() {
  // id 不是数字时不必打请求：路径被手改成 /articles/abc 会直接 400。
  if (!Number.isFinite(articleId.value) || articleId.value <= 0) {
    article.value = null
    return
  }
  loading.value = true
  try {
    article.value = await getArticle(articleId.value)
  } catch {
    // 拦截器已经弹过提示（404 会被翻成「文章不存在」这类文案），
    // 这里只需要让页面落到「不存在」那个空态。
    article.value = null
  } finally {
    loading.value = false
  }
}

// loadComments 拉一页根评论。
// 失败时保留旧列表：清空会让用户以为评论被删了。
async function loadComments(p: number) {
  commentSeq += 1
  const current = commentSeq
  commentsLoading.value = true
  try {
    const data = await listComments(articleId.value, p, commentPageSize)
    if (current !== commentSeq) {
      return
    }
    comments.value = data.list
    commentTotal.value = data.total
    commentPage.value = data.page
  } catch {
    // 见上。
  } finally {
    if (current === commentSeq) {
      commentsLoading.value = false
    }
  }
}

function onCommentPage(next: number) {
  // 换页时收起回复框：它属于上一页的某条评论，留着会指向看不到的内容。
  cancelReply()
  void loadComments(next)
}

// initComments 决定首次进评论区落在第几页。
//
// 从通知点进来时 URL 带 ?comment=<id>：列表按根评论分页，那条评论可能在第五页，
// 直接拉第一页等于没跳。所以先问服务端它在哪一页，再滚过去高亮。
// 定位失败一律退回第一页——定位不到不该让整个评论区变成空白。
async function initComments() {
  const raw = route.query.comment
  const anchorId = Number(Array.isArray(raw) ? raw[0] : raw)
  if (!Number.isFinite(anchorId) || anchorId <= 0) {
    await loadComments(1)
    return
  }

  let anchor: CommentAnchor
  try {
    anchor = await locateComment(anchorId)
  } catch (err) {
    // 只有「评论不存在」才说明锚点失效：说成「已删除」而实际是限流或网络抖动，
    // 会让用户以为内容没了。其它错误静默降级到第一页。
    // 不再弹第二个提示框——拦截器已经弹过一次，两条并排弹出来像出了两个错。
    if ((err as { errorCode?: string }).errorCode === 'not_found') {
      anchorGone.value = true
    }
    await loadComments(1)
    return
  }

  // 不是同一篇就按普通打开处理：URL 被手改过，或者评论已删、id 对不上。
  if (anchor.article_id !== articleId.value) {
    await loadComments(1)
    return
  }

  // index 是从 1 开始的序号，一页 20 条：第 1 条在第 1 页，第 21 条在第 2 页。
  const page = Math.ceil(anchor.index / commentPageSize)
  await loadComments(page)
  await focusAnchor(anchor)
}

// focusAnchor 滚到目标评论并闪一下底色。
//
// 回复有可能不在这页上：后端每条根评论最多下发 200 条回复，超出的只给总数。
// 这种情况退到它的根评论，至少让人看到是哪一层楼，而不是滚到一个不存在的位置。
async function focusAnchor(anchor: CommentAnchor) {
  // 等列表渲染完再找 DOM，否则 querySelector 一定拿不到刚拉回来的数据。
  await nextTick()
  let el = document.querySelector<HTMLElement>('[data-cid="' + String(anchor.comment_id) + '"]')
  if (!el) {
    el = document.querySelector<HTMLElement>('[data-cid="' + String(anchor.root_id) + '"]')
  }
  if (!el) {
    return
  }

  // block: 'center' 而不是默认的 start：顶栏吸顶，滚到顶部会把目标藏在标题栏后面。
  el.scrollIntoView({ block: 'center' })
  // 事件回调里用局部常量，避免闭包里再碰可能被重新赋值的 el。
  const target = el
  target.classList.add('is-anchor')
  target.addEventListener('animationend', () => target.classList.remove('is-anchor'), { once: true })
}

// toggleLike 按当前状态决定点赞还是取消。
//
// 不做乐观更新：数字直接用响应里的 like_count，那是服务端的真实值。
// 乐观更新在并发下会和服务端越差越远，而点赞这个动作的往返只有几十毫秒，
// 用户感觉不到延迟，没必要为它引入「先加一，失败再减回来」的复杂度。
async function toggleLike() {
  if (!article.value || liking.value) {
    return
  }
  liking.value = true
  try {
    const data = article.value.liked
      ? await unlikeArticle(article.value.id)
      : await likeArticle(article.value.id)
    if (article.value) {
      article.value.liked = data.liked
      article.value.like_count = data.like_count
    }
  } catch {
    // 拦截器提示过了，按钮保持原状即可。
  } finally {
    liking.value = false
  }
}

// isAuthor 判断当前登录用户是不是这篇文章的作者。
// 作者字段可能整个缺失（服务端没预加载到作者时会省略），那时一律当作「不是作者」：
// 宁可少给一个删除按钮，也不给一个点了必然 404 的按钮。
const isAuthor = computed(() => {
  if (article.value === null || article.value.author === undefined) {
    return false
  }
  return article.value.author.id === user.userId
})

const deleting = ref(false)

// removeArticle 删除文章。先确认再请求：删除不可撤销，
// 且会连带删掉评论与点赞，一次误点代价太大。
async function removeArticle() {
  if (deleting.value) {
    return
  }
  try {
    await ElMessageBox.confirm('删除后无法恢复，下面的评论和点赞也会一起消失。', '删除这篇文章？', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
    })
  } catch {
    // 点了取消或按 Esc：什么都不做。
    return
  }

  deleting.value = true
  try {
    await deleteArticle(articleId.value)
    // 列表缓存必须失效：否则回到列表还能看到刚删掉的文章，
    // 点进去是 404，看起来像删除没生效。
    articleStore.invalidate()
    ElMessage.success('文章已删除')
    await router.replace('/articles')
  } catch {
    // 拦截器已提示（403 / 404）。留在当前页，用户可以重试或自己返回。
  } finally {
    deleting.value = false
  }
}

// submitComment 发一条顶级评论。
async function submitComment() {
  const content = newComment.value.trim()
  if (content === '' || submitting.value) {
    return
  }
  submitting.value = true
  try {
    await createComment(articleId.value, content)
    newComment.value = ''
    // 发完重新拉一次列表，而不是把返回值插进数组：
    // 后端新建评论时没有预加载用户，返回值里没有 author，直接渲染会少一个头像和名字。
    //
    // 评论按时间升序排（老的在前），新评论落在最后一页，
    // 所以这里跳到「加一条之后的最后一页」，用户才能立刻看到自己刚发的那条。
    const nextTotal = commentTotal.value + 1
    const lastPage = Math.max(1, Math.ceil(nextTotal / commentPageSize))
    await loadComments(lastPage)
    ElMessage.success('评论已发布')
  } catch {
    // 拦截器提示过了：内容为空、超长、文章已删都会走到这里。
  } finally {
    submitting.value = false
  }
}

// startReply 在指定评论下展开输入框。同一时间只展开一个，换目标时清空草稿。
function startReply(commentId: number) {
  if (replyTargetId.value !== commentId) {
    replyContent.value = ''
  }
  replyTargetId.value = commentId
}

function cancelReply() {
  replyTargetId.value = 0
  replyContent.value = ''
}

// submitReply 回复某条评论（可以是根评论，也可以是别的回复）。
// parentId 传目标评论 id，root_id 由后端推导，前端不参与算树的形状。
async function submitReply(parentId: number) {
  const content = replyContent.value.trim()
  if (content === '' || replySubmitting.value) {
    return
  }
  replySubmitting.value = true
  try {
    await createComment(articleId.value, content, parentId)
    cancelReply()
    // 回复不改变根评论的条数，停在当前页即可。
    await loadComments(commentPage.value)
    ElMessage.success('回复已发布')
  } catch {
    // 同上。
  } finally {
    replySubmitting.value = false
  }
}

// removeComment 删自己的评论。删根评论会连带删掉下面所有回复，所以先确认。
async function removeComment(target: CommentResp) {
  const isRoot = target.root_id === 0
  try {
    await ElMessageBox.confirm(
      isRoot ? '这条评论和它下面的回复都会被删除，确定吗？' : '确定删除这条回复吗？',
      '删除评论',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    // 用户点了取消，ElMessageBox 会 reject，这是正常路径不是错误。
    return
  }

  try {
    await deleteComment(target.id)
    // 删的是当前页的最后一条根评论时，这一页会空掉，
    // 往前退一页避免用户停在一个空列表上。
    const remain = isRoot ? commentTotal.value - 1 : commentTotal.value
    const lastPage = Math.max(1, Math.ceil(remain / commentPageSize))
    const targetPage = Math.min(commentPage.value, lastPage)
    await loadComments(targetPage)
    ElMessage.success('已删除')
  } catch {
    // 拦截器提示过了（别人的评论会是 404）。
  }
}

// isMine 决定要不要显示删除按钮。
//
// 注意 userId 在首屏可能还是 0（fetchMe 还没回来），那时不显示删除按钮。
// 这比反过来更安全：宁可短暂少一个按钮，也不要让别人看到能删自己评论的入口。
function isMine(target: CommentResp): boolean {
  if (user.userId === 0 || !target.author) {
    return false
  }
  return target.author.id === user.userId
}

function goBack() {
  // 用 back 而不是 push('/articles')：从列表第 3 页点进来时，
  // push 会把人送回第 1 页，丢失刚才翻页的位置。
  router.back()
}

const authorName = computed(() => {
  if (article.value && article.value.author && article.value.author.nickname) {
    return article.value.author.nickname
  }
  return '匿名'
})

const authorAvatar = computed(() => {
  if (article.value && article.value.author && article.value.author.avatar_url) {
    return article.value.author.avatar_url
  }
  return undefined
})

const authorInitial = computed(() => initials(authorName.value))

// 评论输入框旁边的头像是「我」。资料没加载出来时显示一个占位问号，
// 比空头像好——空头像会让输入框看起来像坏了。
const myAvatar = computed(() => user.profile?.avatar_url || undefined)
const myInitial = computed(() => initials(user.profile?.nickname || '我'))

function nameOf(target: CommentResp): string {
  if (target.author && target.author.nickname) {
    return target.author.nickname
  }
  return '匿名'
}

// avatarOf 空头像返回 undefined 而不是空串：
// el-avatar 拿到空串会渲染 src="" 的 img（浏览器显示成破图）。
function avatarOf(target: CommentResp): string | undefined {
  if (target.author && target.author.avatar_url) {
    return target.author.avatar_url
  }
  return undefined
}

function initialOf(target: CommentResp): string {
  return initials(nameOf(target))
}
</script>

<style scoped>
/* 色板接到全局 token，白底块与文章卡片同圆角同阴影。
   宽度 720 而不是列表页的 820：正文是长文，行太长眼睛要来回扫。 */
.dt-page {
  --dt-primary: var(--fs-primary);
  --dt-text: var(--fs-text-1);
  --dt-text-weak: var(--fs-text-3);
  --dt-line: var(--fs-line);
  max-width: 720px;
  margin: 0 auto;
  /* 左右内边距是这个页面以前没有的：那时没有卡片，文字贴着栏宽，
     看起来只是「左对齐」；包成圆角卡片之后贴边就成了缺陷，
     标题和分割线会直接撞在圆角上。 */
  padding: 0 28px 36px;
  background: var(--fs-surface);
  border-radius: var(--fs-radius-lg);
  box-shadow: var(--fs-shadow-sm);
  overflow: hidden;
}

/* 返回按钮用纯文本而不是 el-button：
   这是一条导航动作，不是页面里的操作，做成按钮会和下面的「发布」抢视觉权重。 */
.dt-back {
  padding: 6px 0;
  border: none;
  background: none;
  color: var(--dt-text-weak);
  font-size: 13px;
  cursor: pointer;
}

.dt-back:hover {
  color: var(--dt-text);
}

.dt-loading {
  min-height: 240px;
}

.dt-title {
  margin: 8px 0 12px;
  font-size: 24px;
  font-weight: 700;
  line-height: 1.4;
  color: var(--dt-text);
}

.dt-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--dt-line);
  font-size: 12px;
  color: var(--dt-text-weak);
}

.dt-meta__name {
  font-size: 13px;
  color: var(--fs-text-2);
}

.dt-meta__sep {
  color: var(--fs-text-3);
}

/* 正文行高比列表大：长文连续阅读需要更多呼吸空间。
   pre-wrap 保留作者写的换行，break-word 防止长链接或长串英文撑出横向滚动。 */
.dt-content {
  padding: 20px 0;
  font-size: 15px;
  line-height: 1.8;
  color: var(--dt-text);
  white-space: pre-wrap;
  word-break: break-word;
}

.dt-actions {
  display: flex;
  /* 左对齐而不是居中：居中会让胶囊悬在长文末尾的空白里，
     和正文左边线对齐之后才是一条连续的阅读轴线。 */
  justify-content: flex-start;
  padding: 4px 0 20px;
  border-bottom: 1px solid var(--dt-line);
}

.dt-like {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 7px 18px;
  border: 1px solid var(--dt-line);
  border-radius: 18px;
  background: #fff;
  color: var(--fs-text-2);
  font-size: 14px;
  cursor: pointer;
  transition: border-color 0.15s, color 0.15s, background-color 0.15s;
}

.dt-like:hover:not(:disabled) {
  border-color: var(--fs-like);
  color: var(--fs-like);
}

/* 已点赞：实心红心 + 浅红底，和未点赞一眼可分。 */
.dt-like.is-liked {
  border-color: var(--fs-like);
  background: var(--fs-like-weak);
  color: var(--fs-like);
}

.dt-like:disabled {
  cursor: default;
  opacity: 0.7;
}

/* 删除推到最右：它是低频且破坏性的动作，不该紧挨着点赞。
   靠 margin-left:auto 而不是给容器加 space-between——
   加 space-between 会让只有点赞按钮时的排版也变，作者与非作者看到的两套布局不一致。 */
.dt-delete {
  margin-left: auto;
  padding: 7px 6px;
  border: none;
  background: none;
  color: var(--dt-text-weak);
  font-size: 13px;
  cursor: pointer;
  transition: color 0.15s;
}

.dt-delete:hover:not(:disabled) {
  color: var(--fs-like);
}

.dt-delete:disabled {
  cursor: default;
  opacity: 0.6;
}

.dt-like__icon {
  font-size: 15px;
  line-height: 1;
}

.dt-comments {
  padding-top: 20px;
}

.dt-comments__title {
  margin: 0 0 14px;
  font-size: 16px;
  font-weight: 600;
  color: var(--dt-text);
}

.dt-comments__count {
  margin-left: 4px;
  font-size: 13px;
  font-weight: 400;
  color: var(--dt-text-weak);
}

.dt-composer {
  display: flex;
  gap: 12px;
  padding-bottom: 18px;
  border-bottom: 1px solid var(--dt-line);
}

.dt-composer__body {
  flex: 1;
  min-width: 0;
}

/* 发布按钮右对齐：输入框占满宽度时，按钮跟文字左对齐会显得掉在中间。 */
.dt-composer__foot {
  display: flex;
  justify-content: flex-end;
  padding-top: 8px;
}

.dt-comments__body {
  /* 不再预留 160px：那条 min-height 是为「评论还很少」时的观感定的，
     实际效果是最后一条评论下面挂着一大片空白，像没加载完。
     空态由 el-empty 自己撑高度。 */
  min-height: 0;
}

.dt-item {
  display: flex;
  gap: 12px;
  padding: 16px 0;
  border-bottom: 1px solid var(--dt-line);
}

/* 锚点失效的内联提示。用浅底小条而不是红字：
   评论没了不算错误，用户只是走丢了一次，语气不必像报错。 */
.dt-anchor-hint {
  margin: 0 0 6px;
  padding: 10px 12px;
  border-radius: var(--fs-radius-sm);
  background: var(--fs-bg);
  color: var(--fs-text-2);
  font-size: 13px;
}

.dt-item__body {
  flex: 1;
  min-width: 0;
}

.dt-item__head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 4px;
}

.dt-item__name {
  font-size: 13px;
  font-weight: 600;
  color: var(--fs-text-2);
}

.dt-item__time {
  font-size: 12px;
  color: var(--dt-text-weak);
  /* 时间不参与压缩也不折行：窄屏时该让「回复 @某人」去省略（见 dt-reply__to）。 */
  flex: none;
  white-space: nowrap;
}

.dt-item__text {
  margin: 0 0 6px;
  font-size: 14px;
  line-height: 1.7;
  color: var(--dt-text);
  /* 评论里也可能出现长链接或连续英文串，同样要能断行。 */
  white-space: pre-wrap;
  word-break: break-word;
}

.dt-item__ops {
  display: flex;
  gap: 14px;
}

/* 回复/删除做成纯文字按钮：它们是次级动作，
   做成 el-button 会让一屏十几个按钮，喧宾夺主。 */
.dt-op {
  padding: 0;
  border: none;
  background: none;
  color: var(--dt-text-weak);
  font-size: 12px;
  cursor: pointer;
}

.dt-op:hover {
  color: var(--dt-primary);
}

.dt-reply-box {
  margin-top: 10px;
}

.dt-reply-box__foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 6px;
}

/* 回复区整体浅灰底 + 左缩进，一眼看出从属于上面那条根评论。
   用背景色而不是只缩进：缩进在窄屏下太不明显了。 */
.dt-replies {
  margin-top: 12px;
  padding: 10px 12px;
  border-radius: 6px;
  background: var(--fs-surface-hover);
}

.dt-reply {
  display: flex;
  gap: 10px;
  padding: 6px 0;
}

.dt-reply + .dt-reply {
  border-top: 1px solid var(--fs-line-soft);
}

/* 从通知定位过来的那条评论闪一下底色，让人一眼看到落点。
   用一次性动画而不是常驻 class：两秒后自动消失，
   常驻的话这条评论会被永久标成「新的」。
   底色用 primary-weak 而不是更淡的 faint：faint 在白卡上几乎看不出来，
   提示要能「闪一下」，不是装饰。
   不做水平内缩也不加圆角：那会让文字重新折行，整块区域抖一下。 */
.dt-item.is-anchor,
.dt-reply.is-anchor {
  animation: dt-anchor-flash 1.8s ease-out 1;
}

@keyframes dt-anchor-flash {
  0%,
  40% {
    background-color: var(--fs-primary-weak);
  }

  100% {
    background-color: transparent;
  }
}

.dt-reply__to {
  font-size: 12px;
  color: var(--dt-primary);
  /* 手机端「回复 @某某」过长时只让这一段省略，而不是把后面的时间挤到第二行、
     再把「8 小时前」拆成「8 小时 / 前」。 */
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dt-replies__more {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--dt-text-weak);
}

.dt-foot {
  display: flex;
  justify-content: center;
  padding: 18px 0 8px;
}
</style>
