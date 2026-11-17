<template>
  <div class="composer">
    <!-- 顶部一行只放「返回」和页面名：写作是单任务页面，
         不需要导航和侧栏，视线应该直接落到标题输入框上。 -->
    <div class="composer__head">
      <button type="button" class="composer__back" @click="leave">← 返回</button>
      <span class="composer__label">{{ isEdit ? '编辑文章' : '写文章' }}</span>
    </div>

    <div v-if="loading" v-loading="true" class="composer__loading" />

    <div v-else class="fs-card composer__card">
      <!-- 标题与正文都不用 el-form 包一层：只有两个字段、没有联动校验，
           表单组件在这里只会多出一圈 label 和 margin。 -->
      <el-input
        ref="titleRef"
        v-model="title"
        class="composer__title"
        placeholder="请输入标题"
        :maxlength="titleLimit"
        :disabled="submitting"
      />

      <div class="composer__divider"></div>

      <el-input
        v-model="content"
        class="composer__body"
        type="textarea"
        :autosize="{ minRows: 14 }"
        resize="none"
        placeholder="写点什么…支持换行，正文里的空行会在详情页原样保留"
        :disabled="submitting"
      />

      <!-- 标签放在正文下面：它是「归类」动作，写的时候想不到要填什么，
           写完回头看一遍才知道该给哪几个。

           用裸输入框而不是带下拉的选择器：下拉里的候选要靠一次额外请求
           拉全站标签，而它弹出来正好挡住正文最后几行；真正有用的候选
           其实也猜不到（谁知道别人把「MySQL」写成「mysql」还是「数据库」）。
           回车或逗号确认，标签右侧的 × 删掉；一个都不填也没关系，
           发布时后端会让大模型起一个（见 config.yaml 的 llm 段）。 -->
      <div class="composer__tags">
        <div class="composer__tags-box">
          <span v-for="(name, index) in tags" :key="name" class="composer__tag">
            {{ name }}
            <button
              class="composer__tag-x"
              type="button"
              :disabled="submitting"
              :aria-label="'删除标签 ' + name"
              @click="removeTag(index)"
            >
              ×
            </button>
          </span>
          <input
            v-model="tagDraft"
            class="composer__tag-input"
            type="text"
            maxlength="32"
            :disabled="submitting || tags.length >= tagLimit"
            :placeholder="tags.length >= tagLimit ? '最多 3 个' : '加标签，回车确认'"
            @keydown.enter.prevent="confirmTag($event)"
            @keydown="onTagKeydown"
            @blur="confirmTag()"
          />
        </div>
        <span class="composer__tags-hint">{{ tags.length }}/{{ tagLimit }}</span>
      </div>
      <!-- 提示只在「还没填」时出现，填了第一枚就撤掉，不占着位置。
           有模型建议时让位给建议行，两行小字叠在一起会互相抵消。 -->
      <p v-if="tags.length === 0 && !isEdit && !suggestion" class="composer__tags-tip">
        不填也没关系，发布后会自动补一个标签
      </p>
      <!-- 模型建议。写够一段才出现，采用或忽略都不影响发布。 -->
      <p v-if="suggestion" class="composer__suggest">
        要不要加「{{ suggestion }}」？
        <button type="button" class="composer__suggest-btn" @click="applySuggestion">采用</button>
        <button type="button" class="composer__suggest-btn" @click="suggestion = ''">不用</button>
      </p>

      <div class="composer__foot">
        <!-- 左侧实时统计，右侧操作。
             这里的「字」按字符算，与后端上限（字节）不是同一口径，
             所以超限时不显示剩余字数，而是直接说明按字节超了。 -->
        <div class="composer__stat" :class="{ 'is-over': overLimit }">
          <template v-if="overLimit">
            正文超出上限（约 1.6 万汉字），请删减后再发布
          </template>
          <template v-else>
            {{ contentLength }} 字<span class="composer__hint"> · Ctrl + Enter {{ isEdit ? '保存' : '发布' }}</span>
          </template>
        </div>

        <div class="composer__ops">
          <el-button :disabled="submitting" @click="leave">取消</el-button>
          <el-button
            type="primary"
            :loading="submitting"
            :disabled="!canSubmit"
            @click="submit"
          >
            {{ isEdit ? '保存' : '发布' }}
          </el-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox, type InputInstance } from 'element-plus'
