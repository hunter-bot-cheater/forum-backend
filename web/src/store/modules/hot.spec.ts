import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

// hot store 只做两件事：缓存榜单、决定什么时候重拉。
// 接口与 token 都换成可编程的假实现，用例只关心「发了几次请求、拿的是哪份数据」。
const topArticles = vi.fn()
const getToken = vi.fn()

vi.mock('@/api/hot', () => ({
  topArticles: (...args: unknown[]) => topArticles(...args),
}))

vi.mock('@/utils/auth', () => ({
  getToken: () => getToken(),
}))

// store 在 import 时才读 token，先 mock 完再动态引入，顺序不能反。
const { useHotStore } = await import('@/store/modules/hot')

// item 造一条榜单数据。liked 单独给默认值：多数用例不关心爱心。
function item(id: number, liked = false) {
  return {
    article_id: id,
    title: '文章 ' + String(id),
    like_count: id,
    view_count: id * 10,
    score: id * 13,
    liked,
  }
}

beforeEach(() => {
  setActivePinia(createPinia())
  topArticles.mockReset()
  getToken.mockReset()
  getToken.mockReturnValue('')
})

describe('hot store', () => {
  it('缓存够用时 ensure 不重复请求', async () => {
    topArticles.mockResolvedValue({ list: [item(1)] })
    const store = useHotStore()

    await store.ensure(10)
    await store.ensure(10)

    expect(topArticles).toHaveBeenCalledTimes(1)
  })

  it('条数不够时补拉，并且整体替换', async () => {
    topArticles
      .mockResolvedValueOnce({ list: [item(1)] })
      .mockResolvedValueOnce({ list: [item(1), item(2)] })
    const store = useHotStore()

    await store.ensure(5)
    await store.ensure(10)

    expect(topArticles).toHaveBeenCalledTimes(2)
    expect(store.list).toHaveLength(2)
  })

  it('登录后身份变了要重拉，否则爱心停在游客状态', async () => {
    // 只比条数的话，登录回到首页时缓存还够用，谁也不会去重新拉一次，
    // 已经赞过的文章会一直是灰的。
    topArticles.mockResolvedValue({ list: [item(1)] })
    const store = useHotStore()
    await store.ensure(10)

    getToken.mockReturnValue('token-after-login')
    await store.ensure(10)

    expect(topArticles).toHaveBeenCalledTimes(2)
  })

  it('登出后同样重拉，拿到的 liked 全部回到 false', async () => {
    getToken.mockReturnValue('token-before-logout')
    topArticles.mockResolvedValue({ list: [item(1, true)] })
    const store = useHotStore()
    await store.ensure(10)

    getToken.mockReturnValue('')
    topArticles.mockResolvedValue({ list: [item(1, false)] })
    await store.ensure(10)

    expect(topArticles).toHaveBeenCalledTimes(2)
    expect(store.list[0].liked).toBe(false)
  })

  it('请求失败时保留旧数据，不把榜单清空', async () => {
    topArticles.mockResolvedValueOnce({ list: [item(1)] })
    const store = useHotStore()
    await store.ensure(10)

    topArticles.mockRejectedValueOnce(new Error('network'))
    await store.load(20)

    // 拦截器已经弹过提示，这里再清空会让榜单变成一块空白。
    expect(store.list).toHaveLength(1)
    expect(store.loading).toBe(false)
  })

  it('invalidate 后下次 ensure 重拉，但旧数据先留着', async () => {
    // 在详情页点了赞，榜单里那一行的 liked 就过期了。
    // 只让缓存过期、不立刻重拉：用户可能根本不回榜单。
    topArticles.mockResolvedValue({ list: [item(1)] })
    const store = useHotStore()
    await store.ensure(10)

    store.invalidate()
    // 清 list 会让侧栏在返回列表页时闪一下空态，所以这里必须还留着旧数据。
    expect(store.list).toHaveLength(1)

    topArticles.mockResolvedValue({ list: [item(1, true)] })
    await store.ensure(10)

    expect(topArticles).toHaveBeenCalledTimes(2)
    expect(store.list[0].liked).toBe(true)
  })
})
