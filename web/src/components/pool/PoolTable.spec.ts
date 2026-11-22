import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

// 组件只负责尺寸、DPR、生命周期与指针转发，绘制本身在 renderer 里已经测过，
// 这里把两个绘制入口都换掉，断言「有没有画、画的尺寸对不对」。
// 台面（木框/库边/台呢/袋口）与动态层（杆、瞄准线、球）是两个入口：
// 前者走离屏缓存，后者每帧重画，两者调用次数本身就是这项优化的判据。
const drawTable = vi.hoisted(() => vi.fn())
const drawActors = vi.hoisted(() => vi.fn())
// sinkingActive 决定要不要自己续一帧：落袋动画是唯一不靠外部输入推进的动画。
// 用例里默认「没有球在落袋」，所以返回 false 就够了。
const sinkingActive = vi.hoisted(() => vi.fn(() => false))
// TABLE_PADDING 是真实常量，指针换算要用它；只有绘制函数被替掉。
vi.mock('@/game/renderer', () => ({
  drawTable,
  drawActors,
  sinkingActive,
  TABLE_PADDING: 12,
}))

import PoolTable from './PoolTable.vue'
import type { BallState } from '@/game/types'

// jsdom 没有 ResizeObserver，也没有真实的布局尺寸。
// 前者补一个能触发回调的假实现，后者用 defineProperty 给画布一个固定尺寸。
const resizeCallbacks: ResizeObserverCallback[] = []
const disconnectSpy = vi.fn()

class FakeResizeObserver {
  private callback: ResizeObserverCallback

  constructor(callback: ResizeObserverCallback) {
    this.callback = callback
    resizeCallbacks.push(callback)
  }

  observe() {
    // 真实的 ResizeObserver 在 observe 之后会立刻回调一次，
    // 这里不模拟：组件在 onMounted 里已经主动画过一帧。
  }

  unobserve() {}

  disconnect = disconnectSpy
}

// triggerResize 手动触发所有观察者的回调，模拟容器尺寸变化。
function triggerResize() {
  for (const callback of resizeCallbacks) {
    callback([], {} as ResizeObserver)
  }
}

// balls 造一份两球球局。
function balls(): BallState[] {
  return [
    { id: 'cue', x: 0.5944, y: 0.4557, z: 0, on_table: true },
    { id: '8', x: 0.4953, y: 1.585, z: 0, on_table: true },
  ]
}

// jsdom 没有 Pointer Capture 的实现。组件调它只是为了「拖出元素仍能收到事件」，
// 测试不需要这个能力，补三个空实现即可。
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