import { createArticle, getArticle, suggestTag, updateArticle } from '@/api/article'
import { useArticleStore } from '@/store/modules/article'
import { useUserStore } from '@/store/modules/user'

const route = useRoute()
const router = useRouter()
const articleStore = useArticleStore()
const user = useUserStore()

// 同一个组件承担两种用法：/write 写新文章，/articles/:id/edit 改旧文章。
// 合成一页而不是拆两份：表单、字数统计、离开确认、快捷键完全相同，
// 拆开后就是两份一样的模板各自演化，改一处忘一处。
const articleId = computed(() => {
  const raw = Number(route.params.id)
  if (Number.isFinite(raw) && raw > 0) {
    return raw
  }
  return 0
})
const isEdit = computed(() => articleId.value > 0)

// loading 只服务编辑态：拉原文期间先不渲染表单，
// 否则用户会看到一屏空白输入框，并在内容回填之前就开始打字。
const loading = ref(false)

// original 是进入编辑页那一刻的原文，用来判断「到底改没改」。
// 创建态没有原文，dirty 退回成「有没有输入内容」。
const original = ref({ title: '', content: '', tags: [] as string[] })

// 与后端 internal/article/service.go 的两个上限保持一致：
// 标题 varchar(128) 按字符计，正文 text 按字节计（50000 字节）。
// 前端不重复做「是否包含非法字符」这类判断——那是后端的事，
// 这里只拦「一定过不了」的长度，避免用户写满一屏才被 400 打回来。
const titleLimit = 128
const contentByteLimit = 50000
// 标签数量上限与 internal/article/tag.go 的 maxTagsPerArticle 一致。
// 两边都写死 3 是有意的：前端要即时拦住第四个，后端要防绕过前端直接调接口。
const tagLimit = 3

const title = ref('')
const content = ref('')
// tags 是已确认的标签名，tagDraft 是输入框里还没回车的那半截。
// 两者分开：输入框清空不代表标签已加，回车（或失焦）才算，用户还有反悔的余地。
const tags = ref<string[]>([])
const tagDraft = ref('')
// suggestion 是模型给的候选标签，用户点「采用」才会进 tags。
// 单独一份而不是直接塞进输入框：建议出现在这里时用户可能还在打字，
// 往输入框里插字会打断他。suggestAsked 保证每次进页面只问一次模型。
const suggestion = ref('')
const suggestAsked = ref(false)
const submitting = ref(false)
// submitted 用来区分「发布成功后跳转」和「用户主动离开」：
// 前者不能再弹「内容还没发布」，否则每次发布成功都会被自己的确认框拦住。
const submitted = ref(false)
const titleRef = ref<InputInstance>()

// 只建一个实例并在下面复用：TextEncoder 是无状态的，
// 放进 computed 里每次重算都 new 一个，等于每敲一个字就分配一次对象。
const encoder = new TextEncoder()

// 正文按字节算，与后端一致。中文一个字 3 字节，
// 所以 50000 字节大约是一万六千多汉字——提示文案里直接说清楚，
// 否则用户看到「50000」会以为是五万个字。
const contentBytes = computed(() => encoder.encode(content.value).length)
// 显示的字数不去空白：用户敲下的每个字符都该算进去，
// 否则在行尾敲空格时数字不动，会让人以为输入丢字。
const contentLength = computed(() => content.value.length)
const overLimit = computed(() => contentBytes.value > contentByteLimit)

// 判空与计数分开：纯空格不算内容（不然后端 trim 完会 400），
// 但计数时它仍是用户敲进去的字符。
const hasContent = computed(() => content.value.trim() !== '')

// dirty 的含义随模式变：
//   - 写新文章：输入框里有内容就算「有还没保存的东西」
//   - 编辑旧文章：要和原文真的不同才算，否则点进来什么都不做，
//     一出页面就弹「还没保存」，属于打扰
const dirty = computed(() => {
  if (isEdit.value) {
    return (
      title.value !== original.value.title ||
      content.value !== original.value.content ||
      !sameTags(tags.value, original.value.tags)
    )
  }
  return title.value.trim() !== '' || content.value.trim() !== '' || tags.value.length > 0
})

