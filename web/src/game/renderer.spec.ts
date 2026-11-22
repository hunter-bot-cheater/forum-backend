import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  BALL_DRAW_SCALE,
  drawScene,
  resetBallMotion,
  TABLE_PADDING,
  type AimLayer,
} from './renderer'
import { fitTable, metersToPx, worldToScreen } from './geometry'
import { BALL_RADIUS, RACK_EIGHTBALL } from './table'
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
        if (prop === 'createRadialGradient' || prop === 'createLinearGradient') {
          return (...args: unknown[]) => {
            calls.push({ method: String(prop), args })
            return { addColorStop() {} }
          }
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

// 滚动角度与落袋动画都是模块级状态（跨帧累积才有意义），
// 用例之间必须清一次，否则上一条用例的球会「继续滚进」这一条的画面里。
beforeEach(() => {
  resetBallMotion()
})

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

  it('球按物理直径的 1.2 倍画：小屏上也看得出是球，不是一颗彩点', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState() }, 1)
    const draw = ctx.calls.find((c) => c.method === 'drawImage')!
    const t = fitTable({ width: 390, height: 844 }, TABLE_PADDING)
    const physical = metersToPx(t, BALL_RADIUS) * 2
    // drawImage(texture, x, y, 宽, 高)：宽高要是物理直径放大后的值。
    expect(Number(draw.args[3])).toBeCloseTo(physical * BALL_DRAW_SCALE, 3)
    expect(BALL_DRAW_SCALE).toBeGreaterThan(1)
  })

  it('球的绘制发生在影子之后：影子都在第一颗球之前', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState() }, 2)
    const firstDraw = ctx.calls.findIndex((c) => c.method === 'drawImage')
    expect(firstDraw).toBeGreaterThan(-1)

    // 影子是 ellipse + fill，球体高光也是 ellipse，所以只看前 16 个：
    // 它们必须全部落在第一次 drawImage 之前。
    const ellipses: number[] = []
    ctx.calls.forEach((call, index) => {
      if (call.method === 'ellipse') {
        ellipses.push(index)
      }
    })
    expect(ellipses.length).toBeGreaterThanOrEqual(16)
    for (const index of ellipses.slice(0, 16)) {
      expect(index).toBeLessThan(firstDraw)
    }
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

  it('袋口的第一道裁剪是木框圆角，木框角上不会多出黑圆', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: [] }, 1)

    // 袋口内部还有两种裁剪（缺口只填库边那一圈、袋兜只填缺口里），
    // 所以不能数次数。真正要守住的是「最外那道裁剪是木框的圆角矩形」：
    // 第一次 clip 之前最近的那条路径必须是圆角（arcTo），不能是直角矩形。
    const methods = ctx.calls.map((c) => c.method)
    const firstClip = methods.indexOf('clip')
    expect(firstClip).toBeGreaterThan(-1)
    const lastBegin = methods.slice(0, firstClip).lastIndexOf('beginPath')
    expect(methods.slice(lastBegin, firstClip)).toContain('arcTo')
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
    phi: Math.PI / 2,
    power: 0,
    locked: false,
  }

  it('有瞄准层时画虚线与幽灵球', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(), aim }, 1)
    expect(countOf(ctx, 'setLineDash')).toBe(1)
    // 虚线一次 + 幽灵球一次。
    expect(countOf(ctx, 'stroke')).toBeGreaterThanOrEqual(2)
  })

  it('撞库时不画幽灵球，只画虚线', () => {
    const wallAim: AimLayer = {
      cue: { x: 0.5, y: 0.5 },
      hit: { x: 0.5, y: 1.9, kind: 'cushion' },
      phi: Math.PI / 2,
      power: 0,
      locked: false,
    }
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(), aim: wallAim }, 1)
    const strokesWithDash = ctx.calls.filter((c) => c.method === 'setLineDash')
    expect(strokesWithDash).toHaveLength(1)
  })

  it('母球与命中点重合时不画线，也不会除零', () => {
    const degenerate: AimLayer = {
      cue: { x: 0.5, y: 0.5 },
      hit: { x: 0.5, y: 0.5, kind: 'cushion' },
      phi: 0,
      power: 0,
      locked: false,
    }
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

describe('drawScene：球杆', () => {
  // translate 的参数就是杆头位置（屏幕坐标）。用它来验证「杆在母球后面」。
  function tipOf(ctx: FakeCtx): { x: number; y: number } {
    const call = ctx.calls.find((c) => c.method === 'translate')
    if (!call) {
      throw new Error('没有画杆')
    }
    return { x: Number(call.args[0]), y: Number(call.args[1]) }
  }

  const base: AimLayer = {
    cue: { x: 0.635, y: 0.635 },
    hit: { x: 0.635, y: 1.9, kind: 'cushion' },
    phi: Math.PI / 2,
    power: 0,
    locked: false,
  }

  it('瞄准时画一根杆，杆头在击球方向的反面', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(), aim: base }, 1)

    // 球往 +y（台面远端）走，屏幕上是向上，所以杆头要在母球下方。
    const t = fitTable({ width: 390, height: 844 }, TABLE_PADDING)
    const center = worldToScreen(t, base.cue.x, base.cue.y)
    expect(tipOf(ctx).x).toBeCloseTo(center.x, 3)
    expect(tipOf(ctx).y).toBeGreaterThan(center.y)
  })

  it('力度越大杆子拉得越远', () => {
    const soft = createFakeCtx()
    drawScene(soft, { width: 390, height: 844 }, { balls: rackState(), aim: base }, 1)

    const hard = createFakeCtx()
    drawScene(
      hard,
      { width: 390, height: 844 },
      { balls: rackState(), aim: { ...base, power: 1, locked: true } },
      1,
    )

    expect(tipOf(hard).y).toBeGreaterThan(tipOf(soft).y)
  })

  it('杆头与先角各画一次，杆身是渐变的而不是纯色', () => {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(), aim: base }, 1)
    // 袋口的喇叭口也用线性渐变，所以这里不能断言恰好 1 个。
    expect(countOf(ctx, 'createLinearGradient')).toBeGreaterThanOrEqual(1)
    expect(countOf(ctx, 'fillRect')).toBeGreaterThanOrEqual(2)
  })
})

