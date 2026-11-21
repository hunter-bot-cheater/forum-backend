// 赢法机读码 → 一句中文。
//
// 文案只在前端定：服务端给的是稳定标识（eight_legal / timeout / leave…），
// 改一句中文不该牵动后端发版，样式分支也不该依赖中文串。
//
// **码是从赢家视角记的**：「leave」表示有人中途离开、于是对手获胜，
// 至于离开的是哪一方，要结合查看着自己赢了还是输了才能说清楚。
// 所以这组函数都带一个 won 参数——少了它，输家那一行会写成「对手中途离开」，
// 而实际上离开的是他自己。
//
// 结算面板与战绩列表共用这一份：两处一旦分叉，同一次胜利会显示成两个说法。

// reasonLabel 是结算面板用的完整说法。
export function reasonLabel(code: string, won: boolean): string {
  if (code === 'eight_legal') {
    // 赢家说「赢下这一局」，输家也得点明是谁打的：
    // 中文没有主语时默认指说话人，写成「打进黑八，赢下这一局」，
    // 输家读到的是「我打进黑八赢了」，与上面那行「这局输了」直接打架。
    return won ? '打进黑八，赢下这一局' : '对手打进黑八，赢下这一局'
  }
  if (code === 'eight_early') {
    return won ? '对手提前打进黑八' : '你提前打进黑八'
  }
  if (code === 'eight_with_foul') {
    return won ? '对手犯规同时打进黑八' : '你犯规同时打进黑八'
  }
  if (code === 'timeout') {
    return won ? '对手连续两次超时，判负' : '你连续两次超时，判负'
  }
  if (code === 'leave') {
    return won ? '对手中途离开，判负' : '你中途离开，判负'
  }
  // 未登记的原因不猜，直接说「本局结束」：编一句可能错的原因比不说更糟。
  return '本局结束'
}

// reasonShort 是列表里用的短说法。
// 战绩列表每行已经有胜负标记，再写一遍「赢下这一局」是重复。
export function reasonShort(code: string, won: boolean): string {
  if (code === 'eight_legal') {
    return '打进黑八'
  }
  if (code === 'eight_early') {
    return won ? '对手提前打进黑八' : '黑八提前落袋'
  }
  if (code === 'eight_with_foul') {
    return '犯规打进黑八'
  }
  if (code === 'timeout') {
    return won ? '对手连续超时' : '连续超时'
  }
  if (code === 'leave') {
    return won ? '对手中途离开' : '中途离开'
  }
  return '已结束'
}

// reasonNeutral 是回放页用的说法：只陈述这一局怎么结束的。
//
// 回放页没有「我」这一侧（没有结算面板、没有胜负标记），
// 硬套 reasonLabel 要么缺 won 参数、要么得凭空假定一个视角，
// 于是单独给一组不带主语的说法：「中途离开」而不是「你/对手中途离开」。
// 未登记的原因同样不猜。
const NEUTRAL: Record<string, string> = {
  eight_legal: '打进黑八',
  eight_early: '提前打进黑八',
  eight_with_foul: '犯规同时打进黑八',
  timeout: '连续两次超时',
  leave: '中途离开',
}

// reasonNeutral 把赢法码翻成不带视角的一句中文。
export function reasonNeutral(code: string): string {
  const text = NEUTRAL[code]
  if (text === undefined) {
    return '已结束'
  }
  return text
}
