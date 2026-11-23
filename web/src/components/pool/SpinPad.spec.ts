import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import SpinPad from './SpinPad.vue'

// 加塞面板靠「指针落在球面哪个位置」换算塞量。jsdom 里元素尺寸一律是 0，
// 所以先给球面一个假矩形（200×200、左上角在 100,100），
// 于是球心是 (200, 200)、半径 100——按下 (250, 200) 就是右塞 50%。

const RECT = {
  top: 100,
  left: 100,
  right: 300,
  bottom: 300,
  width: 200,
  height: 200,
  x: 100,
  y: 100,
}

const opened: Array<ReturnType<typeof mount>> = []

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

afterEach(() => {
  for (const wrapper of opened) {
    wrapper.unmount()
  }
  opened.length = 0
})

// mountPad 挂载面板。open 为 true 时先点一下展开。
async function mountPad(a = 0, b = 0, disabled = false, open = true) {
  const wrapper = mount(SpinPad, { props: { a, b, disabled } })
  opened.push(wrapper)
  if (open) {
    await wrapper.find('.sp__toggle').trigger('click')
    const stage = wrapper.find('.sp__stage').element as HTMLElement
    stage.getBoundingClientRect = () => RECT as unknown as DOMRect
  }
  return wrapper
}

// press 在球面某个坐标按下（指针 1 号，鼠标）。
async function press(wrapper: ReturnType<typeof mount>, clientX: number, clientY: number) {
  const stage = wrapper.find('.sp__stage')
  await stage.trigger('pointerdown', { pointerId: 1, pointerType: 'mouse', clientX, clientY })
  return stage
}

describe('加塞面板', () => {
  it('默认是收起的，只露一颗小球和角度', async () => {
    const wrapper = await mountPad(0, 0, false, false)
    expect(wrapper.find('.sp__panel').exists()).toBe(false)
    expect(wrapper.find('.sp__badge').text()).toBe('0°')
  })

  it('点开之后出现球面、重置与读数', async () => {
    const wrapper = await mountPad(0, 0)
    expect(wrapper.find('.sp__panel').exists()).toBe(true)
    expect(wrapper.find('.sp__reset').text()).toBe('重置')
    expect(wrapper.find('.sp__value').text()).toBe('(0.0,0.0)')
  })

  it('球心右边按下报右塞，屏幕上方按下报高杆', async () => {
    const wrapper = await mountPad(0, 0)
    // 球心在 (200,200)，半径 100：右边 50px 就是 a=0.5，所以 x=250。
    await press(wrapper, 250, 200)
    expect(wrapper.emitted('update:a')![0]).toEqual([0.5])
    expect(wrapper.emitted('update:b')![0]).toEqual([0])
  })

  it('屏幕上方是高杆（b 为正）', async () => {
    const wrapper = await mountPad(0, 0)
    await press(wrapper, 200, 150)
    expect(wrapper.emitted('update:b')![0]).toEqual([0.5])
  })

  it('中心附近有死区：点在小圈里吸附回中杆', async () => {
    const wrapper = await mountPad(0.6, 0.6)
    // 离球心 4px，占半径 4%，在 8% 的死区里。
    await press(wrapper, 204, 196)
    expect(wrapper.emitted('update:a')![0]).toEqual([0])
    expect(wrapper.emitted('update:b')![0]).toEqual([0])
  })

  it('拖出球面时沿半径夹到最大值，不会跳成 0', async () => {
    const wrapper = await mountPad(0, 0)
    await press(wrapper, 400, 200)
    expect(wrapper.emitted('update:a')![0]).toBeCloseTo(0.92, 2)
    expect(wrapper.emitted('update:b')![0]).toEqual([0])
  })

  it('重置把击球点放回圆心', async () => {
    const wrapper = await mountPad(0.4, -0.3)
    await wrapper.find('.sp__reset').trigger('click')
    expect(wrapper.emitted('update:a')![0]).toEqual([0])
    expect(wrapper.emitted('update:b')![0]).toEqual([0])
  })

  it('不是自己回合时点不开', async () => {
    const wrapper = await mountPad(0, 0, true, false)
    await wrapper.find('.sp__toggle').trigger('click')
    expect(wrapper.find('.sp__panel').exists()).toBe(false)
  })

  it('读数按参考图的格式显示百分点', async () => {
    const wrapper = await mountPad(-0.005, -0.286)
    expect(wrapper.find('.sp__value').text()).toBe('(-0.5,-28.6)')
  })
})
