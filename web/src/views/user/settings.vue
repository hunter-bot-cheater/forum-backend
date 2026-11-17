<template>
  <div class="settings">
    <!-- 顶部只放返回与页面名：编辑资料是单任务页面，
         多一行导航只会把视线从表单上挪开。与写文章页同一套头部。 -->
    <div class="settings__head">
      <button type="button" class="settings__back" @click="goBack">← 返回</button>
      <span class="settings__label">编辑资料</span>
    </div>

    <div v-if="loading" v-loading="true" class="settings__loading" />

    <!-- 拉不到自己的资料就不给表单：没有初始值就无法判断「改了哪几项」，
         稀里糊涂保存会把已有昵称、简介一起覆盖成空。 -->
    <el-empty v-else-if="!loaded" description="资料加载失败，请稍后重试" :image-size="96">
      <el-button type="primary" @click="load">重试</el-button>
    </el-empty>

    <div v-else class="fs-card settings__card">
      <section class="field">
        <div class="field__label">头像</div>
        <div class="field__body">
          <!-- 预览跟着 avatarURL 变：传完立刻换成新图，不用等保存。
               监听 error 是必须的：el-avatar 加载失败时会把 img 整个摘掉、
               静默改回首字兜底，页面上没有任何变化，用户只会以为「保存没生效」。
               只剩历史遗留的外链会走到这条提示（对方站点开防盗链时带 Referer 直接 403）。 -->
          <el-avatar
            :size="64"
            :src="previewAvatar"
            class="field__avatar"
            @error="onAvatarError"
          >
            {{ initial }}
          </el-avatar>
          <div class="field__control">
            <!-- 只留上传：贴外链看着灵活，实际大多数人都没有可外链的图床，
               填进去十个有八个是防盗链地址，最后都变成「头像没生效」的困惑。 -->
            <div class="field__row">
              <el-button :loading="uploading" :disabled="saving" @click="pickFile">
                上传图片
              </el-button>
            </div>
            <!-- 隐藏的原生 file 控件：el-upload 会自带一套上传队列、进度条和文件列表，
                 这里只需要「选一个文件」，那套 UI 反而要一条条关掉。 -->
            <input
              ref="fileInput"
              type="file"
              accept="image/jpeg,image/png,image/gif,image/webp"
              class="field__file"
              @change="onFilePicked"
            />
            <p v-if="uploadNote" class="field__note">{{ uploadNote }}</p>
            <p class="field__hint" :class="{ 'is-error': avatarBroken }">
              {{ avatarHint }}
            </p>
          </div>
        </div>
      </section>

      <section class="field">
        <div class="field__label">昵称</div>
        <div class="field__body">
          <div class="field__control">
            <el-input
              v-model="nickname"
              :maxlength="nicknameLimit"
              show-word-limit
              :placeholder="nicknamePlaceholder"
              :disabled="saving"
            />
            <p class="field__hint">留空就显示用户名；昵称可以随时改，用户名不行。</p>
          </div>
        </div>
      </section>

      <section class="field">
        <div class="field__label">用户名</div>
        <div class="field__body">
          <div class="field__control">
            <span class="field__readonly">{{ username }}</span>
            <p class="field__hint">用户名是登录凭据，暂不支持修改。</p>
          </div>
        </div>
      </section>

      <section class="field">
        <div class="field__label">简介</div>
        <div class="field__body">
          <div class="field__control">
            <el-input
              v-model="bio"
              type="textarea"
              :autosize="{ minRows: 3, maxRows: 6 }"
              resize="none"
              :maxlength="bioLimit"
              show-word-limit
              placeholder="一句话介绍自己，会显示在你的主页上"
              :disabled="saving"
            />
          </div>
        </div>
      </section>

      <!-- 操作栏：左侧状态、右侧按钮，与写文章页的底部条一致。
           状态文字不只是装饰——按钮禁用时得让人知道为什么不能点。 -->
      <div class="settings__foot">
        <span class="settings__stat">{{ statusText }}</span>
        <div class="settings__ops">
          <el-button :disabled="saving || uploading" @click="cancel">取消</el-button>
          <el-button type="primary" :loading="saving" :disabled="!canSave" @click="save">
            保存
          </el-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { updateMe, type ProfilePatch, type UserProfile } from '@/api/user'