// sameTags 比较两份标签是否一致。
//
// 顺序敏感：用户把「后端、数据库」调成「数据库、后端」也算改过。
// 存储层按标签创建顺序返回，顺序变了详情页的展示顺序就变了，
// 当成「没改」会让保存按钮在用户眼里失灵。
function sameTags(a: string[], b: string[]) {
  if (a.length !== b.length) {
    return false
  }
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) {
      return false
    }
  }
  return true
}

// 两个字段都有内容、正文没超长才允许提交。
const canSubmit = computed(() => {
  if (title.value.trim() === '' || !hasContent.value || overLimit.value) {
    return false
  }
  // 编辑态一个字符都没改，保存按钮保持禁用：
  // 发一次「把原文再写一遍」的请求没有意义，按钮状态本身也在告诉用户「还没动手」。
  if (isEdit.value && !dirty.value) {
    return false
  }
  return true
})

onBeforeRouteLeave(async () => {
  if (submitted.value || !dirty.value) {
    return true
  }
  // 两种模式的确认文案不同：写新文章丢的是刚写的内容，编辑丢的是这次的修改。
  const tip = isEdit.value ? '修改还没有保存，离开后会丢失，确定离开？' : '内容还没有发布，离开后不会保存，确定离开？'
  try {
    await ElMessageBox.confirm(tip, '离开页面', {
      confirmButtonText: '离开',
      cancelButtonText: '继续编辑',
      type: 'warning',
    })
    return true
  } catch {
    // 点了「继续编辑」或按 Esc：留在页面，输入的内容都还在。
    return false
  }
})

// 进页面自动聚焦标题：写作的第一动作永远是敲标题，
// 多一次点击才能开始打字是没必要的。
// 窄屏不聚焦：手机上自动聚焦会立刻弹出软键盘，把刚打开的页面顶掉一半。
onMounted(async () => {
  if (isEdit.value) {
    loading.value = true
    try {
      const article = await getArticle(articleId.value)
      // 别人的文章拉得到内容却改不了（后端一律 404）。提前拦下来，
      // 免得用户改完一大段才被告知没权限。author 缺失时不做判断，交给后端兜底。
      if (user.userId > 0 && article.author && article.author.id !== user.userId) {
        ElMessage.warning('只能编辑自己的文章')
        await router.replace('/articles/' + String(articleId.value))
        return
      }
      title.value = article.title
      content.value = article.content
      // 两份标签必须各自独立：tags 之后会被 push / splice 修改，
      // 而 original 是「进来那一刻的原文」，用来判断改没改。
      // 若两者指向同一个数组，加一枚标签会把 original 也一起改掉，
      // dirty 恒为 false —— 编辑态下「只改标签」的保存按钮会一直是灰的。
      tags.value = [...article.tags]
      original.value = {
        title: article.title,
        content: article.content,
        tags: [...article.tags],
      }
    } catch {
      // 404 与网络错误都已由拦截器弹过提示，这里不再弹第二个框。
      // 拿不到原文就没法编辑，退回详情页比留一个空表单安全。
      await router.replace('/articles/' + String(articleId.value))
      return
    } finally {
      loading.value = false
    }
  }

  if (window.innerWidth > 720) {
    titleRef.value?.focus()
  }
  window.addEventListener('keydown', onKeydown)
})

// confirmTag 把输入框里的字变成一枚标签。
//
// 浏览器原生输入法还在组词时按回车不能算确认：那一下是「选字」，
// 直接吞掉会让输入法用户的词永远打不出来，所以先看 event.isComposing。
// 失焦时也调一次（不传 event），把用户输了一半就点发布的情况兜住。
function confirmTag(event?: KeyboardEvent) {
  if (event && event.isComposing) {
    return
  }
  const name = tagDraft.value.trim()
  tagDraft.value = ''
  if (name === '' || tags.value.length >= tagLimit) {
    return
  }
  // 大小写不同视为同一个标签：后端也会去重，但前端先拦住能避免
  // 出现「3/3 里两个其实是一个」，用户还得自己删一个。
  const exists = tags.value.some((item) => item.toLowerCase() === name.toLowerCase())
  if (exists) {
    return
  }
  tags.value.push(name)
}

