<template>
  <div class="fs-container adm">
    <div class="adm__head">
      <h1 class="adm__title">管理后台</h1>
      <span class="adm__hint">举报处置与标签整理，只有配置在白名单里的账号能操作</span>
    </div>

    <!-- 资料还没拉回来时先骨架屏：profile 为 null 的那一瞬间 isAdmin 恒为 false，
         直接判断会闪一下「没有权限」，看起来像登录态出了问题。 -->
    <el-skeleton v-if="!user.profileLoaded" class="adm__skeleton" :rows="6" animated />

    <!-- 拉过一次但还是没有资料（网络断了、后端 500）：给一个重试入口。
         这里不能落到下面的「不是管理员」分支，那会让人以为是权限问题。 -->
    <div v-else-if="user.profile === null" class="fs-card adm__forbidden">
      <el-empty description="没能加载你的账号资料，请重试" :image-size="88">
        <el-button type="primary" @click="retryProfile">重新加载</el-button>
      </el-empty>
    </div>

    <div v-else-if="!user.isAdmin" class="fs-card adm__forbidden">
      <el-empty description="这个页面只有管理员能打开" :image-size="88">
        <el-button type="primary" @click="router.replace('/articles')">回到文章列表</el-button>
      </el-empty>
    </div>

    <template v-else>
      <!-- 两个面板用胶囊按钮切换，与通知页同一套写法：
           el-tabs 自带下划线和大内边距，放在这里视觉权重会盖过页面标题。 -->
      <div class="adm__panels">
        <button
          v-for="panel in PANELS"
          :key="panel.value"
          class="adm__panel-btn"
          :class="{ 'is-active': panel.value === activePanel }"
          @click="activePanel = panel.value"
        >
          {{ panel.label }}
        </button>
      </div>

      <!-- ================= 举报处理 ================= -->
      <div v-if="activePanel === 'reports'">
        <div class="adm__bar">
          <div class="adm__seg">
            <button
              v-for="item in REPORT_STATUS_TABS"
              :key="item.value"
              class="adm__seg-btn"
              :class="{ 'is-active': item.value === reportStatus }"
              @click="switchReportStatus(item.value)"
            >
              {{ item.label }}
            </button>
          </div>
          <span class="adm__count">共 {{ reports.total }} 条</span>
        </div>

        <div v-loading="reports.loading" class="fs-card adm__list">
          <el-empty
            v-if="!reports.loading && reports.list.length === 0"
            :description="reportEmptyText"
            :image-size="88"
          />

          <div v-for="item in reports.list" :key="item.id" class="rp">
            <div class="rp__top">
              <span class="rp__kind" :class="'is-' + item.target_type">
                {{ item.target_type === 'article' ? '文章' : '评论' }}
              </span>
              <span class="rp__reason">{{ reasonLabel(item.reason) }}</span>
              <span class="rp__time">{{ formatRelativeTime(item.created_at) }}</span>
              <!-- 终态只读不操作：一条举报处置完不能改判，要改就重新举报，
                   所以这里只标状态，下面不出现按钮。 -->
              <span v-if="item.status !== 'pending'" class="rp__status">
                {{ statusLabel(item.status) }}
              </span>
            </div>

            <router-link class="rp__title" :to="targetLink(item)">
              {{ titleOf(item) }}
            </router-link>

            <p v-if="item.excerpt" class="rp__excerpt">{{ item.excerpt }}</p>
            <p v-if="item.detail" class="rp__detail">补充说明：{{ item.detail }}</p>

            <div class="rp__meta">
              <span>举报人：{{ nameOf(item.reporter) }}</span>
              <span>作者：{{ nameOf(item.target_author) }}</span>
              <!-- 内容已被作者自删时，「下架」没有意义，按钮会置灰；
                   这里明写一句，省得管理员点一下才知道。 -->
              <span v-if="!item.target_exists" class="rp__gone">内容已被删除</span>
            </div>

            <div v-if="item.status === 'pending'" class="rp__ops">
              <el-button size="small" :disabled="busyId === item.id" @click="dismissReport(item)">
                驳回
              </el-button>
              <el-button
                size="small"
                type="warning"
                plain
                :disabled="busyId === item.id || !item.target_exists"
                @click="removeReport(item)"
              >
                下架内容
              </el-button>
              <el-button
                size="small"
                type="danger"
                plain
                :disabled="busyId === item.id"
                @click="banAuthor(item)"
              >
                下架并封禁
              </el-button>
            </div>
          </div>

          <div v-if="reports.total > PAGE_SIZE" class="adm__pager">
            <el-pagination
              background
              layout="prev, pager, next"
              :current-page="reports.page"
              :page-size="PAGE_SIZE"
              :total="reports.total"
              @current-change="onReportPage"
            />
          </div>
        </div>
      </div>

      <!-- ================= 标签管理 ================= -->
      <div v-else>
        <div class="adm__bar">
          <el-input
            v-model="tags.keyword"
            class="adm__search"
            placeholder="按标签名搜"
            clearable
            @keyup.enter="searchTags"
            @clear="searchTags"
          />
          <el-button :loading="tags.loading" @click="searchTags">搜索</el-button>
          <span class="adm__count">共 {{ tags.total }} 个标签</span>
        </div>

        <div v-loading="tags.loading" class="fs-card adm__list">
          <el-empty
            v-if="!tags.loading && tags.list.length === 0"
            :description="tags.keyword.trim() === '' ? '还没有任何标签' : '没有匹配的标签'"
            :image-size="88"
          />

          <table v-if="tags.list.length > 0" class="tg">
            <thead>
              <tr>
                <th>标签</th>
                <th class="tg__count">文章数</th>
                <th class="tg__ops">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in tags.list" :key="item.id">
                <td>
                  <el-input
                    v-if="editingId === item.id"
                    v-model="editingName"
                    size="small"
                    maxlength="32"
                    show-word-limit
                    class="tg__input"
                    @keyup.enter="saveTag(item)"
                  />
                  <span v-else class="tg__name">{{ item.name }}</span>
                </td>
                <td class="tg__count">{{ item.count }}</td>
                <td class="tg__ops">
                  <template v-if="editingId === item.id">
                    <el-button
                      size="small"
                      type="primary"
                      :loading="savingTagId === item.id"
                      @click="saveTag(item)"
                    >
                      保存
                    </el-button>
                    <el-button size="small" @click="cancelEdit">取消</el-button>
                  </template>
                  <el-button v-else size="small" @click="startEdit(item)">改名</el-button>
                </td>
              </tr>
            </tbody>
          </table>

          <div v-if="tags.total > PAGE_SIZE" class="adm__pager">
            <el-pagination
              background
              layout="prev, pager, next"
              :current-page="tags.page"
              :page-size="PAGE_SIZE"
              :total="tags.total"
              @current-change="onTagPage"
            />
          </div>
        </div>

        <!-- 合并规则写在页面里而不是弹窗里：管理员第一次改名前就会看到，
             不至于改完才发现「文章被带走了」。 -->
        <p class="adm__note">
          改成一个已经存在的名字会自动合并：两个标签下的文章并到一起，被并入的那个标签消失。
          一次改名会清掉受影响文章的详情缓存，页面可能要等一两秒。
        </p>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useUserStore } from '@/store/modules/user'
