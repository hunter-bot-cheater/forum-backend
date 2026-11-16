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
          <!-- 预览跟着输入实时变：贴完地址就能看到效果，
               不用先保存再回主页确认。 -->
          <el-avatar :size="64" :src="previewAvatar" class="field__avatar">{{ initial }}</el-avatar>
          <div class="field__control">
            <el-input
              v-model="avatarURL"
              placeholder="https://example.com/avatar.png"
              clearable
              :disabled="saving"
            />
            <p class="field__hint" :class="{ 'is-error': avatarError }">
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
            <span class="field__readonly">@{{ username }}</span>
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
          <el-button :disabled="saving" @click="cancel">取消</el-button>
          <el-button type="primary" :loading="saving" :disabled="!canSave" @click="save">
            保存
          </el-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { updateMe, type ProfilePatch, type UserProfile } from '@/api/user'
import { useUserStore } from '@/store/modules/user'
import { initials } from '@/utils/format'

// 编辑资料页：改昵称、头像地址、简介三样，用户名只读。
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

// previewAvatar 只在地址像 http(s) 链接时才交给 el-avatar，
// 其他情况（空串、刚敲了半截）返回 undefined 走首字兜底：
// 否则每输入一个字符都会请求一次无效图片，头像区不断闪成破图。
const previewAvatar = computed(() => {
  const v = avatarURL.value.trim()
  if (isHTTPURL(v)) {
    return v
  }
  return undefined
})

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

const nicknamePlaceholder = computed(() => '不填则显示 @' + username.value)

// 空地址是合法的（表示用首字兜底），非空但不是 http(s) 才算错。
const avatarInvalid = computed(() => {
  const v = avatarURL.value.trim()
  if (v === '') {
    return false
  }
  return !isHTTPURL(v)
})

const avatarError = computed(() => avatarInvalid.value)

const avatarHint = computed(() => {
  if (avatarInvalid.value) {
    return '要以 http:// 或 https:// 开头的图片地址'
  }
  return '填图片的 http/https 地址，留空则用名字首字。'
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

const canSave = computed(() => !saving.value && dirty.value && !avatarInvalid.value)

const statusText = computed(() => {
  if (saving.value) {
    return '正在保存…'
  }
  if (avatarInvalid.value) {
    return '头像地址不合法'
  }
  if (dirty.value) {
    return '有未保存的修改'
  }
  return '资料已是最新'
})

// isHTTPURL 是后端的简化版：只要求 scheme 与一个非空 host。
// 前端不做完整 URL 解析——真正的校验在 normalizeAvatarURL，
// 这里只拦「一眼就不对」的输入，避免用户点保存才被 400 打回来。
function isHTTPURL(raw: string) {
  return /^https?:\/\/[^\s/]+/i.test(raw)
}

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

// goBack 回自己的主页，而不是 router.back()：
// 直接打开这个地址时没有上一页，back 会退出站点。
function goBack() {
  if (user.userId > 0) {
    void router.push('/users/' + String(user.userId))
    return
  }
  void router.push('/articles')
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
   并排会把输入框压到 200px 出头，长地址和简介都看不清。 */
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