// onTagKeydown 把中英文逗号也当成确认键：
// 输入「后端, 数据库」比敲两次回车顺手，中文输入下更是只会打出「，」。
// 回车已经由模板上的 @keydown.enter.prevent 处理，这里不重复。
function onTagKeydown(event: KeyboardEvent) {
  if (event.key === ',' || event.key === '，') {
    event.preventDefault()
    confirmTag(event)
  }
}

// removeTag 删掉第 index 枚标签。编辑态允许删到 0：标签是整体替换的，
// 一个都不剩就是「这篇文章不属于任何分类」，后端不会自动补。
function removeTag(index: number) {
  tags.value.splice(index, 1)
}

// 标签预热：写到标题加一小段正文，就问一次模型要个建议。
//
// 目的有两个。一是把模型的冷启动挪出发布链路，发布时要么已经采用建议、
// 要么交给后台补，作者都不用等；二是建议摆在眼前，作者能改能拒，
// 比发布后偷偷补一个更透明。
//
// 只问一次：每问一次都消耗额度，而同一篇内容问第二遍答案也差不多。
// 填了标签就不再问，用户已经做过决定。编辑态不问，标签是原文带来的。
let suggestTimer = 0
watch([title, content], () => {
  // 先撤掉上一次排的定时器，再看这次还要不要问。
  // 条件已经不满足时（删短了正文、自己填了标签、已经问过）还留着定时器的话，
  // 1.2 秒后仍会发一次模型请求，白花一次配额。
  window.clearTimeout(suggestTimer)
  if (suggestAsked.value || isEdit.value || tags.value.length > 0) {
    return
  }
  if (title.value.trim() === '' || content.value.trim().length < 20) {
    return
  }
  // 打字期间不停重排，停手 1.2 秒才真的发请求，否则每敲一个字都是一次模型调用。
  suggestTimer = window.setTimeout(() => {
    void fetchSuggestion()
  }, 1200)
})

// fetchSuggestion 直接调接口，失败一律静默。
// 建议是附加值，模型超时、配额用完都不该在写作页弹一个红条打断思路。
async function fetchSuggestion() {
  suggestAsked.value = true
  try {
    const resp = await suggestTag(title.value.trim(), content.value)
    // 等待期间用户可能已经自己填了标签，这时建议没有意义，直接丢弃。
    if (resp.tag !== '' && tags.value.length === 0) {
      suggestion.value = resp.tag
    }
  } catch {
    // 拦截器已经处理过提示，这里只需要让流程继续。
  }
}

// applySuggestion 把建议变成一枚正式标签。
// 转交给 confirmTag：去重、数量上限、清空输入框这些规则与手输共用一份，
// 否则「采用建议」和「手打标签」迟早会走出两种结果。
function applySuggestion() {
  tagDraft.value = suggestion.value
  suggestion.value = ''
  confirmTag()
}

onUnmounted(() => {
  window.clearTimeout(suggestTimer)
  window.removeEventListener('keydown', onKeydown)
})

// Ctrl / Cmd + Enter 发布。绑在 window 上而不是 textarea 的 keydown：
// 焦点在标题框时也该能用，而标题框是 el-input，拿不到统一的事件。
function onKeydown(event: KeyboardEvent) {
  if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') {
    event.preventDefault()
    void submit()
  }
}

