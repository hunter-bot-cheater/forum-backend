import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

// 热榜页只负责把 store 里的数据按行摆出来，所以这里测的是「数据到界面」这一层：
// 爱心的红/灰取决于 item.liked，数字用 like_count 而不是 score。
const topArticles = vi.fn()

vi.mock('@/api/hot', () => ({
  topArticles: (...args: unknown[]) => topArticles(...args),
}))

vi.mock('@/utils/auth', () => ({
  getToken: () => '',
}))

const HotPage = (await import('@/views/hot/index.vue')).default

// 页面用到的 Element Plus 组件都只承担容器作用，用最小替身即可，
// 不装整个 UI 库（这里一条组件的行为都不依赖）。
const stubs = {
  'el-card': { template: '<div class="el-card"><slot /></div>' },
  'el-empty': { template: '<div class="el-empty" />' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  RouterLink: RouterLinkStub,
}

function item(id: number, liked: boolean) {
  return {
    article_id: id,
    title: '第 ' + String(id) + ' 篇',
    like_count: id * 10,
    view_count: id * 100,
    score: id * 130,
    liked,
  }
}

async function mountPage(items: ReturnType<typeof item>[]) {
  topArticles.mockResolvedValue({ list: items })
  const pinia = createPinia()
  setActivePinia(pinia)

  const wrapper = mount(HotPage, { global: { plugins: [pinia], stubs } })
  // onMounted 里的 ensure 是异步的，等它落地再断言，否则第一帧还是骨架屏。
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  topArticles.mockReset()
})

describe('热榜页', () => {
  it('只有我赞过的那一行点亮爱心', async () => {
    const wrapper = await mountPage([item(1, true), item(2, false)])

    const hearts = wrapper.findAll('.hot-row__heart')
    expect(hearts).toHaveLength(2)
    expect(hearts[0].classes()).toContain('is-liked')
    expect(hearts[1].classes()).not.toContain('is-liked')
  })

  it('游客（都没有点赞）时一个爱心也不点亮', async () => {
    const wrapper = await mountPage([item(1, false), item(2, false)])

    expect(wrapper.findAll('.hot-row__heart.is-liked')).toHaveLength(0)
  })

  it('每行显示点赞数与浏览数，不显示合成分数', async () => {
    const wrapper = await mountPage([item(2, false)])

    const row = wrapper.find('.hot-row')
    // 数字取 like_count / view_count：score 是排序用的合成值，
    // 摆出来会被读成「这么多人赞过」。
    expect(row.find('.hot-row__like').text()).toContain('20')
    expect(row.find('.hot-row__view').text()).toContain('200')
    expect(row.text()).not.toContain('260')
  })

  it('名次按数组顺序，前三名单独标记', async () => {
    const wrapper = await mountPage([item(1, false), item(2, false), item(3, false), item(4, false)])

    const ranks = wrapper.findAll('.hot-row__rank')
    expect(ranks[0].text()).toBe('1')
    expect(ranks[0].classes()).toContain('is-top')
    expect(ranks[2].classes()).toContain('is-top')
    expect(ranks[3].classes()).not.toContain('is-top')
  })
})