import { uploadAvatar } from '@/api/upload'
import { useUserStore } from '@/store/modules/user'
import { initials } from '@/utils/format'
import { goBack as goBackTo } from '@/utils/nav'

// 编辑资料页：改头像、昵称、简介三样，用户名只读。
// 只走一次 PUT /users/me，成功后用返回值回填本地与该页基线。
const router = useRouter()
const user = useUserStore()

// 上限与后端 internal/user/profile.go 的常量一致：
// 超了后端会 400，前端先拦住，用户不用提交一次才知道。
const nicknameLimit = 32
const bioLimit = 200

const loading = ref(false)
// loaded 表示拿到过自己的资料，可以显示表单。
// 不用 user.profile 直接判断：页面可能先从 store 拿到旧值，再被 fetchMe 刷新。
const loaded = ref(false)

const nickname = ref('')
const avatarURL = ref('')
const bio = ref('')
const username = ref('')

// baseline 是最近一次「已知与库里一致」的值，用来判断有没有改动。
// 用对象而不是一个 dirty 布尔值：保存成功后要把基线整体替换成新值，
// 只重置布尔值的话，改回原值再改出去就判断不出差异了。
const baseline = ref({ nickname: '', avatarURL: '', bio: '' })

const saving = ref(false)

// previewAvatar 只在确实有地址时才交给 el-avatar，否则返回 undefined 走首字兜底：
// 地址已经不由用户手输，只可能是空串（没设过头像）或服务端给的值。
const previewAvatar = computed(() => {
  const v = avatarURL.value.trim()
  if (v !== '') {
    return v
  }
  return undefined
})

// avatarBroken 表示「这个地址的图真的加载不出来」。
//
// el-avatar 的失败处理是静默的：它把 <img> 摘掉、渲染回默认插槽里的首字，
// 外观上和「本来就没设头像」完全一样。用户看到的就是「头像没变」。
// 现在头像只能上传，能走到这里的只剩两种老数据：以前填的外链被对方站点封了，
// 或者当年上传的文件被清理掉了。这一位就是给那种情况留一句人话。
const avatarBroken = ref(false)

// 地址一变就先清掉失败标记：上一次的失败不该一直挂着，
// 也不能等新的加载结果——el-avatar 只在失败时发事件，成功不回调，
// 不清的话重新上传成功后提示会一直留着。
watch(previewAvatar, () => {
  avatarBroken.value = false
})

function onAvatarError() {
  avatarBroken.value = true
}

// ---------------- 上传 ----------------

// uploading 期间禁用输入与保存：上传完会自动落库，
// 这中间用户再点保存，会用还没更新的地址把刚上传的结果覆盖掉。
const uploading = ref(false)

// uploadNote 显示这次上传的压缩结果（原图多大、压完多大）。
// 不显示的话，用户看到「头像变了」但不知道服务端动了手脚，
// 下次传一张大图发现变小了，会以为是图坏了。
const uploadNote = ref('')

const fileInput = ref<HTMLInputElement | null>(null)

// pickFile 只是转一下点击：原生 file 控件藏在页面上，样式不可控，
// 用普通按钮触发，外观才能和其它按钮一致。
function pickFile() {
  fileInput.value?.click()
}

// onFilePicked 选完文件立刻上传并落库。
//
// 为什么不在上传后只填地址、让用户再点保存：
// 用户的心智是「选一张图 → 头像变了」。中间多一次保存，
// 他会以为上传没成功，然后在输入框里反复试。
async function onFilePicked(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  // 立刻清空选择：不清的话，同一个文件第二次选不会触发 change 事件，
  // 用户会以为按钮坏了。
  input.value = ''
  if (!file) {
    return
  }

  uploading.value = true
  uploadNote.value = ''
  try {
    const uploaded = await uploadAvatar(file)
    const updated = await updateMe({ avatar_url: uploaded.url })
    user.applyProfile(updated)
    // 只改头像这一项，不调 fill()：页面上可能还有没保存的昵称，
    // 整体回填会把用户正在敲的内容覆盖掉。
    avatarURL.value = uploaded.url
    baseline.value.avatarURL = uploaded.url
    uploadNote.value = `已压缩：${formatBytes(uploaded.original_bytes)} → ${formatBytes(uploaded.bytes)}`
    ElMessage.success('头像已更新')
  } catch {
    // 失败提示由请求拦截器统一弹出（413/415 都会带后端给的具体原因）。
  } finally {
    uploading.value = false
  }
}

