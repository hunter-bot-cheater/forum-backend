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

// 袋口尺寸按参考图（天大台球）实测的比例定，单位都是球直径 D = 57.15mm：
//
//   洞口直径 2.17D（124mm），角袋开口 2.08D（119mm），中袋开口 2.25D（129mm）。
//
// 这三组数是像素量出来的，不是估的：参考图里球径 24px、洞 52px、角袋开口
// 50px、中袋开口 54px。洞比角袋开口还大一点点，所以画出来是「一个整圆，
// 开口从圆上横过去」，这也正是玩家要的观感——洞不能被库边切掉一块。
export const POCKET_RADIUS = 0.062

// 角袋与中袋在库边脸上的开口宽度（两个鼻尖之间的距离），见上面的比例。
// 中袋比角袋宽是真实球桌的规律，也让中袋打起来明显更松。
export const CORNER_MOUTH = 0.1189
export const SIDE_MOUTH = 0.1286

// 角袋斜边相对库边脸偏转的角度：袋口角 142°，等价于斜边偏 38°。
// 38° 落在「收拢夹角 30°–40°」的区间里，两条斜边只差 14°，观感上既收拢
// 又不会把口子收成尖角。
export const CORNER_JAW_BEND = 38

// 中袋斜边相对库边法线的偏转角度：袋口角 104°，即斜边偏 14°。
// 比角袋平缓得多——中袋是「门框式」的两小段切角，不是角袋那种斜槽。
export const SIDE_JAW_BEND = 14

// 中袋颊板往库边里退一个库边厚时，纵向收进去多少：CUSHION_WIDTH * tan14°。
// 收口宽度 = SIDE_MOUTH - 2 * 这个值 ≈ 2.25D - 0.44D = 1.8D，
// 比角袋的收口更宽——参考图里中袋是一段平缓的切角，不是斜槽。
const SIDE_JAW_INWARD = CUSHION_WIDTH * Math.tan((SIDE_JAW_BEND * Math.PI) / 180)

// Pocket 是一个袋口。
//
// 这里的坐标与半径就是**落袋判定用的圆，也就是画出来的那个洞**，两者必须是
// 同一个圆：判定圆比显示圆大，会出现「看着还压在洞口边上，球却没了」；
// 比显示圆小，球已经掉进洞里却继续滚。所以渲染直接用这份数据，不再自己挪圆心。
//
// 与 pooltool 的差异：圆心沿朝外方向挪到库边厚度附近（角袋 17.96mm、
// 中袋 32mm）。pooltool 的中袋心在 x = -0.0685，照它画整个洞会落在木框上。
// 中袋的 32mm 是参考图的实测值（洞心离开库边脸约 0.56D）：洞因此有半个
// 圆压在台呢上，看起来才是「圆洞嵌在库边里」，而不是贴在木框上的一口井。
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
// 角袋的圆心落在对角线、离两条库边各 17.96mm；中袋的圆心离开库边脸 32mm。
// 两个值都不参与开口宽度计算（开口由 CORNER_MOUTH / SIDE_MOUTH 直接给出），
// 只决定「洞有多深地探进台呢」。
export const POCKETS: ReadonlyArray<Pocket> = [
  { id: 'lb', kind: 'corner', x: -0.01796, y: -0.01796, radius: POCKET_RADIUS },
  { id: 'lc', kind: 'side', x: -0.032, y: 0.9906, radius: POCKET_RADIUS },
  { id: 'lt', kind: 'corner', x: -0.01796, y: 1.99916, radius: POCKET_RADIUS },
  { id: 'rb', kind: 'corner', x: 1.00856, y: -0.01796, radius: POCKET_RADIUS },
  { id: 'rc', kind: 'side', x: 1.0226, y: 0.9906, radius: POCKET_RADIUS },
  { id: 'rt', kind: 'corner', x: 1.00856, y: 1.99916, radius: POCKET_RADIUS },
]

// 库边与颊板都是线段，格式 [x1, y1, x2, y2]，单位米。
export type CushionSegment = readonly [number, number, number, number]

// 一条袋口的张口：两个鼻尖、两条颊板的外端。
// 渲染按它画袋兜暗面与斜边高光，物理按同样的点生成碰撞线段——
// 画出来的斜边就是能撞到的斜边，否则会出现「看着擦着袋口弹了一下，
// 另一台机器上却没有」。
export interface PocketMouth {
  id: string
  // 鼻尖：颊板与库边脸的交点，也是直线库边的端点。
  noseA: { x: number; y: number }
  noseB: { x: number; y: number }
  // 颊板外端。角袋落在库边外沿（斜槽的入口），中袋落在台呢里
  // （梯形缺口的短底边）——两类袋口的张口方向不同，见 mouthOf。
  jawA: { x: number; y: number }
  jawB: { x: number; y: number }
}

