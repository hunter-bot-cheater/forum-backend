<template>
  <div class="composer">
    <!-- 顶部一行只放「返回」和页面名：写文章是单任务页面，
         不需要导航和侧栏，视线应该直接落到标题输入框上。 -->
    <div class="composer__head">
      <button type="button" class="composer__back" @click="leave">← 返回</button>
      <span class="composer__label">写文章</span>
    </div>

    <div class="fs-card composer__card">
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

      <div class="composer__foot">
        <!-- 左侧实时统计，右侧操作。
             这里的「字」按字符算，与后端上限（字节）不是同一口径，
             所以超限时不显示剩余字数，而是直接说明按字节超了。 -->
        <div class="composer__stat" :class="{ 'is-over': overLimit }">
          <template v-if="overLimit">
            正文超出上限（约 1.6 万汉字），请删减后再发布
          </template>
          <template v-else>
            {{ contentLength }} 字<span class="composer__hint"> · Ctrl + Enter 发布</span>
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
            发布
          </el-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox, type InputInstance } from 'element-plus'
import { createArticle } from '@/api/article'
import { useArticleStore } from '@/store/modules/article'

const router = useRouter()
const articleStore = useArticleStore()

// 与后端 internal/article/service.go 的两个上限保持一致：
// 标题 varchar(128) 按字符计，正文 text 按字节计（50000 字节）。
// 前端不重复做「是否包含非法字符」这类判断——那是后端的事，
// 这里只拦「一定过不了」的长度，避免用户写满一屏才被 400 打回来。
const titleLimit = 128
const contentByteLimit = 50000

const title = ref('')
const content = ref('')
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

// 两个字段都有内容、正文没超长才允许发布。
const canSubmit = computed(() => {
  return title.value.trim() !== '' && hasContent.value && !overLimit.value
})

// 有内容未发布时，离开页面要确认一次。
// 误点导航或刷新都会丢掉刚写的东西，而写文章往往是整页最费时的操作。
const dirty = computed(() => title.value.trim() !== '' || content.value.trim() !== '')

onBeforeRouteLeave(async () => {
  if (submitted.value || !dirty.value) {
    return true
  }
  try {
    await ElMessageBox.confirm('内容还没有发布，离开后不会保存，确定离开？', '离开页面', {
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

// 进页面自动聚焦标题：写文章的第一动作永远是敲标题，
// 多一次点击才能开始打字是没必要的。
// 窄屏不聚焦：手机上自动聚焦会立刻弹出软键盘，把刚打开的页面顶掉一半。
onMounted(() => {
  if (window.innerWidth > 720) {
    titleRef.value?.focus()
  }
  window.addEventListener('keydown', onKeydown)
})

onUnmounted(() => {
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
    const created = await createArticle(title.value.trim(), content.value.trim())
    // 先让列表失效再跳转：回到列表时才会重拉第一页，
    // 否则用户看到的是发布前的旧列表，会怀疑自己没发出去。
    articleStore.invalidate()
    submitted.value = true
    ElMessage.success('发布成功')
    // replace 而不是 push：发布完成后不该还能「后退回到编辑页」，
    // 那样会看到一个空白编辑器，再点发布就发出第二篇。
    await router.replace('/articles/' + String(created.id))
  } catch {
    // 失败提示已由请求拦截器统一弹出（400 是标题/正文不合法，401 会跳登录）。
    // 这里不重复弹，也不清空输入——用户改几个字就能重新提交。
  } finally {
    submitting.value = false
  }
}

// 取消与返回都走这里，而不是让按钮自己调 router：
// 两条路径都要经过路由守卫的「未发布确认」，统一成一个函数就不会漏。
function leave() {
  void router.push('/articles')
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
