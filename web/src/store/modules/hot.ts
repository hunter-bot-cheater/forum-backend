import { defineStore } from 'pinia'
import { ref } from 'vue'
import { topArticles, type HotArticle } from '@/api/hot'
import { getToken } from '@/utils/auth'

// 当前访问者的身份指纹。
//
// liked 的主语是「我」，同一条榜单登录前后返回的内容不同，
// 所以缓存必须按身份分。用 access token 当指纹，是因为它随登录、登出、
// 换号自动变化，不需要每个页面记得在登录后主动清缓存；
// 而它在刷新页面时本来就在 localStorage 里，首屏那次请求不会是「先按游客拿一遍」。
//
// 同一用户 token 轮换后会多拉一次榜，代价可以接受：
// 换来的是绝不会拿别人的 liked 去渲染红心。
function viewerKey(): string {
  return getToken()
}

// 热榜状态。
//
// 右侧栏要 5 条、热榜页要 10 条，两处读同一份缓存：
// 先进列表页（也就是先进列表页的右侧栏）拿到 5 条，再点进热榜页时
// ensure(10) 发现缓存不够会重新拉一次，不用两个地方各存一份。
export const useHotStore = defineStore('hot', () => {
  const list = ref<HotArticle[]>([])
  // cachedLimit 记的是「当前这份 list 是按前多少名拉的」。
  // 不能只看 list.length：榜上只有 3 篇时 length 恒为 3，
  // 之后又有人点赞变成 10 篇，只比 length 会一直认为缓存够用，榜单永远停在 3 条。
  const cachedLimit = ref(0)
  // cachedViewer 记的是「这份 list 是按谁的眼睛拉的」。空串代表从没拉过，
  // 与游客（无 token）的值一致，所以在取到第一份数据之前它不会误判成命中。
  const cachedViewer = ref('')
  const loading = ref(false)

  // seq 丢弃迟到响应：右侧栏与热榜页可能同时触发请求，先发的后到会把新数据盖掉。
  let seq = 0

  // load 拉前 n 名，成功后整体替换。
  // 失败时保留旧数据：拦截器已经弹过提示，清空会让榜单变成空白。
  async function load(n = 10) {
    seq += 1
    const current = seq
    // 先记下身份再发请求。请求在途时用户登出的话，回来的仍是带 token 请求的结果，
    // 但缓存的指纹记的是「已登录」，下一次 ensure 会立刻发现不一致并重拉。
    const viewer = viewerKey()
    loading.value = true
    try {
      const data = await topArticles(n)
      if (current !== seq) {
        return
      }
      list.value = data.list
      cachedLimit.value = n
      cachedViewer.value = viewer
    } catch {
      // 见上：保留旧数据。
    } finally {
      if (current === seq) {
        loading.value = false
      }
    }
  }

  // ensure 在缓存不够新时补拉一次。
  // 榜单是按小时衰减重算的，同一份缓存坐着不动会越来越旧，
  // 所以页面每次进入都调一次 ensure，而不是只在 mounted 时拉一次。
  //
  // 除了条数，还要比身份：登录、登出、换号之后同一条榜单的 liked 完全不同，
  // 只比条数会让刚登录的用户仍然看到一片灰爱心。
  async function ensure(n = 10) {
    if (cachedLimit.value >= n && cachedViewer.value === viewerKey()) {
      return
    }
    await load(n)
  }

  // invalidate 让下一次 ensure 重拉，但不动当前数据。
  //
  // 用户在某篇文章点了赞，榜单里那一行的 liked 就过期了。
  // 不清 list 是有意的：清空会让侧栏在返回列表页时闪一下空态，
  // 而 ensure 看到条数不够会重拉，重拉回来之前页面仍然有内容可看。
  function invalidate() {
    cachedLimit.value = 0
  }

  function reset() {
    seq += 1
    list.value = []
    cachedLimit.value = 0
    cachedViewer.value = ''
    loading.value = false
  }

  return { list, cachedLimit, loading, load, ensure, invalidate, reset }
})
