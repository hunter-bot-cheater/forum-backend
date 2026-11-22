import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { PoolRecord, RecordsResp } from '@/api/pool'

// 战绩页的用例集中在五件事上：
//   1. 汇总数字怎么显示（尤其是一场没打、以及还没拉到时的胜率）；
//   2. 「拉失败」与「真的没战绩」不能混成同一种空态；
//   3. 每行的胜负标记与对手资料；
//   4. 翻页会带上新的页码再请求一次，且慢的旧请求不许盖回新页；
//   5. 返回键走 goBack。
// 页面里没有 WebSocket，也没有计时器，所以不需要假时钟。

const mocks = vi.hoisted(() => ({
  getRecords: vi.fn(),
  goBack: vi.fn(),
}))

vi.mock('@/api/pool', () => ({
  getRecords: (page: number, size: number) => mocks.getRecords(page, size),
}))

vi.mock('@/utils/nav', () => ({
  goBack: (router: unknown, fallback: string) => mocks.goBack(fallback),
}))

vi.mock('vue-router', async (importOriginal) => {
  // 只换 useRouter，其余导出保留：整份替换会让 RouterLink 之类在后面加进来就炸。
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    useRouter: () => ({ replace: vi.fn(), push: vi.fn(), back: vi.fn() }),
  }
})

const RecordsPage = (await import('@/views/pool/records.vue')).default

// record 造一行战绩，默认是一场赢下来的局。
function record(patch: Partial<PoolRecord> = {}): PoolRecord {
  const base: PoolRecord = {
    match_id: 11,
    opponent: { user_id: 2, nickname: '小满', avatar: '' },
    won: true,
    win_reason: 'eight_legal',
    shot_count: 9,
    ended_at: '2026-11-20 21:05:00',
  }
  return { ...base, ...patch }
}

// resp 造一份战绩响应。
function resp(list: PoolRecord[], total?: number): RecordsResp {
  const wins = list.filter((item) => item.won).length
  const losses = list.length - wins
  return {
    rating: { elo: 1032, wins, losses },
    list,
    total: total === undefined ? list.length : total,
    page: 1,
    size: 10,
  }
}

const stubs = {
  'el-avatar': { template: '<span class="el-avatar"><slot /></span>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  // 页头那个返回箭头只是装饰，替身成一个空容器就够了。
  'el-icon': { template: '<span class="el-icon"><slot /></span>' },
  'el-pagination': {
    template: '<div class="el-pagination" @click="$emit(\'current-change\', 2)" />',
    props: ['currentPage', 'pageSize', 'total'],
  },
  RouterLink: { template: '<a class="router-link"><slot /></a>' },
}

async function mountPage(data: RecordsResp | Error) {
  if (data instanceof Error) {
    mocks.getRecords.mockRejectedValue(data)
  } else {
    mocks.getRecords.mockResolvedValue(data)
  }
  const pinia = createPinia()
  setActivePinia(pinia)

  const wrapper = mount(RecordsPage, {
    global: { plugins: [pinia], stubs },
  })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  mocks.getRecords.mockReset()
  mocks.goBack.mockReset()
})