// cornerMouth / sideMouth 分别算出角袋与中袋的张口几何。
//
// 两种袋口的斜边不是一种类型，这是标准图里最容易被忽略、但一眼能看出区别的一处：
//
//   角袋：两段斜边对称内收，各自与库边脸成 142°（斜边本身偏 38°）。两段只差
//         14°、接近平行，把洞口半围住，球沿着这条斜槽进洞。
//   中袋：两段斜边外张，形成一个梯形：长底边是库边脸上的开口、朝向台面内部，
//         短底边贴着洞口。斜边与库边脸的夹角是 104°（偏 14°），比角袋平缓得多。
function mouthOf(p: Pocket): PocketMouth {
  if (p.kind === 'side') {
    // 中袋落在一条库边的中段：整张开口在同一条库边脸上。
    // 两条颊板都朝袋心收（越往库边外沿越窄），所以纵向位移一律取反号：
    // 取同号会让颊板背对洞口斜出去，斜边根本碰不到洞，只剩一个大三角缺口。
    const half = SIDE_MOUTH / 2
    // 斜边朝洞口收 14°：越往库边外沿走，两条斜边的纵向间距越小。
    const inward = SIDE_JAW_INWARD
    if (p.x < 0 || p.x > TABLE_WIDTH) {
      const faceX = p.x < 0 ? 0 : TABLE_WIDTH
      const outX = p.x < 0 ? -CUSHION_WIDTH : CUSHION_WIDTH
      return {
        id: p.id,
        noseA: { x: faceX, y: p.y + half },
        noseB: { x: faceX, y: p.y - half },
        jawA: { x: faceX + outX, y: p.y + half - inward },
        jawB: { x: faceX + outX, y: p.y - half + inward },
      }
    }
    const faceY = p.y < 0 ? 0 : TABLE_LENGTH
    const outY = p.y < 0 ? -CUSHION_WIDTH : CUSHION_WIDTH
    return {
      id: p.id,
      noseA: { x: p.x - half, y: faceY },
      noseB: { x: p.x + half, y: faceY },
      jawA: { x: p.x - half + inward, y: faceY + outY },
      jawB: { x: p.x + half - inward, y: faceY + outY },
    }
  }

  // 角袋：两个鼻尖都在库边脸上，各自离袋角 CORNER_MOUTH/√2。
  // 这个距离是「开口宽度」倒推出来的：开口线在库边脸上截出多长，鼻尖就
  // 偏移 L/√2 各就各位，量出来正好是 L。不要再写成「袋心 ± 多少倍半径」
  // ——袋心在台外，那样算出来的鼻尖会跟着圆心漂移。
  //
  // 斜边方向：与库边脸成 142°，也就是朝洞口那边偏 38°（CORNER_JAW_BEND）。
  // 两段因此只差 14°、接近平行，各自朝洞心收过去，在洞口外侧交汇，
  // 交汇点被圆洞盖住——看起来就是「两段斜边把洞口半围住」。
  // 取 45° 时两段会重合，袋口变成一刀平切，没有「收拢」的意思；
  // 取 30° 又太竖，斜边在台呢上拐得太急，像缺了一角。
  const nose = CORNER_MOUTH / Math.SQRT2
  const bend = (CORNER_JAW_BEND * Math.PI) / 180
  const cosB = Math.cos(bend)
  const sinB = Math.sin(bend)
  const depth = CUSHION_WIDTH / sinB
  const inX = p.x < 0 ? 1 : -1
  const inY = p.y < 0 ? 1 : -1
  const faceX = p.x < 0 ? 0 : TABLE_WIDTH
  const faceY = p.y < 0 ? 0 : TABLE_LENGTH
  return {
    id: p.id,
    noseA: { x: faceX + inX * nose, y: faceY },
    noseB: { x: faceX, y: faceY + inY * nose },
    jawA: { x: faceX + inX * (nose - depth * cosB), y: faceY - inY * CUSHION_WIDTH },
    jawB: { x: faceX - inX * CUSHION_WIDTH, y: faceY + inY * (nose - depth * cosB) },
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

// CUSHION_JAWS 是十二条颊板：角袋的退到库边外沿（45° 斜槽的两壁），
// 中袋的切进台呢（梯形缺口的两条斜边）。
export const CUSHION_JAWS: ReadonlyArray<CushionSegment> = POCKET_MOUTHS.flatMap((m) => [
  [m.noseA.x, m.noseA.y, m.jawA.x, m.jawA.y] as CushionSegment,
  [m.noseB.x, m.noseB.y, m.jawB.x, m.jawB.y] as CushionSegment,
])

// CUSHION_BACKS 是六个袋兜的底：把两条颊板的外端连起来。
//
// 没有它，球从袋口斜着冲进去就会从两条颊板之间穿出去，落在木框上继续滚
// ——看起来像球飞出了台子。这段挡板整个落在洞口圆里：正常进袋的球
// 在碰到它之前就已经被判落袋（判定圆比它大一圈），所以它只兜住
// 「擦着袋口没进、在袋喉里打转」的那一类。
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
