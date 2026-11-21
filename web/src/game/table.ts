// 七尺台（7 英尺）的几何常量，单位一律米。
//
// 数据从 pooltool 0.6.0 的 Table.default() 导出，不是估的：袋口中心与库边折线
// 都带 6 位小数，与物理侧用的是同一份模型。台面尺寸一旦和物理侧对不上，
// 「球心到袋口的距离」这类判断两边就会分歧，表现为「我看着进了它说没进」。
//
// 坐标系与 pooltool 一致：原点在开球端左侧的库边内侧，x 向右、y 朝远端。
// 绘制时 worldToScreen 会把 y 翻转一次——屏幕的 y 朝下，不翻转就会看到
// 球堆在下面、母球在上面，与站在开球端的视角正好相反。

// 台面内沿尺寸：x 方向宽 990.6mm，y 方向长 1981.2mm。
export const TABLE_WIDTH = 0.9906
export const TABLE_LENGTH = 1.9812

// 库边厚度。pooltool 的库边折线分布在台面外侧 50.8mm 处。
export const CUSHION_WIDTH = 0.0508

// 木质外框在库边之外再向外的厚度，没有物理含义，只是外观。
//
// 它必须参与缩放计算。几何若只按「台面 + 两侧库边」算，木框就会画到画布外，
// 圆角与投影各被裁掉一半，看起来像球台被切了一刀。
export const RAIL_WIDTH = 0.055

// 球半径 28.575mm，即直径 57.15mm，与中式八球用球一致。
export const BALL_RADIUS = 0.028575

// PocketKind 只分两类：角袋与中袋。渲染时两者都画成圆，区分开是为了
// 尺寸与后续的碰撞判定：中袋开口略大，且入口方向只有垂直于库边一种。
export type PocketKind = 'corner' | 'side'

// Pocket 是一个袋口。center 有部分落在台面之外，这是 pooltool 的原始定义：
// 圆心在台面外一点，让袋口的开口更贴近真实台呢的切法。
export interface Pocket {
  id: string
  kind: PocketKind
  x: number
  y: number
  radius: number
}

// POCKETS 是六个袋口，顺序为左下、左中、左上、右下、右中、右上。
// 名字沿用 pooltool 的 lb / lc / lt 记号（left bottom / center / top）。
export const POCKETS: ReadonlyArray<Pocket> = [
  { id: 'lb', kind: 'corner', x: -0.029486, y: -0.029486, radius: 0.062 },
  { id: 'lc', kind: 'side', x: -0.0685, y: 0.9906, radius: 0.0645 },
  { id: 'lt', kind: 'corner', x: -0.029486, y: 2.010686, radius: 0.062 },
  { id: 'rb', kind: 'corner', x: 1.020086, y: -0.029486, radius: 0.062 },
  { id: 'rc', kind: 'side', x: 1.0591, y: 0.9906, radius: 0.0645 },
  { id: 'rt', kind: 'corner', x: 1.020086, y: 2.010686, radius: 0.062 },
]

// RackBall 是开局摆球里的一颗球。id 用 'cue' 表示母球，其余是球号字符串。
export interface RackBall {
  id: string
  x: number
  y: number
}

// RACK_EIGHTBALL 是八球开局的 16 颗球位置，坐标取自 pooltool 0.6.0
// get_rack(GameType.EIGHTBALL) 的实测值。
//
// 球号是按八球规则自己排的：pooltool 每次调用都重新洗牌，同一个坐标上
// 这次是 7 号、下次可能是 1 号，所以「逐颗对齐」只可能指坐标，不可能指编号。
// 这里把 8 号放在三角中心、顶点球放在置球点上，符合八球摆球规则。
//
// 它不是假数据，而是这一局开球前的真实初始状态。联调后端之前先用它渲染，
// 能把台面尺寸与球半径的问题提前暴露出来。
export const RACK_EIGHTBALL: ReadonlyArray<RackBall> = [
  { id: 'cue', x: 0.594371, y: 0.455658 },
  { id: '4', x: 0.495301, y: 1.485928 },
  { id: '12', x: 0.466705, y: 1.535447 },
  { id: '5', x: 0.523904, y: 1.535439 },
  { id: '9', x: 0.552501, y: 1.584991 },
  { id: '8', x: 0.495308, y: 1.585009 },
  { id: '7', x: 0.438092, y: 1.584971 },
  { id: '15', x: 0.409471, y: 1.634507 },
  { id: '1', x: 0.466716, y: 1.634516 },
  { id: '13', x: 0.523894, y: 1.634524 },
  { id: '3', x: 0.581132, y: 1.634539 },
  { id: '10', x: 0.609732, y: 1.684068 },
  { id: '2', x: 0.552511, y: 1.684074 },
  { id: '11', x: 0.495296, y: 1.684069 },
  { id: '14', x: 0.438113, y: 1.684064 },
  { id: '6', x: 0.380883, y: 1.68409 },
]

// ballCountOfGroup 是某一组还剩几颗，用于顶部席位卡的剩余球小圆点。
// ids 传当前还在台面上的球号，未定组（group 为 undefined）时返回空数组：
// 没定组之前两侧都不该显示「我的球」，那会让人误以为自己已经拿到那一组。
export function remainingBallsOfGroup(
  ids: ReadonlyArray<string>,
  group: 'solids' | 'stripes' | undefined,
): string[] {
  if (!group) {
    return []
  }
  const result: string[] = []
  for (const id of ids) {
    const n = Number(id)
    if (!Number.isInteger(n) || n < 1 || n > 15 || n === 8) {
      continue
    }
    const isSolid = n < 8
    if ((group === 'solids') === isSolid) {
      result.push(id)
    }
  }
  return result
}
