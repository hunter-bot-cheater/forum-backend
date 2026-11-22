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

// 开球线：离开球端库边的距离，台长的四分之一（495.3mm）。
//
// 与后端 internal/pool/match.go 的 cueHeadString 是同一个数：服务端按它拒掉
// 越线落位，前端按它夹虚影，两边差一毫米就会出现「看着在线内、服务端说越界」。
export const CUE_HEAD_STRING = TABLE_LENGTH / 4

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

// Pocket 是一个袋口。
//
// 这里的坐标与半径就是**落袋判定用的圆，也就是画出来的那个洞**，两者必须是
// 同一个圆：判定圆比显示圆大，会出现「看着还压在洞口边上，球却没了」；
// 比显示圆小，球已经掉进洞里却继续滚。所以渲染直接用这份数据，不再自己挪圆心。
//
// 与 pooltool 的两处刻意差异：
//   - 圆心沿朝外方向挪到库边厚度中线上。pooltool 的袋心在库边之外
//     （中袋 x = -0.0685），那是它围栏网格的定义，照它画整个洞会落在木框上；
//   - 半径比 pooltool 的袋心半径小 20%。pooltool 的 0.062 m 是「球心进到多深
//     算落袋」的判据，直径 124mm 是球径的 2.17 倍，画出来是一口井，
//     真实球桌的角袋开口在球径 1.7 倍上下。
export interface Pocket {
  id: string
  kind: PocketKind
  x: number
  y: number
  radius: number
}

// POCKETS 是六个袋口，顺序为左下、左中、左上、右下、右中、右上。
// 名字沿用 pooltool 的 lb / lc / lt 记号（left bottom / center / top）。
//
// 圆心到台面内沿的距离一律是半个库边厚（25.4mm），这是「洞心落在库边厚度
// 中线上」的直接结果，也是渲染算缺口时的基准。角袋的圆心因此落在对角线
// 上、离两条库边各 17.96mm。
export const POCKETS: ReadonlyArray<Pocket> = [
  { id: 'lb', kind: 'corner', x: -0.01796, y: -0.01796, radius: 0.0496 },
  { id: 'lc', kind: 'side', x: -0.0254, y: 0.9906, radius: 0.0516 },
  { id: 'lt', kind: 'corner', x: -0.01796, y: 1.99916, radius: 0.0496 },
  { id: 'rb', kind: 'corner', x: 1.00856, y: -0.01796, radius: 0.0496 },
  { id: 'rc', kind: 'side', x: 1.016, y: 0.9906, radius: 0.0516 },
  { id: 'rt', kind: 'corner', x: 1.00856, y: 1.99916, radius: 0.0496 },
]

// 袋心到台面内沿的距离。渲染算缺口、物理算颊板起点都用它，
// 不要各处自己写 0.0254：两者差几毫米，斜边就和洞错开了。
export const POCKET_INSET = CUSHION_WIDTH / 2

// 颊板鼻尖离开袋心沿库边的距离，以洞口半径为 1。
//
// 1.32 让袋口在库边脸上的开口（两侧各 1.32r，共约 136mm）明显宽于洞径
// （约 103mm）：洞整个落在开口里，看上去是「台呢被啃掉一个 V，洞在 V 里」。
// 再小一点洞会比开口还宽，洞沿会压到库边脸上，像贴在布上的圆片。
export const POCKET_JAW_HALF = 1.32

// 库边与颊板都是线段，格式 [x1, y1, x2, y2]，单位米。
export type CushionSegment = readonly [number, number, number, number]

// 一条袋口的张口：两个鼻尖、两条 45° 颊板的外端。
// 渲染按它画 V 形暗面与斜边高光，物理按同样的点生成碰撞线段——
// 画出来的斜边就是能撞到的斜边，否则会出现「看着擦着袋口弹了一下，
// 另一台机器上却没有」。
export interface PocketMouth {
  id: string
  // 鼻尖：颊板与库边脸的交点，也是直线库边的端点。
  noseA: { x: number; y: number }
  noseB: { x: number; y: number }
  // 颊板外端：落在库边外沿上。两条颊板在这里收口，形成袋兜的底。
  jawA: { x: number; y: number }
  jawB: { x: number; y: number }
}

