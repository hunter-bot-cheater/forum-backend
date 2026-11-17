import { describe, expect, it } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import HotRankCard from '@/components/HotRankCard.vue'
import type { HotArticle } from '@/api/hot'

// 热榜卡片是哑组件：数据全从 props 进来，组件只决定「显示几条、怎么显示」。
// 这些用例锁的是几个容易改坏、改坏了又不报错的判定：
// 条数截断、more 的触发条件、前三名的高亮、空态与加载态的优先级。

function makeItems(count: number): HotArticle[] {
  const items: HotArticle[] = []
  for (let i = 1; i <= count; i++) {
    items.push({
      article_id: i,
      title: '第 ' + String(i) + ' 篇文章',
      like_count: i * 10,
      view_count: i * 100,
      score: i * 30 + i * 100,
    })
  }
  return items
}

// el-card / el-skeleton 是 Element Plus 的全局组件，
// 单测里不装整个 UI 库（一个卡片用不上它的任何行为），用最小替身即可。
const stubs = {
  'el-card': { template: '<div class="el-card"><slot /></div>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  RouterLink: RouterLinkStub,
}

// 组件 props 显式写一份类型：mountCard 的入参若用 Record<string, unknown>，
// Vue 的 props 校验在类型层面会拒绝（items 是必填）。
type CardProps = {
  items: HotArticle[]
  loading?: boolean
  limit?: number
  title?: string
}

function mountCard(props: CardProps) {
  return mount(HotRankCard, { props, global: { stubs } })
}

describe('HotRankCard', () => {
  it('默认只显示前 5 条', () => {
    const wrapper = mountCard({ items: makeItems(8) })

    const rows = wrapper.findAll('.hr__item')
    expect(rows).toHaveLength(5)
    // 显示的是前 5 条而不是后 5 条：榜单的顺序本身就是信息。
    expect(rows[0].text()).toContain('第 1 篇文章')
    expect(rows[4].text()).toContain('第 5 篇文章')
  })

  it('limit 可改，且不受条目总数影响', () => {
    // 右侧栏 5 条、热榜页 10 条，同一个组件只改这个 prop。
    const wrapper = mountCard({ items: makeItems(20), limit: 10 })

    expect(wrapper.findAll('.hr__item')).toHaveLength(10)
  })

  it('条目刚好等于 limit 时不给「查看完整榜单」', () => {
    const wrapper = mountCard({ items: makeItems(5) })

    // 只有 5 条却给一个「看全部」的入口，点进去还是这 5 条，会让人以为没生效。
    expect(wrapper.find('.hr__more').exists()).toBe(false)
  })

  it('条目多于 limit 时才给「查看完整榜单」', () => {
    const wrapper = mountCard({ items: makeItems(6) })

    expect(wrapper.find('.hr__more').exists()).toBe(true)
  })

  it('前三名单独标记，第四名起不标记', () => {
    const wrapper = mountCard({ items: makeItems(4) })

    const ranks = wrapper.findAll('.hr__rank')
    expect(ranks[0].classes()).toContain('is-top')
    expect(ranks[2].classes()).toContain('is-top')
    expect(ranks[3].classes()).not.toContain('is-top')
  })

  it('每行显示点赞数，不显示合成分数', () => {
    const wrapper = mountCard({ items: makeItems(1) })

    // 显示的是 like_count（10）而不是 score（130）：
    // 把合成分数当点赞数摆在侧栏，读者会以为这篇有 130 个人赞过。
    // 侧栏只放点赞数，浏览数留给热榜页整行展示——这里宽度不够。
    expect(wrapper.find('.hr__like').text()).toContain('10')
    expect(wrapper.text()).not.toContain('130')
  })

  it('没有数据时显示空态，且不显示骨架屏', () => {
    const wrapper = mountCard({ items: [] })

    expect(wrapper.find('.hr__empty').text()).toBe('榜单还是空的')
    expect(wrapper.find('.hr__skeleton').exists()).toBe(false)
  })

  it('首次加载时才给骨架屏，且不闪空态', () => {
    const wrapper = mountCard({ items: [], loading: true })

    // 加载中先说「榜单还是空的」，等数据回来又冒出一堆条目，观感像出错。
    expect(wrapper.find('.hr__skeleton').exists()).toBe(true)
    expect(wrapper.find('.hr__empty').exists()).toBe(false)
  })

  it('已有数据时刷新不塌成骨架屏', () => {
    // 轮询刷新时列表里有旧数据，这时整块换成骨架会让人以为内容丢了。
    const wrapper = mountCard({ items: makeItems(3), loading: true })

    expect(wrapper.find('.hr__skeleton').exists()).toBe(false)
    expect(wrapper.findAll('.hr__item')).toHaveLength(3)
  })

  it('标题可以换，默认是「热榜」', () => {
    expect(mountCard({ items: [] }).find('.hr__title').text()).toBe('热榜')
    expect(mountCard({ items: [], title: '本周热榜' }).find('.hr__title').text()).toBe('本周热榜')
  })
})