// formatBytes 把字节数换成好读的单位。
// 只保留一位小数：这里要传达的是「小了一个数量级」，不是精确到字节。
function formatBytes(n: number) {
  if (n < 1024) {
    return n + ' B'
  }
  if (n < 1024 * 1024) {
    return (n / 1024).toFixed(0) + ' KB'
  }
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}

const initial = computed(() => {
  const name = nickname.value.trim()
  if (name) {
    return initials(name)
  }
  if (username.value) {
    return initials(username.value)
  }
  return '?'
})

const nicknamePlaceholder = computed(() => '不填则显示 ' + username.value)

const avatarHint = computed(() => {
  if (avatarBroken.value) {
    return '当前头像的图片打不开：可能是以前填的外链失效或被对方站点屏蔽了。重新上传一张就行。'
  }
  return '点「上传图片」选本地文件，会自动压缩再保存；没设头像时用名字首字兜底。'
})

const dirty = computed(() => {
  if (nickname.value !== baseline.value.nickname) {
    return true
  }
  if (avatarURL.value.trim() !== baseline.value.avatarURL) {
    return true
  }
  return bio.value !== baseline.value.bio
})

// 上传过程中不允许保存：上传完成会自动落库，这期间点保存
// 会用还没更新的地址把刚上传的结果覆盖回去。
const canSave = computed(() => !saving.value && !uploading.value && dirty.value)

const statusText = computed(() => {
  if (uploading.value) {
    return '正在上传…'
  }
  if (saving.value) {
    return '正在保存…'
  }
  if (avatarBroken.value) {
    return '头像图片打不开'
  }
  if (dirty.value) {
    return '有未保存的修改'
  }
  return '资料已是最新'
})

// fill 用一份资料回填表单，并把基线一起更新。
// 保存成功后也走这里：后端会 trim、可能改大小写，以库里的真值为准，
// 否则页面显示的和实际存下来的会慢慢对不上。
function fill(p: UserProfile) {
  username.value = p.username
  // 昵称看 has_custom_nickname 而不是 nickname：
  // 没设过昵称时接口回的是用户名，直接回填会把用户名钉进表单，
  // 用户保存一次就凭空多出一个「自定义昵称」。
  if (p.has_custom_nickname) {
    nickname.value = p.nickname
  } else {
    nickname.value = ''
  }
  avatarURL.value = p.avatar_url
  bio.value = p.bio
  baseline.value = {
    nickname: nickname.value,
    avatarURL: avatarURL.value.trim(),
    bio: bio.value,
  }
}

// load 取一次自己的资料。store 里已有就直接用，省一次请求；
// 但顶栏那份可能是登录时填的，字段不全（关注计数是 0），
// 所以只在确实没有时才发给网络。
async function load() {
  loading.value = true
  try {
    let p = user.profile
    if (!p) {
      p = await user.fetchMe()
    }
    if (p) {
      fill(p)
      loaded.value = true
    } else {
      loaded.value = false
    }
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!canSave.value) {
    return
  }
  saving.value = true
  try {
    // 三项全传：表单是完整回填的，用户看到什么就提交什么。
    // 只传改动项要额外算差异，收益是省几个字节，不值得。
    const patch: ProfilePatch = {
      nickname: nickname.value,
      avatar_url: avatarURL.value.trim(),
      bio: bio.value,
    }
    const updated = await updateMe(patch)
    // 先同步到 store，顶栏昵称头像立刻变；再回填表单与基线。
    user.applyProfile(updated)
    fill(updated)
    ElMessage.success('资料已更新')
  } catch {
    // 失败提示由请求拦截器统一弹出（400 字段不合法、401 跳登录）。
    // 这里保留输入，改一下就能重试，不清空也不跳页。
  } finally {
    saving.value = false
  }
}

// goBack 优先真的回上一页（从主页点进来就回主页，从文章列表点进来就回列表）；
// 没有上一页（直接打开这个地址）才退回自己的主页。
// 以前这里固定 push 自己的主页：主页的「返回」再 back 回来，
// 两页就会一直互相弹——每点一次都往历史里压一条，看起来就是死循环。
function goBack() {
  const fallback = user.userId > 0 ? '/users/' + String(user.userId) : '/articles'
  goBackTo(router, fallback)
}

