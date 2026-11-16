import { defineStore } from 'pinia'
import { ref } from 'vue'
import { topArticles, type HotArticle } from '@/api/hot'

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
  const loading = ref(false)

  // seq 丢弃迟到响应：右侧栏与热榜页可能同时触发请求，先发的后到会把新数据盖掉。
  let seq = 0

  // load 拉前 n 名，成功后整体替换。
  // 失败时保留旧数据：拦截器已经弹过提示，清空会让榜单变成空白。
  async function load(n = 10) {
    seq += 1
    const current = seq
    loading.value = true
    try {
      const data = await topArticles(n)
      if (current !== seq) {
        return
      }
      list.value = data.list
      cachedLimit.value = n
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
  // 所以页面每次进入都调一次 ensureRefresh，而不是只在 mounted 时拉一次。
  async function ensure(n = 10) {
    if (cachedLimit.value >= n) {
      return
    }
    await load(n)
  }

  function reset() {
    seq += 1
    list.value = []
    cachedLimit.value = 0
    loading.value = false
  }

  return { list, cachedLimit, loading, load, ensure, reset }
})