import { formatRelativeTime } from '@/utils/format'
import {
  handleReport,
  listReports,
  reasonLabel,
  type HandleAction,
  type ReportItem,
  type ReportStatus,
} from '@/api/report'
import { adminListTags, renameTag, type AdminTagItem } from '@/api/article'

// 管理后台：举报处置 + 标签整理。
// 权限只有一层——后端 /api/admin 那一组挂了 RequireAdmin，
// 这里的 isAdmin 判断只决定「显不显示页面」，不是安全边界：
// 就算有人把本地状态改成 true，所有请求照样会被 403 挡住。

const router = useRouter()
const user = useUserStore()

// 一页十条：举报要逐条读内容判断，条数再多就会翻到懒得看。
// 标签也共用这个值——管理界面里两处每页条数不一致，翻页手感会对不上。
const PAGE_SIZE = 10

// 面板的 value 显式写成联合类型：让 v-model 那边的 ref<'reports' | 'tags'>
// 能直接接住，不然数组字面量会把 value 推成 string，赋值时报类型不匹配。
const PANELS: { value: 'reports' | 'tags'; label: string }[] = [
  { value: 'reports', label: '举报处理' },
  { value: 'tags', label: '标签管理' },
]
const activePanel = ref<'reports' | 'tags'>('reports')