describe('drawScene：手中球虚影', () => {
  // 袋口也是用 arc 画的圆，所以不能直接数 arc 的总数。虚影是整帧最后画的一笔，
  // 拿「有虚影」与「没虚影」两次绘制相减，差出来的就是它。
  function callsWith(ghost: { x: number; y: number } | null): Call[] {
    const ctx = createFakeCtx()
    drawScene(ctx, { width: 390, height: 844 }, { balls: rackState(), ghost }, 1)
    return ctx.calls
  }

  // countIn 数某一类调用在给定调用序列里出现了几次。
  function countIn(calls: Call[], method: string): number {
    let n = 0
    for (const call of calls) {
      if (call.method === method) {
        n += 1
      }
    }
    return n
  }

  const spot = { x: 0.635, y: 0.9 }

  it('比没有虚影时多画两个圆：球身与外围虚线', () => {
    const plain = callsWith(null)
    const ghost = callsWith(spot)

    expect(countIn(ghost, 'arc')).toBe(countIn(plain, 'arc') + 2)

    // 球身是一个球径，虚线圈是它的 1.55 倍。
    const arcs = ghost.filter((c) => c.method === 'arc')
    const body = arcs[arcs.length - 2]
    const ring = arcs[arcs.length - 1]
    expect(Number(ring.args[2])).toBeCloseTo(Number(body.args[2]) * 1.55, 3)
  })

  it('虚影落在球心换算出来的屏幕位置上', () => {
    const t = fitTable({ width: 390, height: 844 }, TABLE_PADDING)
    const center = worldToScreen(t, spot.x, spot.y)
    const arcs = callsWith(spot).filter((c) => c.method === 'arc')
    const body = arcs[arcs.length - 2]
    expect(Number(body.args[0])).toBeCloseTo(center.x, 3)
    expect(Number(body.args[1])).toBeCloseTo(center.y, 3)
  })

  it('球身用径向渐变画，虚线圈画完把 dash 复位', () => {
    const plain = callsWith(null)
    const ghost = callsWith(spot)
    // 半透明感靠径向渐变：中间亮、边缘淡，和真球是同一个光源方向。
    expect(countIn(ghost, 'createRadialGradient')).toBe(countIn(plain, 'createRadialGradient') + 1)
    // 一次设 dash、一次复位。少了复位，之后所有描边都会变成虚线。
    expect(countIn(ghost, 'setLineDash')).toBe(2)
  })
})