beforeEach(() => {
  resizeCallbacks.length = 0
  disconnectSpy.mockClear()
  drawTable.mockClear()
  drawActors.mockClear()
  vi.stubGlobal('ResizeObserver', FakeResizeObserver)

  Object.defineProperty(HTMLCanvasElement.prototype, 'clientWidth', {
    configurable: true,
    get: () => 300,
  })
  Object.defineProperty(HTMLCanvasElement.prototype, 'clientHeight', {
    configurable: true,
    get: () => 600,
  })
  // 画布上下文在 jsdom 里是空的。这里只补 paint 真正用到的那几个：
  // 复位变换、清屏、把静态层贴回去。
  HTMLCanvasElement.prototype.getContext = (() => ({
    setTransform: vi.fn(),
    clearRect: vi.fn(),
    drawImage: vi.fn(),
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext
})

describe('PoolTable', () => {
  it('挂载后按 CSS 尺寸画一帧', () => {
    mount(PoolTable, { props: { balls: balls() } })
    expect(drawActors).toHaveBeenCalledTimes(1)
    const args = drawActors.mock.calls[0]
    expect(args[1]).toEqual({ width: 300, height: 600 })
  })

  it('位图尺寸按 CSS 尺寸乘 DPR 设置', () => {
    const wrapper = mount(PoolTable, { props: { balls: balls() } })
    const canvas = wrapper.find('canvas').element as HTMLCanvasElement
    expect(canvas.width).toBe(300)
    expect(canvas.height).toBe(600)
  })

  it('球位变化会重画', async () => {
    const wrapper = mount(PoolTable, { props: { balls: balls() } })
    drawActors.mockClear()
    wrapper.setProps({ balls: [...balls(), { id: '9', x: 0.4, y: 1.2, z: 0, on_table: true }] })
    await new Promise((r) => setTimeout(r, 40))
    expect(drawActors).toHaveBeenCalled()
    // 台呢、库边、六个袋口一帧一个像素都不变，却在拖动时每帧重画——
    // 那正是「动杆子发涩」的来源。球位变化只该重画动态层。
    expect(drawTable).toHaveBeenCalledTimes(1)
  })

  it('还有球在落袋时会自己续帧，播完就停', async () => {
    // 落袋动画只随时间推进，没有球位变化可等。不给它续帧的话，
    // 画面会停在「球刚被判落袋」的那一帧上——那颗球一直挂在袋口不消失。
    sinkingActive.mockReturnValue(true)
    const wrapper = mount(PoolTable, { props: { balls: balls() } })
    await new Promise((r) => setTimeout(r, 80))
    expect(drawActors.mock.calls.length).toBeGreaterThan(2)

    // 动画播完（sinkingActive 变假）之后必须停下来，否则就是永远的 rAF 空转。
    sinkingActive.mockReturnValue(false)
    await new Promise((r) => setTimeout(r, 40))
    const settled = drawActors.mock.calls.length
    await new Promise((r) => setTimeout(r, 60))
    expect(drawActors.mock.calls.length).toBe(settled)
    wrapper.unmount()
  })

  it('容器尺寸不变时复用静态台面，变了才重画', async () => {
    mount(PoolTable, { props: { balls: balls() } })
    // 挂载时画了一次台面。
    expect(drawTable).toHaveBeenCalledTimes(1)

    // 尺寸没变：静态层继续用缓存。
    triggerResize()
    await new Promise((r) => setTimeout(r, 40))
    expect(drawTable).toHaveBeenCalledTimes(1)

    // 尺寸变了：缓存作废，按新尺寸重画一次。
    Object.defineProperty(HTMLCanvasElement.prototype, 'clientHeight', {
      configurable: true,
      get: () => 800,
    })
    triggerResize()
    await new Promise((r) => setTimeout(r, 40))
    expect(drawTable).toHaveBeenCalledTimes(2)

    // 这个尺寸是改在原型上的，不还原会污染后面的用例。
    Object.defineProperty(HTMLCanvasElement.prototype, 'clientHeight', {
      configurable: true,
      get: () => 600,
    })
  })

  it('容器尺寸变化会重画', async () => {
    mount(PoolTable, { props: { balls: balls() } })
    drawActors.mockClear()
    triggerResize()
    await new Promise((r) => setTimeout(r, 40))
    expect(drawActors).toHaveBeenCalled()
  })

  it('卸载时断开尺寸观察，避免容器移除后还在回调', () => {
    const wrapper = mount(PoolTable, { props: { balls: balls() } })
    wrapper.unmount()
    expect(disconnectSpy).toHaveBeenCalled()
  })

  it('拖动上报相对起点的累计位移，并带上指针的台面坐标', async () => {
    const wrapper = mount(PoolTable, { props: { balls: balls() } })
    const host = wrapper.find('.pt')
    await host.trigger('pointerdown', { clientX: 100, clientY: 200, pointerId: 1, pointerType: 'mouse', button: 0 })
    await host.trigger('pointermove', { clientX: 130, clientY: 210, pointerId: 1, pointerType: 'mouse' })

    const events = wrapper.emitted('drag')!
    expect(events[0][0]).toMatchObject({ phase: 'start', dx: 0, dy: 0 })
    expect(events[1][0]).toMatchObject({ phase: 'move', dx: 30, dy: 10 })
    // point 是手中球挪位置用的：那里要的是「指针现在在哪」，
    // 只给位移的话球会停在起点，和手指错开一段。
    expect(events[1][0].point).not.toBeNull()
  })

  it('手中球的虚影会传给渲染层', async () => {
    const wrapper = mount(PoolTable, { props: { balls: balls(), ghost: { x: 0.5, y: 0.6 } } })
    await new Promise((r) => setTimeout(r, 40))

    const scene = drawActors.mock.calls.at(-1)?.[2] as { ghost?: { x: number; y: number } }
    expect(scene.ghost).toEqual({ x: 0.5, y: 0.6 })
    wrapper.unmount()
  })

  it('没有按下时鼠标划过不上报拖动，但会上报悬停位置', async () => {
    const wrapper = mount(PoolTable, { props: { balls: balls() } })
    await wrapper.find('.pt').trigger('pointermove', { clientX: 130, clientY: 210, pointerId: 1, pointerType: 'mouse' })
    expect(wrapper.emitted('drag')).toBeUndefined()
    // 桌面端「指哪打哪」靠这一条，缺了就只能靠拖动转角度。
    expect(wrapper.emitted('hover')).toHaveLength(1)
  })

  it('手指划过不上报悬停（触摸屏没有悬停）', async () => {
    const wrapper = mount(PoolTable, { props: { balls: balls() } })
    await wrapper.find('.pt').trigger('pointermove', { clientX: 130, clientY: 210, pointerId: 1, pointerType: 'touch' })
    expect(wrapper.emitted('hover')).toBeUndefined()
  })

  it('松手后不再上报移动', async () => {
    const wrapper = mount(PoolTable, { props: { balls: balls() } })
    const host = wrapper.find('.pt')
    await host.trigger('pointerdown', { clientX: 100, clientY: 200, pointerId: 1, pointerType: 'mouse', button: 0 })
    await host.trigger('pointerup', { clientX: 110, clientY: 200, pointerId: 1, pointerType: 'mouse' })
    await host.trigger('pointermove', { clientX: 160, clientY: 200, pointerId: 1, pointerType: 'mouse' })

    const events = wrapper.emitted('drag')!
    // start + end 两条，没有第三条 move。
    expect(events).toHaveLength(2)
    expect(events[1][0].phase).toBe('end')
  })

  it('右键按下不进入拖动状态', async () => {
    const wrapper = mount(PoolTable, { props: { balls: balls() } })
    await wrapper.find('.pt').trigger('pointerdown', { clientX: 100, clientY: 200, pointerId: 1, pointerType: 'mouse', button: 2 })
    expect(wrapper.emitted('drag')).toBeUndefined()
  })
})