const REPORT_STATUS_TABS: { value: ReportStatus; label: string }[] = [
  { value: 'pending', label: '待处理' },
  { value: 'dismissed', label: '已驳回' },
  { value: 'removed', label: '已下架' },
  { value: 'banned', label: '已封禁' },
]

// ready 表示「资料已经拉过一次且拿到了」。
// 它为 false 时不发任何管理请求：那时 isAdmin 还是默认的 false，
// 提前拉列表只会换来一次 403 和一条多余的错误提示。
const ready = computed(() => user.profileLoaded && user.profile !== null)

const reports = reactive({
  list: [] as ReportItem[],
  total: 0,
  page: 1,
  loading: false,
})
const reportStatus = ref<ReportStatus>('pending')
// busyId 记正在处置的那条：处置是写操作且不可撤销，
// 连点两下会拿到 409（已被处理过），不如点下去就把这排按钮锁住。
const busyId = ref(0)

const tags = reactive({
  list: [] as AdminTagItem[],
  total: 0,
  page: 1,
  keyword: '',
  loading: false,
})
// editingId 非 0 时，那一行切换成输入框；同一时刻只允许一行处于编辑态，
// 两行同时编辑容易把「保存」点成另一行的。
const editingId = ref(0)
const editingName = ref('')
const savingTagId = ref(0)

// 两个面板第一次真正可见时才拉数据。
// 用 flag 而不是在 onMounted 里拉：挂载的那一刻 profile 还没回来，
// 那时 isAdmin 为 false，请求会被后端拒绝。
let loaded = false

watch(
  () => [ready.value, user.isAdmin] as const,
  (state) => {
    const [isReady, isAdmin] = state
    if (!isReady || !isAdmin || loaded) {
      return
    }
    loaded = true
    void loadReports()
    void loadTags()
  },
  { immediate: true },
)

// reportEmptyText 把四种状态的空态分开写。
// 统一写「没有数据」的话，管理员分不清是「都处理完了」还是「筛错了状态」。
// 用 computed 而不是函数：模板里直接绑函数会渲染成函数体字符串。
const reportEmptyText = computed(() => {
  if (reportStatus.value === 'pending') {
    return '没有待处理的举报，暂时不用管'
  }
  return '这个状态下没有举报记录'
})

function statusLabel(status: ReportStatus): string {
  for (const item of REPORT_STATUS_TABS) {
    if (item.value === status) {
      return item.label
    }
  }
  return status
}

// nameOf 取一个展示名，缺失时写「已注销」。
// 触发者注销后后端会把整个 reporter 字段省掉，直接读 username 会抛错。
function nameOf(brief?: { nickname: string; username: string }): string {
  if (!brief) {
    return '已注销'
  }
  if (brief.nickname) {
    return brief.nickname
  }
  return brief.username
}

// titleOf 兜底标题。文章被删后 target_title 是空的，
// 空链接点进去只有一个 404，不如明说内容没了。
function titleOf(item: ReportItem): string {
  if (item.target_title) {
    return item.target_title
  }
  return '（内容已删除）'
}

