import { describe, expect, it, vi } from 'vitest'
import { drawScene, type AimLayer } from './renderer'
import { RACK_EIGHTBALL } from './table'
import type { BallState } from './types'

// ballTexture 会创建 canvas 并取 2d 上下文，jsdom 没有实现这一层。
// 这里的用例只关心「画了几颗球、坐标算得对不对」，贴图换成一个空对象就够。
vi.mock('./balls', () => ({
  ballTexture: () => ({}) as unknown as HTMLCanvasElement,
}))

// 记录每次绘制调用的方法名与参数。
//
// 只数次数会漏掉「画了但坐标是 NaN」这类错误：NaN 传进 canvas API 不会报错，
// 只会静默不画。所以断言里要抽查坐标是不是有限数。
interface Call {
  method: string
  args: unknown[]
}

type FakeCtx = CanvasRenderingContext2D & { calls: Call[] }

// createFakeCtx 用 Proxy 兜住 canvas 的全部方法。
// 不用逐条列：API 面很宽，漏一个就会在运行期报「not a function」，
// 而这里测的是业务分支，不是 API 覆盖。
function createFakeCtx(): FakeCtx {
  const calls: Call[] = []
  const proxy = new Proxy(
    {},
    {
      get(_target, prop) {
        if (prop === 'calls') {
          return calls
        }
        if (prop === 'createRadialGradient') {
          return () => ({ addColorStop() {} })
        }
        return (...args: unknown[]) => {
          calls.push({ method: String(prop), args })
        }
      },
      set() {
        return true
      },
    },
  )
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

// countOf 统计某个方法被调用几次。
function countOf(ctx: FakeCtx, method: string): number {
  let n = 0
  for (const call of ctx.calls) {
    if (call.method === method) {
      n += 1
    }
  }
  return n
}

describe('drawScene：球', () => {
  it('16 颗球各画一次，清屏一次', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState() }, 2)
    expect(countOf(ctx, 'clearRect')).toBe(1)
    expect(countOf(ctx, 'drawImage')).toBe(16)
  })

  it('球贴图的绘制目标是有限数，不是 NaN', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState() }, 2)
    const draws = ctx.calls.filter((c) => c.method === 'drawImage')
    for (const draw of draws) {
      // drawImage(texture, x, y, w, h)：后四个参数都要是有限数。
      for (const value of draw.args.slice(1)) {
        expect(typeof value).toBe('number')
        expect(Number.isFinite(value as number)).toBe(true)
      }
      expect(draw.args[3]).toBeGreaterThan(0)
      expect(draw.args[4]).toBeGreaterThan(0)
    }
  })

  it('球的绘制发生在影子之后：所有 fill 都在第一个 drawImage 之前', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState() }, 2)
    const firstDraw = ctx.calls.findIndex((c) => c.method === 'drawImage')
    const lastFill = ctx.calls.map((c) => c.method).lastIndexOf('fill')
    expect(firstDraw).toBeGreaterThan(-1)
    expect(lastFill).toBeLessThan(firstDraw)
  })

  it('落袋的球不画', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(['1', '9']) }, 2)
    expect(countOf(ctx, 'drawImage')).toBe(14)
  })

  it('空球局只画台面，不抛异常', () => {
    const ctx = createFakeCtx()
    expect(() => drawScene(ctx, { width: 390, height: 844 }, { balls: [] }, 1)).not.toThrow()
    expect(countOf(ctx, 'drawImage')).toBe(0)
  })

  it('容器尺寸为 0 时不抛异常也不产生 NaN 坐标', () => {
    const ctx = createFakeCtx()
    expect(() => drawScene(ctx, { width: 0, height: 0 }, { balls: rackState() }, 1)).not.toThrow()
    for (const draw of ctx.calls.filter((c) => c.method === 'drawImage')) {
      for (const value of draw.args.slice(1)) {
        expect(Number.isFinite(value as number)).toBe(true)
      }
    }
  })

  it('桌面尺寸下同样画满 16 颗球', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 1440, height: 900 }, { balls: rackState() }, 1)
    expect(countOf(ctx, 'drawImage')).toBe(16)
  })

  it('跳球时球体略微放大，但仍是 16 次绘制', () => {
    const ctx = createFakeCtx()
    const balls = rackState()
    const flying = balls[1]
    flying.z = 0.12
    drawScene(ctx, { width: 390, height: 844 }, { balls }, 2)
    expect(countOf(ctx, 'drawImage')).toBe(16)

    // 比大小而不是比具体数值：放大系数属于观感，改数值不该让测试红。
    const draws = ctx.calls.filter((c) => c.method === 'drawImage')
    const flyingIndex = balls.findIndex((b) => b.id === flying.id)
    const groundIndex = balls.findIndex((b) => b.id !== flying.id)
    expect(draws[flyingIndex].args[3] as number).toBeGreaterThan(draws[groundIndex].args[3] as number)
  })
})

describe('drawScene：台面', () => {
  it('六个袋口各画两层圆', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: [] }, 1)
    // 6 个袋口 × 2 层 + 置球点 1 个 = 13。
    expect(countOf(ctx, 'arc')).toBeGreaterThanOrEqual(13)
  })

  it('袋口绘制前先裁剪到木框，木框角上不会多出黑圆', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: [] }, 1)
    expect(countOf(ctx, 'clip')).toBe(1)
  })

  it('木框是圆角矩形，不是直角矩形', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: [] }, 1)
    // 圆角由 4 段 arcTo 组成；两条边用 rect 的话这里是 0。
    expect(countOf(ctx, 'arcTo')).toBeGreaterThanOrEqual(8)
  })
})

describe('drawScene：瞄准层', () => {
  const aim: AimLayer = {
    cue: { x: 0.5944, y: 0.4557 },
    hit: { x: 0.5944, y: 1.1, kind: 'ball' },
  }

  it('有瞄准层时画虚线与幽灵球', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(), aim }, 1)
    expect(countOf(ctx, 'setLineDash')).toBe(1)
    // 虚线一次 + 幽灵球一次。
    expect(countOf(ctx, 'stroke')).toBeGreaterThanOrEqual(2)
  })

  it('撞库时不画幽灵球，只画虚线', () => {
    const wallAim: AimLayer = { cue: { x: 0.5, y: 0.5 }, hit: { x: 0.5, y: 1.9, kind: 'cushion' } }
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(), aim: wallAim }, 1)
    const strokesWithDash = ctx.calls.filter((c) => c.method === 'setLineDash')
    expect(strokesWithDash).toHaveLength(1)
  })

  it('母球与命中点重合时不画线，也不会除零', () => {
    const degenerate: AimLayer = { cue: { x: 0.5, y: 0.5 }, hit: { x: 0.5, y: 0.5, kind: 'cushion' } }
    const ctx = createFakeCtx()
    expect(() =>
      drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(), aim: degenerate }, 1),
    ).not.toThrow()
    expect(countOf(ctx, 'setLineDash')).toBe(0)
  })

  it('没有瞄准层时一条虚线都不画', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(), aim: null }, 1)
    expect(countOf(ctx, 'setLineDash')).toBe(0)
  })
})
