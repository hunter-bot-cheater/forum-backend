import { describe, expect, it } from 'vitest'
import {
  TABLE_OUTER_LENGTH,
  TABLE_OUTER_WIDTH,
  fitTable,
  metersToPx,
  screenToWorld,
  worldToScreen,
} from './geometry'
import { CUSHION_WIDTH, TABLE_LENGTH, TABLE_WIDTH } from './table'

// 这层是所有绘制的公共分母，测的是三个不变量：
// 等比（不拉伸）、居中（留白对称）、可逆（点回去还是原来的地方）。

// contentOf 由 transform 反推外层矩形在画布里的宽高，避免用例里重复算。
function contentOf(t: { scale: number }) {
  return { width: TABLE_OUTER_WIDTH * t.scale, height: TABLE_OUTER_LENGTH * t.scale }
}

describe('fitTable', () => {
  it('竖屏手机由宽度受限，台面横向铺满可用区', () => {
    const view = { width: 390, height: 844 }
    const pad = 8
    const t = fitTable(view, pad)
    const content = contentOf(t)
    expect(content.width).toBeCloseTo(390 - pad * 2, 6)
    expect(content.height).toBeLessThan(844 - pad * 2)
  })

  it('桌面由高度受限，台面纵向铺满可用区', () => {
    const view = { width: 1440, height: 900 }
    const pad = 8
    const t = fitTable(view, pad)
    const content = contentOf(t)
    expect(content.height).toBeCloseTo(900 - pad * 2, 6)
    expect(content.width).toBeLessThan(1440 - pad * 2)
  })

  it('左右留白对称，台面不会偏到一边', () => {
    const view = { width: 1440, height: 900 }
    const t = fitTable(view, 8)
    const content = contentOf(t)
    const left = t.offsetX
    const right = view.width - t.offsetX - content.width
    expect(Math.abs(left - right)).toBeLessThan(1e-6)
  })

  it('上下留白对称', () => {
    const view = { width: 390, height: 844 }
    const t = fitTable(view, 8)
    const content = contentOf(t)
    const top = t.offsetY
    const bottom = view.height - t.offsetY - content.height
    expect(Math.abs(top - bottom)).toBeLessThan(1e-6)
  })

  it('等比缩放：台面宽高比在任何容器下都不变', () => {
    const cases = [
      { width: 390, height: 844 },
      { width: 1440, height: 900 },
      { width: 768, height: 1024 },
    ]
    for (const view of cases) {
      const t = fitTable(view, 12)
      const content = contentOf(t)
      expect(content.width / content.height).toBeCloseTo(TABLE_OUTER_WIDTH / TABLE_OUTER_LENGTH, 9)
    }
  })

  it('容器宽度为 0 时不产生 NaN 或 Infinity', () => {
    const t = fitTable({ width: 0, height: 0 }, 8)
    expect(Number.isFinite(t.scale)).toBe(true)
    expect(Number.isFinite(t.offsetX)).toBe(true)
    expect(Number.isFinite(t.offsetY)).toBe(true)
    expect(t.scale).toBeGreaterThan(0)
  })

  it('留白大于半个容器时也不会算出负数缩放', () => {
    const t = fitTable({ width: 10, height: 10 }, 100)
    expect(t.scale).toBeGreaterThan(0)
    expect(Number.isFinite(t.offsetX)).toBe(true)
  })
})

describe('worldToScreen / screenToWorld', () => {
  const view = { width: 390, height: 844 }
  const t = fitTable(view, 8)

  it('世界原点落在屏幕左下角，y 翻转后起点在下方', () => {
    const p = worldToScreen(t, 0, 0)
    expect(p.x).toBeCloseTo(t.offsetX + CUSHION_WIDTH * t.scale, 9)
    expect(p.y).toBeCloseTo(t.offsetY + (TABLE_LENGTH + CUSHION_WIDTH) * t.scale, 9)
  })

  it('台面两个对角在屏幕上的跨度等于台面尺寸乘缩放', () => {
    const a = worldToScreen(t, 0, 0)
    const b = worldToScreen(t, TABLE_WIDTH, TABLE_LENGTH)
    expect(b.x - a.x).toBeCloseTo(TABLE_WIDTH * t.scale, 9)
    // y 轴翻转：世界 y 增大是往远端走，屏幕上反而往上。
    expect(a.y - b.y).toBeCloseTo(TABLE_LENGTH * t.scale, 9)
  })

  it('母球在屏幕下方、球堆在上方，与玩家站在台子这一头的视角一致', () => {
    const cue = worldToScreen(t, 0.5944, 0.4557)
    const rack = worldToScreen(t, 0.4953, 1.585)
    expect(cue.y).toBeGreaterThan(rack.y)
    // x 不翻转：母球在 x 0.594，比球堆中心的 0.495 更靠右。
    expect(cue.x).toBeGreaterThan(rack.x)
  })

  it('往返变换回到原点，误差在浮点范围内', () => {
    const points = [
      { x: 0, y: 0 },
      { x: TABLE_WIDTH, y: TABLE_LENGTH },
      { x: 0.4953, y: 1.585 },
      { x: 0.028575, y: 1.95 },
    ]
    for (const p of points) {
      const s = worldToScreen(t, p.x, p.y)
      const back = screenToWorld(t, s.x, s.y)
      expect(back.x).toBeCloseTo(p.x, 9)
      expect(back.y).toBeCloseTo(p.y, 9)
    }
  })

  it('屏幕中心落在台面中心附近，说明居中没算错', () => {
    const center = screenToWorld(t, view.width / 2, view.height / 2)
    expect(center.x).toBeCloseTo(TABLE_WIDTH / 2, 6)
    expect(center.y).toBeCloseTo(TABLE_LENGTH / 2, 6)
  })
})

describe('metersToPx', () => {
  it('与坐标变换共用同一个 scale', () => {
    const t = fitTable({ width: 390, height: 844 }, 8)
    const a = worldToScreen(t, 0, 0)
    const b = worldToScreen(t, 0.1, 0)
    expect(metersToPx(t, 0.1)).toBeCloseTo(b.x - a.x, 9)
  })
})