describe('战绩页', () => {
  it('汇总条显示场次与胜负，胜率按局数算出来', async () => {
    const wrapper = await mountPage(resp([record(), record({ match_id: 12, won: false })]))

    const stats = wrapper.findAll('.rec__stat-value')
    // 场次 = 胜 + 负，积分不再出现在界面上。
    expect(stats[0].text()).toBe('2')
    expect(stats[1].text()).toBe('1 / 1')
    expect(stats[2].text()).toBe('50%')
  })

  it('一场没打时胜率显示短横，而不是 0%', async () => {
    const wrapper = await mountPage(resp([]))

    const stats = wrapper.findAll('.rec__stat-value')
    // 0% 会被读成「打了全输」，与「还没打过」是两件事。
    expect(stats[2].text()).toBe('—')
    expect(wrapper.find('.rec__empty').text()).toContain('还没有打完的对局')
  })

  it('拉失败时给重试入口，不说成「没有战绩」', async () => {
    const wrapper = await mountPage(new Error('boom'))

    const empty = wrapper.find('.rec__empty')
    expect(empty.text()).toContain('没加载出来')
    expect(empty.text()).not.toContain('还没有打完的对局')
    expect(wrapper.find('.rec__retry').exists()).toBe(true)
  })

  it('每行显示对手、胜负标记与杆数', async () => {
    const wrapper = await mountPage(
      resp([
        record(),
        record({ match_id: 12, won: false, win_reason: 'leave', shot_count: 4 }),
      ]),
    )

    const rows = wrapper.findAll('.rec__row')
    expect(rows).toHaveLength(2)
    expect(rows[0].find('.rec__flag').text()).toBe('胜')
    expect(rows[0].find('.rec__flag').classes()).toContain('is-win')
    expect(rows[0].text()).toContain('小满')
    expect(rows[0].text()).toContain('打进黑八')
    expect(rows[0].text()).toContain('9 杆')

    expect(rows[1].find('.rec__flag').text()).toBe('负')
    expect(rows[1].find('.rec__flag').classes()).toContain('is-lose')
    // 赢法码是赢家视角记的：输家那一行不能写成「对手中途离开」。
    expect(rows[1].text()).toContain('中途离开')
    expect(rows[1].text()).not.toContain('对手中途离开')
    expect(rows[1].text()).toContain('4 杆')
  })

  it('赢的那一行才把功劳写成「对手…」', async () => {
    const wrapper = await mountPage(resp([record({ win_reason: 'leave' })]))

    const row = wrapper.find('.rec__row')
    expect(row.text()).toContain('对手中途离开')
  })

  it('翻页会带着第 2 页再请求一次', async () => {
    const wrapper = await mountPage(resp([record()], 25))

    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()

    expect(mocks.getRecords).toHaveBeenCalledTimes(2)
    expect(mocks.getRecords).toHaveBeenLastCalledWith(2, 10)
  })

  // 汇总条在拿到数据之前不能摆出 1000 / 0 胜 0 负：
  // 请求失败时那三个数字会留在页面上，与下面那句「没加载出来」互相打架，
  // 看起来还像「我的战绩被清零了」。
  it('还没拉到数据时汇总条显示短横，不拿默认值顶上', async () => {
    // 永不 resolve 的请求：页面停在「首次加载中」。
    mocks.getRecords.mockReturnValue(new Promise(() => {}))

    const pinia = createPinia()
    setActivePinia(pinia)
    const wrapper = mount(RecordsPage, { global: { plugins: [pinia], stubs } })
    await flushPromises()

    const stats = wrapper.findAll('.rec__stat-value')
    expect(stats[0].text()).toBe('—')
    expect(stats[1].text()).toBe('— / —')
    expect(stats[2].text()).toBe('—')
    expect(stats[0].text()).not.toBe('1000')
  })

  // 连点两页时分页控件不会禁用，两条请求同时在途。
  // 先发的那条后到时如果照写，用户翻到新一页看到的却是上一页的内容。
  it('两条请求同时在途时，后发的才算数', async () => {
    let resolveOlder: (value: RecordsResp) => void = () => {}
    let resolveNewer: (value: RecordsResp) => void = () => {}

    // 第一条直接返回第 1 页：分页器要等 total 出来才渲染。
    mocks.getRecords.mockResolvedValueOnce(resp([record()], 25))
    mocks.getRecords.mockReturnValueOnce(
      new Promise<RecordsResp>((resolve) => {
        resolveOlder = resolve
      }),
    )
    mocks.getRecords.mockReturnValueOnce(
      new Promise<RecordsResp>((resolve) => {
        resolveNewer = resolve
      }),
    )

    const pinia = createPinia()
    setActivePinia(pinia)
    const wrapper = mount(RecordsPage, { global: { plugins: [pinia], stubs } })
    await flushPromises()

    // 连点两次，排进两条还在途的请求。
    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()
    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()
    expect(mocks.getRecords).toHaveBeenCalledTimes(3)

    // 后发的先回来，先发的后回来：最终留下的必须是后发那条。
    resolveNewer(
      resp([record({ match_id: 21, opponent: { user_id: 3, nickname: '后发的那页', avatar: '' } })]),
    )
    await flushPromises()
    resolveOlder(
      resp([record({ match_id: 31, opponent: { user_id: 4, nickname: '先发的那页', avatar: '' } })]),
    )
    await flushPromises()

    expect(wrapper.text()).toContain('后发的那页')
    expect(wrapper.text()).not.toContain('先发的那页')
  })

  // 返回键走 goBack 而不是 router.back()：从大厅进来再回去，
  // 页面各自 push 会把历史压满，用户连点几次就退不出去。
  it('返回键走 goBack，退回大厅', async () => {
    const wrapper = await mountPage(resp([record()]))

    await wrapper.find('.rec__back').trigger('click')

    expect(mocks.goBack).toHaveBeenCalledWith('/pool')
  })
})