// targetLink 给出「去看看被举报的东西」的地址。
// 评论类举报带 ?comment=<id>，详情页会翻页并高亮到那一条；
// 只给文章地址的话，管理员得自己在几百条评论里找。
function targetLink(item: ReportItem): string {
  if (item.target_type === 'comment' && item.article_id > 0) {
    return '/articles/' + String(item.article_id) + '?comment=' + String(item.target_id)
  }
  return '/articles/' + String(item.target_id)
}

// retryProfile 只重新拉一次资料，不动页面上的其它状态。
// 失败本身就是「重新加载」，不需要再套一层 loading。
function retryProfile() {
  void user.fetchMe()
}

// ---------------- 举报 ----------------

async function loadReports() {
  reports.loading = true
  try {
    const data = await listReports(reportStatus.value, reports.page, PAGE_SIZE)
    reports.list = data.list
    reports.total = data.total
  } finally {
    // 失败时也要把 loading 关掉：拦截器已经弹过提示，
    // 留着转圈会让人以为还在等，反复点分页。
    reports.loading = false
  }
}

function switchReportStatus(status: ReportStatus) {
  if (status === reportStatus.value) {
    return
  }
  reportStatus.value = status
  // 换筛选必须回第一页：原来在第 3 页、新状态只有 5 条，
  // 不回退会直接看到一个空列表。
  reports.page = 1
  void loadReports()
}

function onReportPage(page: number) {
  reports.page = page
  void loadReports()
}

// act 是三种处置动作的共同出口：调接口 → 提示 → 重拉当前页。
async function act(item: ReportItem, action: HandleAction, banDays: number, tip: string) {
  busyId.value = item.id
  try {
    await handleReport(item.id, action, banDays)
    ElMessage.success(tip)
    // 当前页只有这一条时，处理后它就从 pending 列表里消失了。
    // 不回退一页的话，管理员会停在一个空页上，以为后面没有了。
    if (reports.list.length === 1 && reports.page > 1) {
      reports.page -= 1
    }
    await loadReports()
  } finally {
    busyId.value = 0
  }
}

// 驳回不需要二次确认：它不处罚任何人，误点也只是这条举报回到待处理。
function dismissReport(item: ReportItem) {
  void act(item, 'dismiss', 0, '已驳回这条举报')
}

// 下架会直接改内容状态，作者那边看不到原文了，属于不可逆操作，先确认一次。
async function removeReport(item: ReportItem) {
  try {
    await ElMessageBox.confirm(
      '下架后文章/评论会从站内消失，作者无法自行恢复。确认下架？',
      '下架内容',
      { confirmButtonText: '下架', cancelButtonText: '取消', type: 'warning' },
    )
  } catch {
    return
  }
  void act(item, 'remove', 0, '已下架内容')
}

// 封禁天数用 prompt 而不是下拉框：下拉框里放几个固定档位（1/3/7/30），
// 遇到需要封 90 天的场景就得改前端。这里直接填数字，1~3650 与后端一致。
async function banAuthor(item: ReportItem) {
  let days = 7
  try {
    const res = await ElMessageBox.prompt(
      '封禁天数 1~3650。填 3650 约等于十年，后端不提供「永久」，因为一次误封没法靠回滚补救。',
      '下架内容并封禁作者',
      {
        confirmButtonText: '封禁',
        cancelButtonText: '取消',
        inputValue: '7',
        inputValidator: (value: string) => {
          const n = Number(value)
          if (!Number.isInteger(n) || n < 1 || n > 3650) {
            return '请填 1 到 3650 之间的整数'
          }
          return true
        },
      },
    )
    days = Number(res.value)
  } catch {
    return
  }
  void act(item, 'ban', days, '已下架内容并封禁作者 ' + String(days) + ' 天')
}

// ---------------- 标签 ----------------

async function loadTags() {
  tags.loading = true
  try {
    const data = await adminListTags(tags.keyword, tags.page, PAGE_SIZE)
    tags.list = data.list
    tags.total = data.total
  } finally {
    tags.loading = false
  }
}

function searchTags() {
  // 搜索词变了必须回第一页，理由同举报的状态筛选。
  tags.page = 1
  void loadTags()
}