async function submit() {
  // 双击防重：后端没有幂等键，连点两次会真的发两篇。
  // canSubmit 已经保证了内容合法，这里只看「是不是正在发」。
  if (submitting.value || !canSubmit.value) {
    return
  }
  submitting.value = true
  try {
    let targetID = articleId.value
    if (isEdit.value) {
      const updated = await updateArticle(articleId.value, title.value.trim(), content.value.trim(), tags.value)
      targetID = updated.id
      ElMessage.success('已保存')
    } else {
      const created = await createArticle(title.value.trim(), content.value.trim(), tags.value)
      targetID = created.id
      ElMessage.success('发布成功')
    }
    // 先让列表失效再跳转：回到列表时才会重拉第一页。
    // 编辑也要失效——列表里显示的是标题与摘要，改完不刷新会看到旧标题。
    articleStore.invalidate()
    submitted.value = true
    // replace 而不是 push：提交完成后不该还能「后退回到编辑页」，
    // 那样会看到一个内容已经旧了的编辑器，再点保存又覆盖一次。
    await router.replace('/articles/' + String(targetID))
  } catch {
    // 失败提示已由请求拦截器统一弹出（400 是标题/正文不合法，401 会跳登录，
    // 404 是文章不存在或不是本人的）。这里不重复弹，也不清空输入。
  } finally {
    submitting.value = false
  }
}

// 取消与返回都走这里，而不是让按钮自己调 router：
// 两条路径都要经过路由守卫的「未保存确认」，统一成一个函数就不会漏。
//
// 用 back 而不是 push 回详情页：编辑页是从详情 push 进来的，
// 再 push 一层详情会让历史变成「详情 → 编辑 → 详情」，
// 用户在详情点「← 返回」又落回编辑页，两个页面来回跳，看起来像死循环。
// 直接打开编辑地址（没有上一页）时 back 会离开站点，这时改用 replace，
// 退回对应的落地页：编辑态回详情，新建态回列表。
function leave() {
  const back = window.history.state ? window.history.state.back : null
  if (typeof back === 'string' && back !== '') {
    router.back()
    return
  }
  if (isEdit.value) {
    void router.replace('/articles/' + String(articleId.value))
    return
  }
  void router.replace('/articles')
}
</script>

<style scoped>
/* 写作页比列表页窄：一行文字超过 40 个汉字后，眼睛回行容易串行。
   1120px 的通用容器宽度是给双栏列表用的，这里单独收窄到 820。 */
.composer {
  max-width: 820px;
  margin: 0 auto;
}

.composer__head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
  padding: 0 2px;
}

/* 返回做成纯文本：这是导航动作，不是页内操作，
   做成按钮会和右下角的「发布」抢注意力。 */
.composer__back {
  padding: 4px 0;
  border: none;
  background: none;
  color: var(--fs-text-3);
  font-size: 13px;
  cursor: pointer;
}

.composer__back:hover {
  color: var(--fs-text-1);
}

.composer__label {
  font-size: 13px;
  color: var(--fs-text-3);
}

.composer__card {
  padding: 28px 28px 20px;
}

/* 编辑态拉原文时的占位高度：给一个和卡片相近的高度，
   避免加载完成后页面整体跳一下。 */
.composer__loading {
  height: 420px;
}

/* 标题输入框去掉 el-input 的默认边框与内边距：
   写文章页的标题应该看起来像「纸上的第一行大字」，不是一个表单控件。
   覆盖的是 el-input 内部的 wrapper，所以要 deep。 */
.composer__title :deep(.el-input__wrapper) {
  padding: 0;
  box-shadow: none;
  background: transparent;
}

.composer__title :deep(.el-input__inner) {
  height: 44px;
  font-size: 24px;
  font-weight: 700;
  color: var(--fs-text-1);
}

.composer__title :deep(.el-input__inner::placeholder) {
  font-weight: 600;
  color: #c0c4cc;
}

.composer__divider {
  height: 1px;
  margin: 14px 0 18px;
  background: var(--fs-line-soft);
}

/* 正文同样去边框。min-height 给足一屏，避免只有一个小方框、
   让人觉得「只能写两行」。
   max-height 是必需的：autosize 会按内容把内联 height 一路加高，
   正文写到几千字时卡片会被撑到几万像素，底部操作栏和字数提示全被推出屏幕，
   用户以为按钮没了。限高之后超出部分在框内滚动，操作栏始终在视线里。 */
/* 底部留一点内边距：滚到最底时最后一行不会被文本框下边缘切掉半行。 */
.composer__body :deep(.el-textarea__inner) {
  padding: 0 0 10px;
  min-height: 360px !important;
  max-height: 56vh;
  overflow-y: auto;
  box-shadow: none;
  background: transparent;
  font-size: 15px;
  line-height: 1.9;
  color: var(--fs-text-1);
}

