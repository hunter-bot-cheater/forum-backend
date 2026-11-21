import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import type { ReplayResp } from '@/api/pool'

// 回放页的用例集中在三件事上：
//   1. 跳到某一杆开头时，台面上的球要「该怎么看就怎么看」——
//      母球落袋后由下一杆摆回去，跳杆时必须把首帧也套上；
//   2. 不能重试的几种状态（不是你的局 / 没有这局 / 还没结算）不给重试按钮；
//   3. 播放/暂停/上一杆/下一杆的边界。
//
// 播放本身用假的 playShot 顶掉：它依赖 requestAnimationFrame 与真实时间，
// 在 jsdom 里等它跑完既慢又不稳。sampleAt / applySample 保持真实实现，
// 因为跳杆的正误全在这两个函数上。

const mocks = vi.hoisted(() => ({
  getReplay: vi.fn(),
  playShot: vi.fn(),
}))

vi.mock('@/api/pool', () => ({
  getReplay: (id: string) => mocks.getReplay(id),
}))

vi.mock('@/game/animator', async (importOriginal) => {
  // 只换 playShot，其余（sampleAt / applySample）保留真实实现。
  const actual = await importOriginal<typeof import('@/game/animator')>()
  return {
    ...actual,
    playShot: (shot: unknown, options: unknown) => mocks.playShot(shot, options),
  }
})

vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    useRouter: () => ({ replace: vi.fn(), push: vi.fn(), back: vi.fn() }),
  }
})

const ReplayPage = (await import('@/views/pool/replay.vue')).default

// frame 造一帧。balls 是 [球号, x, y, z]，与协议一致。
function frame(t: number, balls: Array<[string, number, number, number]>) {
  return { t, balls }
}

// replayResp 造一份回放：两杆，第一杆结束时母球已经落袋。
//
// 这正是真实局面里会出现的形状——母球进袋后，它由下一杆摆位重新回到台面，
// 所以「第 1 杆末帧」与「第 2 杆首帧」的母球状态是不一样的。
function replayResp(): ReplayResp {
  return {
    match_id: 7,
    players: [
      { user_id: 1, nickname: '阿泽', avatar: '' },
      { user_id: 2, nickname: '小满', avatar: '' },
    ],
    winner: 0,
    win_reason: 'eight_legal',
    shots: [
      {
        seq: 1,
        seat: 0,
        duration: 1,
        frames: [
          frame(0, [
            ['cue', 0.5, 0.5, 0],
            ['9', 0.6, 1.0, 0],
          ]),
          // 母球进袋：这一帧之后它就不在台面上了。
          frame(1, [['9', 0.7, 1.1, 0]]),
        ],
      },
      {
        seq: 2,
        seat: 1,
        duration: 1,
        frames: [
          // 第 2 杆开头母球被摆回来。
          frame(0, [
            ['cue', 0.5, 0.6, 0],
            ['9', 0.7, 1.1, 0],
          ]),
          frame(1, [
            ['cue', 0.5, 0.6, 0],
            ['9', 0.8, 1.2, 0],
          ]),
        ],
      },
    ],
  }
}

// PoolTable 换成一句「台面上有哪些球」，好让断言直接看得见。
const stubs = {
  PoolTable: {
    props: ['balls'],
    template:
      '<div class="tbl"><span v-for="b in balls" :key="b.id">{{ b.id }}{{ b.on_table ? "+" : "-" }}</span></div>',
  },
  'el-icon': { template: '<span class="el-icon"><slot /></span>' },
}

// mountPage 装一次页面。error 非空时让请求失败。
async function mountPage(error?: unknown) {
  if (error === undefined) {
    mocks.getReplay.mockResolvedValue(replayResp())
  } else {
    mocks.getReplay.mockRejectedValue(error)
  }
  const wrapper = mount(ReplayPage, {
    props: { id: '7' },
    global: { stubs },
  })
  await flushPromises()
  return wrapper
}

// cueState 读台面上母球的标记：'+' 在台面，'-' 已落袋。
function cueState(wrapper: VueWrapper) {
  return wrapper.find('.tbl').text().includes('cue+')
}

beforeEach(() => {
  mocks.getReplay.mockReset()
  mocks.playShot.mockReset()
  // 假的播放器：什么都不做，只给一个能 stop 的手柄。
  mocks.playShot.mockReturnValue({ stop: () => {} })
})

describe('回放页', () => {
  it('进页面就停在第一杆开头，母球在台面上', async () => {
    const wrapper = await mountPage()

    expect(wrapper.find('.tbl').text()).toContain('cue+')
    expect(wrapper.find('.rp__count').text()).toBe('第 1 / 2 杆')
  })

  // 这条守的是跳杆时最容易漏的一步：只套上一杆末帧，母球永远回不来。
  it('跳到第 2 杆开头时把母球摆回台面', async () => {
    const wrapper = await mountPage()

    // 第 2 个按钮是「下一杆」。
    await wrapper.findAll('.rp__step')[1].trigger('click')

    expect(cueState(wrapper)).toBe(true)
    expect(wrapper.find('.rp__count').text()).toBe('第 2 / 2 杆')
  })

  it('从第 2 杆退回第 1 杆时母球也应当在场', async () => {
    const wrapper = await mountPage()

    await wrapper.findAll('.rp__step')[1].trigger('click')
    await wrapper.findAll('.rp__step')[0].trigger('click')

    expect(cueState(wrapper)).toBe(true)
    expect(wrapper.find('.rp__count').text()).toBe('第 1 / 2 杆')
  })

  it('标题栏写不带视角的赢法，两边名字都在', async () => {
    const wrapper = await mountPage()

    const reason = wrapper.find('.rp__reason').text()
    // 回放页没有「我」，不能出现「你 / 对手」。
    expect(reason).toBe('打进黑八')
    expect(reason).not.toContain('你')
    expect(reason).not.toContain('对手')
    expect(wrapper.find('.rp__who').text()).toContain('阿泽')
    expect(wrapper.find('.rp__who').text()).toContain('小满')
  })

  it('还没拉到数据时两侧名字都不标成赢家', async () => {
    mocks.getReplay.mockReturnValue(new Promise(() => {}))
    const wrapper = mount(ReplayPage, { props: { id: '7' }, global: { stubs } })
    await flushPromises()

    // 用 0 兜底的话，左半边会被提前描成赢家。
    expect(wrapper.findAll('.rp__name.is-win')).toHaveLength(0)
  })

  it('这一局不是自己的：说明原因，且不给重试按钮', async () => {
    const wrapper = await mountPage({ response: { status: 403 } })

    const hint = wrapper.find('.rp__hint')
    expect(hint.text()).toContain('不是你的对局')
    // 重试必然得到同一个 403，给按钮只会让人反复点。
    expect(wrapper.find('.rp__retry').exists()).toBe(false)
  })

  it('还没结算：说明等结算之后再来看', async () => {
    const wrapper = await mountPage({ response: { status: 409 } })

    expect(wrapper.find('.rp__hint').text()).toContain('还没结束')
    expect(wrapper.find('.rp__retry').exists()).toBe(false)
  })

  it('网络或服务端出错：给重试入口', async () => {
    const wrapper = await mountPage(new Error('boom'))

    expect(wrapper.find('.rp__hint').text()).toContain('没加载出来')
    expect(wrapper.find('.rp__retry').exists()).toBe(true)
  })
})
