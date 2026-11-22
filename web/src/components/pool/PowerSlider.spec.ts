import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import PowerSlider from './PowerSlider.vue'

// 力度条的取值全靠指针位置换算，jsdom 里元素尺寸一律是 0，
// 所以每条用例先给轨道一个假矩形，否则所有位置都算成 0 或 1。
//
// 按下绑在组件上，拖动与松手绑在 window 上（滑轨只有 12px 宽，
// 手指斜着拖必然移出元素，只靠组件上的监听会中途断掉）。
// 所以 move / up 要往 window 上派发，往组件上 trigger 是收不到的。

const RECT = { top: 100, height: 200, bottom: 300, left: 0, right: 20, width: 20, x: 0, y: 100 }

// 每条用例结束后卸载：组件在 window 上挂了 move / up，
// 不卸载的话前一条的监听会留到下一条，把事件串起来。
const opened: Array<ReturnType<typeof mount>> = []

beforeAll(() => {
  if (!HTMLElement.prototype.setPointerCapture) {
    HTMLElement.prototype.setPointerCapture = () => {}
  }
})

afterEach(() => {
  for (const wrapper of opened) {
    wrapper.unmount()
  }
  opened.length = 0
})

// mountSlider 挂载并给轨道一个可控矩形。
function mountSlider(value: number, disabled = false) {
  const wrapper = mount(PowerSlider, { props: { value, disabled } })
  const track = wrapper.find('.pw__track').element as HTMLElement
  track.getBoundingClientRect = () => RECT as unknown as DOMRect
  opened.push(wrapper)
  return wrapper
}

// press 在指定 clientY 按下（鼠标左键，指针 1 号）。
async function press(wrapper: ReturnType<typeof mountSlider>, clientY: number) {
  await wrapper.trigger('pointerdown', { clientY, pointerId: 1, pointerType: 'mouse', button: 0 })
}

// fireWindow 往 window 上派发一个指针事件，模拟「手指已经移出组件」。
//
// jsdom 没有 PointerEvent 构造器，用 MouseEvent 补上 pointerId 即可：
// 组件只读这两个字段（pointerId 用来认手指，clientY 用来算力度）。
function fireWindow(type: 'pointermove' | 'pointerup' | 'pointercancel', clientY: number, pointerId = 1) {
  const event = new MouseEvent(type, { clientY, bubbles: true })
  Object.defineProperty(event, 'pointerId', { value: pointerId })
  Object.defineProperty(event, 'pointerType', { value: 'mouse' })
  window.dispatchEvent(event)
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
    fireWindow('pointerup', 220)
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('shoot')).toHaveLength(1)
  })

  it('力度几乎为零时松手不算出杆', async () => {
    const wrapper = mountSlider(0.01)
    await press(wrapper, 102)
    fireWindow('pointerup', 102)
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('shoot')).toBeUndefined()
  })

  it('没按下就滑动不会改力度', async () => {
    const wrapper = mountSlider(0.3)
    fireWindow('pointermove', 120)
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('update:value')).toBeUndefined()
  })

  it('禁用时按下与松手都不响应', async () => {
    const wrapper = mountSlider(0.5, true)
    await press(wrapper, 150)
    fireWindow('pointerup', 150)
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('update:value')).toBeUndefined()
    expect(wrapper.emitted('shoot')).toBeUndefined()
  })

  it('指针拖出滑轨之后仍然继续改力度', async () => {
    // 这条是回归用例：原来 move 只绑在组件上，滑轨只有 12px 宽，
    // 手指一斜就移出元素，力度停在移出那一刻的值，看起来像「拖不动了」。
    const wrapper = mountSlider(0)
    await press(wrapper, 140)
    fireWindow('pointermove', 200)
    fireWindow('pointermove', 260)
    await wrapper.vm.$nextTick()
    const values = wrapper.emitted('update:value')!.map((item) => item[0])
    expect(values[values.length - 1]).toBeCloseTo(0.8, 6)
  })

  it('松手之后滑动不再改力度', async () => {
    const wrapper = mountSlider(0)
    await press(wrapper, 140)
    fireWindow('pointerup', 140)
    fireWindow('pointermove', 280)
    await wrapper.vm.$nextTick()
    const values = wrapper.emitted('update:value')!.map((item) => item[0])
    expect(values[values.length - 1]).toBeCloseTo(0.2, 6)
  })

  it('另一根手指的移动不影响当前这根', async () => {
    const wrapper = mountSlider(0)
    await press(wrapper, 140)
    fireWindow('pointermove', 280, 2)
    await wrapper.vm.$nextTick()
    const values = wrapper.emitted('update:value')!.map((item) => item[0])
    expect(values[values.length - 1]).toBeCloseTo(0.2, 6)
  })

  it('显示百分比与传入值一致', () => {
    const wrapper = mount(PowerSlider, { props: { value: 0.42 } })
    opened.push(wrapper)
    expect(wrapper.find('.pw__value').text()).toBe('42')
  })
})