function onTagPage(page: number) {
  tags.page = page
  void loadTags()
}

function startEdit(item: AdminTagItem) {
  editingId.value = item.id
  editingName.value = item.name
}

function cancelEdit() {
  editingId.value = 0
  editingName.value = ''
}

// saveTag 提交改名。新名字已存在时后端自动合并，前端不需要多问一句
// 「要不要合并」：管理员的意图就是「把这些文章改叫那个名字」。
async function saveTag(item: AdminTagItem) {
  const name = editingName.value.trim()
  if (name === '') {
    ElMessage.warning('标签名不能为空')
    return
  }
  // 没改就什么都不发：后端同名分支虽然是幂等的，但白跑一次请求毫无意义。
  if (name === item.name) {
    cancelEdit()
    return
  }

  savingTagId.value = item.id
  try {
    const res = await renameTag(item.id, name)
    if (res.merged) {
      ElMessage.success('已合并到「' + res.name + '」，' + String(res.moved_articles) + ' 篇文章改挂')
    } else {
      ElMessage.success('已改名为「' + res.name + '」，影响 ' + String(res.moved_articles) + ' 篇文章')
    }
    cancelEdit()
    await loadTags()
  } finally {
    savingTagId.value = 0
  }
}
</script>

<style scoped>
.adm {
  max-width: 860px;
}

.adm__head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 14px;
  padding: 0 2px;
}

.adm__title {
  font-size: 22px;
  font-weight: 700;
  color: var(--fs-text-1);
}

.adm__hint {
  font-size: 13px;
  color: var(--fs-text-3);
}

.adm__skeleton {
  padding: 8px 4px;
}

.adm__forbidden {
  padding: 24px 0;
}

/* 面板切换用胶囊：与通知页的分类 tab 同一套写法，
   全站只有一种「分段切换」的样子。 */
.adm__panels {
  display: flex;
  gap: 8px;
  margin-bottom: 14px;
}

.adm__panel-btn {
  padding: 8px 18px;
  border: none;
  border-radius: 16px;
  background: var(--fs-surface);
  color: var(--fs-text-2);
  font-size: 14px;
  cursor: pointer;
  transition: background-color 0.15s, color 0.15s;
  box-shadow: var(--fs-shadow-sm);
}

.adm__panel-btn:hover {
  background: var(--fs-line-soft);
}

.adm__panel-btn.is-active {
  background: var(--fs-primary);
  color: #fff;
  font-weight: 600;
}

.adm__bar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}

/* 状态筛选比面板切换轻一档：字号小、底色浅，
   不能让两层胶囊看起来像同一级导航。 */
.adm__seg {
  display: flex;
  gap: 6px;
}

.adm__seg-btn {
  padding: 5px 12px;
  border: none;
  border-radius: 12px;
  background: var(--fs-bg);
  color: var(--fs-text-2);
  font-size: 13px;
  cursor: pointer;
  transition: background-color 0.15s, color 0.15s;
}

.adm__seg-btn:hover {
  background: var(--fs-line);
}

.adm__seg-btn.is-active {
  background: var(--fs-primary-weak);
  color: var(--fs-primary);
  font-weight: 600;
}

.adm__search {
  width: 240px;
}

.adm__count {
  margin-left: auto;
  font-size: 13px;
  color: var(--fs-text-3);
}

.adm__list {
  overflow: hidden;
  padding: 4px 0;
}

.adm__pager {
  display: flex;
  justify-content: center;
  padding: 14px 0;
  border-top: 1px solid var(--fs-line-soft);
}

.adm__note {
  margin-top: 12px;
  padding: 0 2px;
  font-size: 12px;
  line-height: 1.7;
  color: var(--fs-text-3);
}

/* ---------------- 举报行 ---------------- */

.rp {
  padding: 16px 20px;
}

/* 用相邻兄弟而不是 :not(:last-of-type)：卡片里还有分页块（同样是 div），
   最后一条举报并不一定是最后一个 div，写反了会多出一道横线压在分页上方。
   第一行本身不需要上边线，`.rp + .rp` 刚好只给中间的分隔。 */
