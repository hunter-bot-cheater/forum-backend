import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import PowerSlider from './PowerSlider.vue'

// 力度条的取值全靠指针位置换算，jsdom 里元素尺寸一律是 0，
// 所以每条用例先给轨道一个假矩形，否则所有位置都算成 0 或 1。
//
// setPointerCapture 在 jsdom 里没有实现，组件里调它只是为了「拖出元素仍能收到事件」，
// 测试不需要这个能力，补三个空实现即可。

const RECT = { top: 100, height: 200, bottom: 300, left: 0, right: 20, width: 20, x: 0, y: 100 }

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

// mountSlider 挂载并给轨道一个可控矩形。
function mountSlider(value: number, disabled = false) {
  const wrapper = mount(PowerSlider, { props: { value, disabled } })
  const track = wrapper.find('.pw__track').element as HTMLElement
  track.getBoundingClientRect = () => RECT as unknown as DOMRect
  return wrapper
}

// press 在指定 clientY 按下（鼠标左键）。
async function press(wrapper: ReturnType<typeof mountSlider>, clientY: number) {
  await wrapper.trigger('pointerdown', { clientY, pointerId: 1, pointerType: 'mouse', button: 0 })
}

describe('PowerSlider', () => {
  it('在轨道顶部按下是 0 力度', async () => {
    const wrapper = mountSlider(0.5)
    await press(wrapper, 100)
    const emitted = wrapper.emitted('update:value')
    expect(emitted).toBeTruthy()
    expect(emitted![0][0]).toBeCloseTo(0, 6)
  })

  it('拉到轨道底部是满力', async () => {
    const wrapper = mountSlider(0)
    await press(wrapper, 300)
    const emitted = wrapper.emitted('update:value')
    expect(emitted![0][0]).toBeCloseTo(1, 6)
  })

  it('按在中间给一半', async () => {
    const wrapper = mountSlider(0)
    await press(wrapper, 200)
    expect(wrapper.emitted('update:value')![0][0]).toBeCloseTo(0.5, 6)
  })

  it('超出轨道下方不会超过 1', async () => {
    const wrapper = mountSlider(0)
    await press(wrapper, 380)
    expect(wrapper.emitted('update:value')![0][0]).toBe(1)
  })

  it('松手后出杆', async () => {
    const wrapper = mountSlider(0.6)
    await press(wrapper, 220)
    await wrapper.trigger('pointerup', { clientY: 220, pointerId: 1, pointerType: 'mouse' })
    expect(wrapper.emitted('shoot')).toHaveLength(1)
  })

  it('力度几乎为零时松手不算出杆', async () => {
    const wrapper = mountSlider(0.01)
    await press(wrapper, 102)
    await wrapper.trigger('pointerup', { clientY: 102, pointerId: 1, pointerType: 'mouse' })
    expect(wrapper.emitted('shoot')).toBeUndefined()
  })

  it('没按下就滑动不会改力度', async () => {
    const wrapper = mountSlider(0.3)
    await wrapper.trigger('pointermove', { clientY: 120, pointerId: 1, pointerType: 'mouse' })
    expect(wrapper.emitted('update:value')).toBeUndefined()
  })

  it('禁用时按下与松手都不响应', async () => {
    const wrapper = mountSlider(0.5, true)
    await press(wrapper, 150)
    await wrapper.trigger('pointerup', { clientY: 150, pointerId: 1, pointerType: 'mouse' })
    expect(wrapper.emitted('update:value')).toBeUndefined()
    expect(wrapper.emitted('shoot')).toBeUndefined()
  })

  it('显示百分比与传入值一致', () => {
    const wrapper = mount(PowerSlider, { props: { value: 0.42 } })
    expect(wrapper.find('.pw__value').text()).toBe('42')
  })
})
