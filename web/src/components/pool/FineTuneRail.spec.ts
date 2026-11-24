import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import FineTuneRail from './FineTuneRail.vue'

// 微调只做两件事：报出一个明确的步长、把当前角度显示清楚。
// 这里逐条核对步长与正负，避免出现一次按下转两格这类重复计算。

// mountRail 挂载微调条，phi 用弧度。
function mountRail(phi: number, disabled = false) {
  return mount(FineTuneRail, { props: { phi, disabled } })
}

describe('FineTuneRail：两个步长按钮', () => {
  it('按左边往负方向走 0.05 度', async () => {
    const wrapper = mountRail(1)
    await wrapper.findAll('.ft__step')[0].trigger('pointerdown')
    expect(wrapper.emitted('nudge')![0]).toEqual([-0.05])
  })

  it('按右边往正方向走 0.05 度', async () => {
    const wrapper = mountRail(1)
    await wrapper.findAll('.ft__step')[1].trigger('pointerdown')
    expect(wrapper.emitted('nudge')![0]).toEqual([0.05])
  })

  it('禁用时按钮不发事件', async () => {
    const wrapper = mountRail(1, true)
    await wrapper.findAll('.ft__step')[1].trigger('pointerdown')
    expect(wrapper.emitted('nudge')).toBeUndefined()
  })

  it('键盘的 Enter 与空格同样能走一格', async () => {
    const wrapper = mountRail(1)
    await wrapper.findAll('.ft__step')[1].trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('nudge')![0]).toEqual([0.05])
  })
})

describe('FineTuneRail：连发', () => {
  it('按住不放会一直走，抬手就停', async () => {
    vi.useFakeTimers()
    try {
      const wrapper = mountRail(1)
      const right = wrapper.findAll('.ft__step')[1]

      // 按下立刻走一格。
      await right.trigger('pointerdown')
      expect(wrapper.emitted('nudge')!).toHaveLength(1)

      // 350ms 之后开始连发：再等 210ms 应当多出三格。
      vi.advanceTimersByTime(350 + 55 * 3)
      expect(wrapper.emitted('nudge')!.length).toBeGreaterThanOrEqual(4)

      // 抬手之后不再增长。
      await right.trigger('pointerup')
      const count = wrapper.emitted('nudge')!.length
      vi.advanceTimersByTime(1000)
      expect(wrapper.emitted('nudge')!.length).toBe(count)
    } finally {
      vi.useRealTimers()
    }
  })

  it('还没到连发时间就抬手，只走一格', async () => {
    vi.useFakeTimers()
    try {
      const wrapper = mountRail(1)
      const right = wrapper.findAll('.ft__step')[1]
      await right.trigger('pointerdown')
      await right.trigger('pointerup')
      vi.advanceTimersByTime(1000)
      expect(wrapper.emitted('nudge')!).toHaveLength(1)
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('FineTuneRail：角度显示', () => {
  it('90 度显示 90.0', () => {
    const wrapper = mountRail(Math.PI / 2)
    expect(wrapper.find('.ft__angle').text()).toBe('90.00°')
  })

  it('负角度折回 0 到 360 之间', () => {
    const wrapper = mountRail(-0.1)
    expect(wrapper.find('.ft__angle').text()).toBe('354.27°')
  })

  it('超过一圈也落在 0 到 360 之间', () => {
    const wrapper = mountRail(Math.PI * 2 + Math.PI)
    expect(wrapper.find('.ft__angle').text()).toBe('180.00°')
  })
})
