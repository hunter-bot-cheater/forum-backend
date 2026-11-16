import { defineStore } from 'pinia'
import { ref } from 'vue'
import { listArticles, type ArticleListItem } from '@/api/article'
import { onLogout } from '@/utils/auth'

// 文章列表状态。
//
// 放在 store 而不是页面本地 ref：右侧栏、列表页、以后的通知跳转都可能要读同一份数据，
// 页面本地 ref 在切走再切回来时会被销毁，回来就得重新拉一次。
// 分页位置也一并放这里，后面做「从详情返回保持原页码」不用再改结构。
export const useArticleStore = defineStore('article', () => {
  const list = ref<ArticleListItem[]>([])
  const total = ref(0)
  const page = ref(1)
  // 一页十篇：按当前卡片高度估算大约一屏半，
  // 再多会让首屏加载变慢，再少又显得列表很空。
  const pageSize = ref(10)
  const loading = ref(false)
  // loaded 与 loading 分开：前者表示「至少成功拿到过一次数据」。
  // 首屏要据此决定是显示骨架屏还是空态——只有 loading 的话，
  // 第一次请求还没回来时列表是空的，会先闪一下「还没有文章」。
  const loaded = ref(false)
  // keyword 是当前列表的搜索词，空串表示没在搜索。
  // 放在 store 而不是页面里：从详情页返回列表时页面组件会重新挂载，
  // 页面本地 ref 一销毁搜索态就没了，用户回来看到的是全站列表。
  const keyword = ref('')
  // tag 是当前列表的标签筛选，空串表示没筛。
  // 与 keyword 并存：后端两个条件是 AND，前端也允许同时留着（搜「GORM」且只看「后端」）。
  // 和 keyword 一样放在 store：从详情返回时筛选态不能丢。
  const tag = ref('')

  // seq 用来丢弃迟到的响应。
  // 用户快速连点页码时会发出多个请求，网络抖动下它们可能乱序回来：
  // 第 2 页的响应后到，就会把已经显示的第 3 页覆盖回去。
  let seq = 0

  // load 拉第 p 页，成功后整体替换列表。
  // 失败时保留旧数据：请求拦截器已经弹过提示，这里清空会让人以为文章被删了。
  // 搜索词从 store 读，调用方不用每次显式传：翻页时漏传关键词
  // 会让第 2 页悄悄变回全站列表，这种错很难从界面上看出来。
  async function load(p = 1) {
    seq += 1
    const current = seq
    loading.value = true
    try {
      const data = await listArticles(p, pageSize.value, 0, keyword.value, tag.value)
      if (current !== seq) {
        return
      }
      list.value = data.list
      total.value = data.total
      // 用服务端回显的页码，而不是入参 p：服务端会把越界页码收敛成有效值，
      // 前端跟着它走，分页器高亮的位置才能真正对应这一页内容。
      page.value = data.page
      loaded.value = true
    } catch {
      // 见上：保留旧列表。
    } finally {
      // loading 也只在最新那次请求回来时关掉，
      // 否则先到的旧请求会把还在加载中的界面提前解除遮罩。
      if (current === seq) {
        loading.value = false
      }
    }
  }

  // applyFilter 一次性设置关键词与标签筛选，并回到第一页。
  //
  // 两个条件在后端是 AND（搜「GORM」且只看「后端」），前端也允许同时留着。
  // 之所以合成一个方法而不是 applyKeyword + applyTag：带 ?q=…&tag=… 的链接进来时，
  // 分开调会各自触发一次 load，同一份列表拉两遍。
  //
  // 换条件后页码必须归 1：第 3 页在新结果集里可能根本不存在，
  // 后端会把越界页码收敛成最后一页，用户看到的是「筛出来的第二屏」。
  // 与现值完全相同就不重拉：地址栏的 watch 与页面上的操作都可能触发这里，
  // 重复请求会让列表白闪一次 loading。
  async function applyFilter(next: { keyword: string; tag: string }) {
    const kw = next.keyword.trim()
    const tg = next.tag.trim()
    if (kw === keyword.value && tg === tag.value) {
      return
    }
    keyword.value = kw
    tag.value = tg
    page.value = 1
    await load(1)
  }

  // invalidate 表示「服务端数据已经变了，下次进列表要重拉」。
  //
  // 发布文章后调用：新文章排在第一页，不重拉的话用户回到列表还是旧数据，
  // 会以为自己发失败了。只把 loaded 打回 false、不动 list，
  // 这样回到列表时先看到旧内容再被新数据替换，不会先塌成骨架屏再长回来。
  // 搜索词不动：发布后回到搜索态是对的，用户刚才在找的就是这类文章。
  function invalidate() {
    loaded.value = false
    page.value = 1
  }

  // reset 在登出时清空。文章是公开数据，不清也不会串号，
  // 但下一个人登录后看到的还是上一个人停留的页码，容易以为列表坏了。
  // 搜索词一起清：上一个人搜的词留在搜索框里，下一个人会莫名其妙。
  function reset() {
    seq += 1
    list.value = []
    total.value = 0
    page.value = 1
    keyword.value = ''
    tag.value = ''
    loading.value = false
    loaded.value = false
  }

  // 登出时清空。注册到 auth 的回调表而不是在顶栏里手动调用：
  // 登录过期（401）也走同一条路径，不会出现「手动退出清了、过期没清」。
  onLogout(reset)

  return { list, total, page, pageSize, loading, loaded, keyword, tag, load, applyFilter, invalidate, reset }
})