// 取消 = 放弃这次修改：先把表单还原成基线，dirty 变 false 再离开，
// 这样路由守卫不会追着问「确定离开吗」。用户点「取消」已经表过态了，
// 再确认一次等于没听懂。顶部的「← 返回」不走这里，那里仍要确认。
function cancel() {
  nickname.value = baseline.value.nickname
  avatarURL.value = baseline.value.avatarURL
  bio.value = baseline.value.bio
  goBack()
}

// 有未保存改动时离开要确认一次。这里只有三个字段，误触的代价不大，
// 但昵称和简介都是手打的内容，刷新一次就没了。
onBeforeRouteLeave(async () => {
  if (saving.value || !dirty.value) {
    return true
  }
  try {
    await ElMessageBox.confirm('资料还没保存，离开后修改会丢失，确定离开？', '离开页面', {
      confirmButtonText: '离开',
      cancelButtonText: '继续编辑',
      type: 'warning',
    })
    return true
  } catch {
    return false
  }
})

onMounted(() => {
  void load()
})
</script>

<style scoped>
/* 与写文章页同样收窄到 820px：表单一行太长会让 label 和输入框相距过远，
   眼睛要在两列之间来回跳。 */
.settings {
  max-width: 820px;
  margin: 0 auto;
}

.settings__head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
  padding: 0 2px;
}

.settings__back {
  padding: 4px 0;
  border: none;
  background: none;
  color: var(--fs-text-3);
  font-size: 13px;
  cursor: pointer;
}

.settings__back:hover {
  color: var(--fs-text-1);
}

.settings__label {
  font-size: 13px;
  color: var(--fs-text-3);
}

.settings__loading {
  height: 220px;
}

.settings__card {
  padding: 8px 28px 20px;
}

/* 每个字段一行：左边固定宽的 label，右边自适应。
   固定 label 宽度是为了让四个输入框左边缘对齐，读起来像一张表。 */
.field {
  display: flex;
  gap: 16px;
  padding: 18px 0;
}

.field + .field {
  border-top: 1px solid var(--fs-line-soft);
}

.field__label {
  flex: none;
  width: 64px;
  padding-top: 8px;
  font-size: 14px;
  font-weight: 500;
  color: var(--fs-text-1);
}

.field__body {
  display: flex;
  flex: 1;
  gap: 16px;
  min-width: 0;
}

.field__control {
  flex: 1;
  min-width: 0;
}

.field__avatar {
  flex: none;
  background: var(--fs-primary-weak);
  color: var(--fs-primary);
  font-size: 24px;
}

.field__hint {
  margin: 8px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--fs-text-3);
}

/* 按钮独占一行，高度与右侧其它输入框的首行对齐。 */
.field__row {
  display: flex;
  gap: 8px;
}

/* 原生 file 控件完全藏起来，靠「上传图片」按钮触发它的 click。
   display:none 不影响 input.click()，现代浏览器都允许。 */
.field__file {
  display: none;
}

/* 压缩结果比普通提示更重要，用主色让它从灰字里跳出来。 */
.field__note {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--fs-primary);
}

/* 唯一需要提示「填错了」的地方，用点赞那支暖红。
   不做输入框标红：错误已经写在下面一行文字里，再描红边是重复。 */
.field__hint.is-error {
  color: var(--fs-like);
}

.field__readonly {
  display: inline-block;
  padding: 8px 0;
  font-size: 14px;
  color: var(--fs-text-2);
}

/* 与写文章页的底部条一致：一条浅线分隔，两端对齐。 */
.settings__foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-top: 8px;
  padding-top: 16px;
  border-top: 1px solid var(--fs-line-soft);
}

.settings__stat {
  font-size: 13px;
  color: var(--fs-text-3);
}

.settings__ops {
  display: flex;
  flex: none;
  gap: 8px;
}

/* 窄屏：label 换到输入框上方，一行只放一个字段。
   并排会把输入框压到 200px 出头，简介看不清。 */
@media (max-width: 720px) {
  .settings__card {
    padding: 4px 16px 16px;
  }

  .field {
    flex-direction: column;
    gap: 10px;
    padding: 14px 0;
  }

  .field__label {
    width: auto;
    padding-top: 0;
  }

  .field__body {
    gap: 12px;
  }

  .field__avatar {
    --el-avatar-size: 52px !important;
  }
}
</style>