// cornerMouth / sideMouth 分别算出角袋与中袋的张口几何。
//
// 角袋的两条颊板都朝袋角那条对角线走（左下角就是 -x-y 方向），
// 中袋的两条颊板各自朝远离袋心的方向走；两者都从「库边脸上的鼻尖」
// 斜 45° 退到库边外沿，斜边与库边脸的夹角正好 135°。
function mouthOf(p: Pocket): PocketMouth {
  const half = p.radius * POCKET_JAW_HALF
  if (p.kind === 'side') {
    // 中袋落在一条库边的中段：整张开口在同一条库边脸上。
    // 两条颊板都朝袋心收（越往库边外沿越窄），所以纵向位移一律取反号：
    // 取同号会让颊板背对洞口斜出去，斜边根本碰不到洞，只剩一个大三角缺口。
    if (p.x < 0 || p.x > TABLE_WIDTH) {
      const faceX = p.x < 0 ? 0 : TABLE_WIDTH
      const railX = p.x < 0 ? -CUSHION_WIDTH : TABLE_WIDTH + CUSHION_WIDTH
      return {
        id: p.id,
        noseA: { x: faceX, y: p.y + half },
        noseB: { x: faceX, y: p.y - half },
        jawA: { x: railX, y: p.y + half - CUSHION_WIDTH },
        jawB: { x: railX, y: p.y - half + CUSHION_WIDTH },
      }
    }
    const faceY = p.y < 0 ? 0 : TABLE_LENGTH
    const railY = p.y < 0 ? -CUSHION_WIDTH : TABLE_LENGTH + CUSHION_WIDTH
    return {
      id: p.id,
      noseA: { x: p.x - half, y: faceY },
      noseB: { x: p.x + half, y: faceY },
      jawA: { x: p.x - half + CUSHION_WIDTH, y: railY },
      jawB: { x: p.x + half - CUSHION_WIDTH, y: railY },
    }
  }

  // 角袋：横库上一条颊板、纵库上一条颊板，都朝袋角那条对角线退出去。
  // 鼻尖从「袋心投影到库边脸的那个点」往台面内侧让 half：袋心本身在库边
  // 外面十几毫米，直接按它算的话鼻尖会跑到台子外面去。
  const sx = p.x < 0 ? -1 : 1
  const sy = p.y < 0 ? -1 : 1
  const faceX = p.x < 0 ? 0 : TABLE_WIDTH
  const faceY = p.y < 0 ? 0 : TABLE_LENGTH
  return {
    id: p.id,
    noseA: { x: p.x - sx * half, y: faceY },
    noseB: { x: faceX, y: p.y - sy * half },
    jawA: { x: p.x - sx * half + sx * CUSHION_WIDTH, y: faceY + sy * CUSHION_WIDTH },
    jawB: { x: faceX + sx * CUSHION_WIDTH, y: p.y - sy * half + sy * CUSHION_WIDTH },
  }
}

// POCKET_MOUTHS 是六个袋口的张口，顺序与 POCKETS 一致。
export const POCKET_MOUTHS: ReadonlyArray<PocketMouth> = POCKETS.map(mouthOf)

// CUSHION_RUNS 是六条笔直的库边：每条都从一侧袋口的鼻尖跑到另一侧袋口的鼻尖。
// 端点直接取 POCKET_MOUTHS，改袋口尺寸时直线段自动跟着让位。
export const CUSHION_RUNS: ReadonlyArray<CushionSegment> = [
  // 左库（x = 0）分两段，中间给中袋让开口。
  [0, POCKET_MOUTHS[0].noseB.y, 0, POCKET_MOUTHS[1].noseB.y],
  [0, POCKET_MOUTHS[1].noseA.y, 0, POCKET_MOUTHS[2].noseB.y],
  // 右库（x = 台宽）同理。
  [TABLE_WIDTH, POCKET_MOUTHS[3].noseB.y, TABLE_WIDTH, POCKET_MOUTHS[4].noseB.y],
  [TABLE_WIDTH, POCKET_MOUTHS[4].noseA.y, TABLE_WIDTH, POCKET_MOUTHS[5].noseB.y],
  // 下库与上库（y = 0 / y = 台长）：两端各接一个角袋。
  [POCKET_MOUTHS[0].noseA.x, 0, POCKET_MOUTHS[3].noseA.x, 0],
  [POCKET_MOUTHS[2].noseA.x, TABLE_LENGTH, POCKET_MOUTHS[5].noseA.x, TABLE_LENGTH],
]

// CUSHION_JAWS 是十二条 45° 颊板，每条从鼻尖退到库边外沿。
export const CUSHION_JAWS: ReadonlyArray<CushionSegment> = POCKET_MOUTHS.flatMap((m) => [
  [m.noseA.x, m.noseA.y, m.jawA.x, m.jawA.y] as CushionSegment,
  [m.noseB.x, m.noseB.y, m.jawB.x, m.jawB.y] as CushionSegment,
])

// CUSHION_BACKS 是六个袋兜的底：把两条颊板的外端连起来。
//
// 没有它，球从袋口斜着冲进去就会从两条颊板之间穿出去，落在木框上继续滚
// ——看起来像球飞出了台子。这段挡板整个落在洞口圆里，正常进袋的球
// 在碰到它之前就已经被判落袋，所以它只兜住「擦着袋口没进」的那一类。
export const CUSHION_BACKS: ReadonlyArray<CushionSegment> = POCKET_MOUTHS.map((m) => [
  m.jawA.x,
  m.jawA.y,
  m.jawB.x,
  m.jawB.y,
])

// CUSHION_SEGMENTS 是物理用的全部库边线段：直线段 + 颊板 + 袋兜底。
export const CUSHION_SEGMENTS: ReadonlyArray<CushionSegment> = [
  ...CUSHION_RUNS,
  ...CUSHION_JAWS,
  ...CUSHION_BACKS,
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