.rp + .rp {
  border-top: 1px solid var(--fs-line-soft);
}

.rp__top {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
  font-size: 12px;
  color: var(--fs-text-3);
}

/* 类型标记用两档中性色：这里只是分类，不是「好/坏」，
   用红蓝会跟页面上的危险操作抢注意力。 */
.rp__kind {
  padding: 1px 7px;
  border-radius: 4px;
  background: var(--fs-bg);
  color: var(--fs-text-2);
  font-size: 12px;
}

.rp__kind.is-article {
  background: var(--fs-primary-weak);
  color: var(--fs-primary);
}

.rp__reason {
  font-weight: 600;
  color: var(--fs-text-2);
}

.rp__time {
  margin-left: auto;
}

.rp__status {
  padding: 1px 7px;
  border-radius: 4px;
  background: var(--fs-line-soft);
  color: var(--fs-text-3);
}

.rp__title {
  display: block;
  margin-bottom: 6px;
  font-size: 15px;
  font-weight: 600;
  color: var(--fs-text-1);
}

.rp__title:hover {
  color: var(--fs-primary);
}

/* 摘要最多两行：一屏十条举报，每条都摊开全文就翻不动了。
   要看完整内容点标题进详情。 */
.rp__excerpt {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
  font-size: 13px;
  line-height: 1.7;
  color: var(--fs-text-2);
  white-space: pre-wrap;
  word-break: break-word;
}

.rp__detail {
  margin-top: 6px;
  padding: 6px 10px;
  border-left: 2px solid var(--fs-line);
  font-size: 13px;
  line-height: 1.7;
  color: var(--fs-text-2);
  word-break: break-word;
}

.rp__meta {
  display: flex;
  flex-wrap: wrap;
  gap: 14px;
  margin-top: 8px;
  font-size: 12px;
  color: var(--fs-text-3);
}

.rp__gone {
  color: var(--fs-like);
}

.rp__ops {
  display: flex;
  gap: 8px;
  margin-top: 12px;
}

.rp__ops .el-button {
  margin-left: 0;
}

/* ---------------- 标签表 ---------------- */

.tg {
  width: 100%;
  border-collapse: collapse;
  font-size: 14px;
}

.tg th {
  padding: 10px 20px;
  text-align: left;
  font-size: 12px;
  font-weight: 500;
  color: var(--fs-text-3);
  border-bottom: 1px solid var(--fs-line-soft);
}

.tg td {
  padding: 10px 20px;
  border-bottom: 1px solid var(--fs-line-soft);
  color: var(--fs-text-1);
}

/* 最后一行不画线：否则会和卡片底边叠成一条明显的深色线。 */
.tg tbody tr:last-child td {
  border-bottom: none;
}

/* 右对齐要写成 th.tg__count 这种两级选择器：`.tg th` 的特异性比单个类高，
   只写 .tg__count 的话表头会被强制左对齐，而数字是右对齐，
   一列里表头贴左、数字贴右，看起来像错位。 */
.tg td.tg__count,
.tg th.tg__count {
  width: 100px;
  text-align: right;
}

.tg td.tg__count {
  color: var(--fs-text-2);
}

.tg th.tg__count {
  color: var(--fs-text-3);
}

.tg td.tg__ops,
.tg th.tg__ops {
  width: 160px;
  text-align: right;
}

.tg__ops .el-button {
  margin-left: 8px;
}

.tg__ops .el-button:first-child {
  margin-left: 0;
}

.tg__name {
  font-weight: 500;
}

.tg__input {
  max-width: 320px;
}

@media (max-width: 720px) {
  .adm__bar {
    flex-wrap: wrap;
  }

  .adm__search {
    width: 100%;
  }

  .adm__count {
    margin-left: 0;
  }

  .tg th,
  .tg td {
    padding: 10px 12px;
  }
}
</style>
