import { describe, expect, it, vi } from 'vitest'
import { drawScene } from './renderer'
import { RACK_EIGHTBALL } from './table'
import type { BallState } from './types'

// ballTexture 会创建 canvas 并取 2d 上下文，jsdom 没有实现这一层。
// 这里的用例只关心「画了几颗球」，贴图换成一个空对象就够。
vi.mock('./balls', () => ({
  ballTexture: () => ({}) as unknown as HTMLCanvasElement,
}))

// createFakeCtx 记录每种绘制方法被调用了几次。
//
// 用 Proxy 而不是逐条列方法：canvas 的 API 面很宽，漏一个就会在运行期
// 报「not a function」，而这里想测的是业务分支（几颗球、是否跳过落袋球），
// 不是 API 覆盖。createRadialGradient 要单独返回带 addColorStop 的对象。
type FakeCtx = CanvasRenderingContext2D & { calls: Map<string, number> }

function createFakeCtx(): FakeCtx {
  const calls = new Map<string, number>()
  const target: Record<string, unknown> = {}

  const proxy = new Proxy(target, {
    get(_target, prop) {
      if (prop === 'calls') {
        return calls
      }
      if (prop === 'createRadialGradient') {
        return () => ({ addColorStop() {} })
      }
      return () => {
        const name = String(prop)
        calls.set(name, (calls.get(name) ?? 0) + 1)
      }
    },
    set() {
      return true
    },
  })

  return proxy as unknown as FakeCtx
}

// rackState 把开球摆球转成球状态，可以指定某几颗已经落袋。
function rackState(dropped: string[] = []): BallState[] {
  const list: BallState[] = []
  for (const ball of RACK_EIGHTBALL) {
    list.push({
      id: ball.id,
      x: ball.x,
      y: ball.y,
      z: 0,
      on_table: !dropped.includes(ball.id),
    })
  }
  return list
}

describe('drawScene', () => {
  it('16 颗球各画一次，清屏一次', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState() }, 2)
    expect(ctx.calls.get('clearRect')).toBe(1)
    expect(ctx.calls.get('drawImage')).toBe(16)
  })

  it('落袋的球不画', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(['1', '9']) }, 2)
    expect(ctx.calls.get('drawImage')).toBe(14)
  })

  it('空球局只画台面，不抛异常', () => {
    const ctx = createFakeCtx()
    expect(() => drawScene(ctx, { width: 390, height: 844 }, { balls: [] }, 1)).not.toThrow()
    expect(ctx.calls.get('drawImage')).toBeUndefined()
  })

  it('容器尺寸为 0 时不抛异常也不产生 NaN 坐标', () => {
    const ctx = createFakeCtx()
    expect(() => drawScene(ctx, { width: 0, height: 0 }, { balls: rackState() }, 1)).not.toThrow()
  })

  it('桌面尺寸下同样画满 16 颗球', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 1440, height: 900 }, { balls: rackState() }, 1)
    expect(ctx.calls.get('drawImage')).toBe(16)
  })

  it('跳球时球心位移但不改变绘制次数', () => {
    const ctx = createFakeCtx()
    const balls = rackState()
    balls[1].z = 0.12
    drawScene(ctx, { width: 390, height: 844 }, { balls }, 2)
    expect(ctx.calls.get('drawImage')).toBe(16)
  })
})
