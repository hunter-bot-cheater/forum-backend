import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import FineTuneRail from './FineTuneRail.vue'

// 微调尺的两个入口（按钮与拖动）都必须只报「转了多少度」，
// 这里逐条核对上报的增量，避免出现一次点击转两格这类重复计算。

beforeAll(() => {
  if (!HTMLElement.prototype.setPointerCapture) {
    HTMLElement.prototype.setPointerCapture = () => {}
  }
  if (!HTMLElement.prototype.releasePointerCapture) {
    HTMLElement.prototype.releasePointerCapture = () => {}
  }
  if (!HTMLElement.prototype.hasPointerCapture) {
    HTMLElement.prototype.hasPointerCapture = () => false
  }
})

// mountRail 挂载微调尺，phi 用弧度。
function mountRail(phi: number, disabled = false) {
  return mount(FineTuneRail, { props: { phi, disabled } })
}

describe('FineTuneRail：按钮', () => {
  it('点左箭头往负方向走 0.1 度', async () => {
    const wrapper = mountRail(1)
    await wrapper.findAll('.ft__step')[0].trigger('click')
    expect(wrapper.emitted('nudge')![0]).toEqual([-0.1])
  })

  it('点右箭头往正方向走 0.1 度', async () => {
    const wrapper = mountRail(1)
    await wrapper.findAll('.ft__step')[1].trigger('click')
    expect(wrapper.emitted('nudge')![0]).toEqual([0.1])
  })

  it('禁用时按钮不发事件', async () => {
    const wrapper = mountRail(1, true)
    await wrapper.findAll('.ft__step')[1].trigger('click')
    expect(wrapper.emitted('nudge')).toBeUndefined()
  })
})

describe('FineTuneRail：角度显示', () => {
  it('90 度显示 90.0', () => {
    const wrapper = mountRail(Math.PI / 2)
    expect(wrapper.find('.ft__angle').text()).toBe('90.0°')
  })

  it('负角度折回 0 到 360 之间', () => {
    const wrapper = mountRail(-0.1)
    expect(wrapper.find('.ft__angle').text()).toBe('354.3°')
  })

  it('超过一圈也落在 0 到 360 之间', () => {
    const wrapper = mountRail(Math.PI * 2 + Math.PI)
    expect(wrapper.find('.ft__angle').text()).toBe('180.0°')
  })
})

describe('FineTuneRail：拖动', () => {
  // drag 在轨道上按下并移动到相对起点 offset 像素处。
  async function drag(wrapper: ReturnType<typeof mountRail>, offset: number) {
    const rail = wrapper.find('.ft__rail')
    await rail.trigger('pointerdown', { clientX: 100, pointerId: 1, pointerType: 'mouse', button: 0 })
    await rail.trigger('pointermove', { clientX: 100 + offset, pointerId: 1, pointerType: 'mouse' })
  }

  it('拖 8 像素一格，是 0.1 度', async () => {
    const wrapper = mountRail(1)
    await drag(wrapper, 8)
    expect(wrapper.emitted('nudge')![0][0]).toBeCloseTo(0.1, 6)
  })

  it('拖 16 像素是 0.2 度，一次报够而不是拆成两次', async () => {
    const wrapper = mountRail(1)
    await drag(wrapper, 16)
    const events = wrapper.emitted('nudge')!
    expect(events).toHaveLength(1)
    expect(events[0][0]).toBeCloseTo(0.2, 6)
  })

  it('不足一格的位移不触发', async () => {
    const wrapper = mountRail(1)
    await drag(wrapper, 5)
    expect(wrapper.emitted('nudge')).toBeUndefined()
  })

  it('往回拖报负增量', async () => {
    const wrapper = mountRail(1)
    const rail = wrapper.find('.ft__rail')
    await rail.trigger('pointerdown', { clientX: 100, pointerId: 1, pointerType: 'mouse', button: 0 })
    await rail.trigger('pointermove', { clientX: 84, pointerId: 1, pointerType: 'mouse' })
    expect(wrapper.emitted('nudge')![0][0]).toBeCloseTo(-0.2, 6)
  })

  it('分两次移动只报差额，不把已经转过的度数再加一遍', async () => {
    const wrapper = mountRail(1)
    const rail = wrapper.find('.ft__rail')
    await rail.trigger('pointerdown', { clientX: 100, pointerId: 1, pointerType: 'mouse', button: 0 })
    await rail.trigger('pointermove', { clientX: 108, pointerId: 1, pointerType: 'mouse' })
    await rail.trigger('pointermove', { clientX: 116, pointerId: 1, pointerType: 'mouse' })
    const events = wrapper.emitted('nudge')!
    expect(events).toHaveLength(2)
    expect(events[0][0]).toBeCloseTo(0.1, 6)
    expect(events[1][0]).toBeCloseTo(0.1, 6)
  })

  it('没按下就滑动不触发', async () => {
    const wrapper = mountRail(1)
    await wrapper.find('.ft__rail').trigger('pointermove', { clientX: 120, pointerId: 1, pointerType: 'mouse' })
    expect(wrapper.emitted('nudge')).toBeUndefined()
  })

  it('禁用时拖动不触发', async () => {
    const wrapper = mountRail(1, true)
    await drag(wrapper, 16)
    expect(wrapper.emitted('nudge')).toBeUndefined()
  })
})