.composer__body :deep(.el-textarea__inner::placeholder) {
  color: #c0c4cc;
}

/* 标签行靠左，与正文左边界对齐；不占满整行——它是次要字段，
   占满会让人以为必须填。 */
.composer__tags {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 14px;
}

/* 标签与输入框同处一个描边盒子里，像一个多选输入框。
   宽度上限 420：标签最多 3 个，写满也就三枚窄胶囊加一截输入位，
   真放不下时在盒子内部换行，不会把右侧的计数挤出去。 */
.composer__tags-box {
  display: flex;
  flex: 0 1 420px;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  min-width: 240px;
  padding: 3px 8px;
  border: 1px solid var(--fs-line);
  border-radius: var(--fs-radius-sm);
  background: var(--fs-surface);
  transition: border-color 0.15s;
}

/* 焦点在内部任意位置时整圈变主色：里面只有一个裸 input，
   它自己的 outline 已经被去掉，不给盒子反馈会看不出「正在输入」。 */
.composer__tags-box:focus-within {
  border-color: var(--fs-primary);
}

.composer__tag {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 2px 4px 2px 8px;
  border-radius: 10px;
  background: var(--fs-primary-weak);
  color: var(--fs-primary);
  font-size: 12.5px;
  line-height: 18px;
}

.composer__tag-x {
  padding: 0 2px;
  border: none;
  background: none;
  color: inherit;
  font-size: 13px;
  line-height: 1;
  cursor: pointer;
  opacity: 0.6;
  transition: opacity 0.15s;
}

.composer__tag-x:hover {
  opacity: 1;
}

/* 输入框自己不要边框：外框由 .composer__tags-box 提供，
   两层边框套在一起会变成双线。 */
.composer__tag-input {
  flex: 1;
  min-width: 110px;
  padding: 2px 0;
  border: none;
  outline: none;
  background: none;
  color: var(--fs-text-1);
  font-size: 13px;
  line-height: 20px;
}

.composer__tag-input::placeholder {
  color: var(--fs-text-3);
}

.composer__tag-input:disabled {
  cursor: not-allowed;
}

.composer__tags-hint {
  font-size: 12px;
  color: var(--fs-text-3);
}

/* 自动标签的说明。字号比正文小一档、颜色最浅，
   它是一句补充说明，不该抢走「发布」按钮上方的注意力。 */
.composer__tags-tip {
  margin-top: 6px;
  font-size: 12px;
  color: var(--fs-text-3);
}

/* 模型建议行。与上面那句提示同字号同颜色，靠左侧排列，
   两个小胶囊只是「一句话里的两个动作」，不做成主按钮。 */
.composer__suggest {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
  font-size: 12px;
  color: var(--fs-text-3);
}

.composer__suggest-btn {
  padding: 2px 10px;
  border: 1px solid var(--fs-line);
  border-radius: 999px;
  background: none;
  color: var(--fs-primary);
  font-size: 12px;
  line-height: 18px;
  cursor: pointer;
}

.composer__suggest-btn:hover {
  background: var(--fs-primary-weak);
}

/* 操作栏与正文之间用一条浅线分隔，不描整圈边框。
   左右两端对齐：统计靠左随内容变化，按钮靠右固定在视觉落点。 */
.composer__foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-top: 18px;
  padding-top: 16px;
  border-top: 1px solid var(--fs-line-soft);
}

.composer__stat {
  font-size: 13px;
  color: var(--fs-text-3);
}

/* 超限用点赞那支暖红：整站只有它的语义是「需要注意」。 */
.composer__stat.is-over {
  color: var(--fs-like);
}

.composer__hint {
  color: var(--fs-text-3);
}

.composer__ops {
  display: flex;
  gap: 8px;
  flex: none;
}

@media (max-width: 720px) {
  .composer__card {
    padding: 20px 18px 16px;
  }

  .composer__title :deep(.el-input__inner) {
    font-size: 20px;
  }

  /* 窄屏把快捷键提示藏掉：手机没有 Ctrl 键，留着是噪音。 */
  .composer__hint {
    display: none;
  }
}
</style>
