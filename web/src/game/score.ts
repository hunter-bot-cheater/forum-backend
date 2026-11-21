// 台面上的「还剩哪些球」，用于双方条上的小球点。
//
// 分组规则与后端 internal/pool/state.go 的 ballGroup / groupBalls 同一份：
// 1~7 全色（solids），9~15 花色（stripes），8 号是黑球归谁都不算。
// 两边各写一份是这类项目最容易分叉的地方——后端改了分组，前端还按旧表画，
// 表现为「他说我清完了，界面上还剩一颗」。

import type { BallGroup, MatchSnapshot, Seat } from './types'

// SOLIDS / STRIPES 是两组球号。
// 用字符串而不是数字：服务端下发与上报的 id 全是字符串（'cue' / '8'），
// 中间转一次数字再转回去只会多一处出错的机会。
export const SOLIDS: ReadonlyArray<string> = ['1', '2', '3', '4', '5', '6', '7']
export const STRIPES: ReadonlyArray<string> = ['9', '10', '11', '12', '13', '14', '15']

// EIGHT_BALL 是黑八的球号，单独列出来免得各处散落一个 '8'。
export const EIGHT_BALL = '8'

// ballsOfGroup 返回某个分组的全部球号。'none' 返回空数组。
export function ballsOfGroup(group: BallGroup): ReadonlyArray<string> {
  if (group === 'solids') {
    return SOLIDS
  }
  if (group === 'stripes') {
    return STRIPES
  }
  return []
}

// onTableIds 收集当前还在台面上的球号。
export function onTableIds(snapshot: MatchSnapshot): Set<string> {
  const ids = new Set<string>()
  for (const ball of snapshot.balls) {
    if (ball.on_table) {
      ids.add(ball.id)
    }
  }
  return ids
}

// remainingOf 返回某个座位「自己组里还在台面上」的球号，按号码升序。
//
// 未定组时返回空数组：这时两边都没有「我的球」，
// 画一排无法归属的球点只会让人以为分组已经定了。
export function remainingOf(snapshot: MatchSnapshot, seat: Seat): string[] {
  const group = snapshot.group[seat]
  if (group === 'none') {
    return []
  }
  const alive = onTableIds(snapshot)
  const list: string[] = []
  for (const id of ballsOfGroup(group)) {
    if (alive.has(id)) {
      list.push(id)
    }
  }
  return list
}

// groupLabel 是分组的中文名，双方条与横幅共用。
export function groupLabel(group: BallGroup): string {
  if (group === 'solids') {
    return '全色'
  }
  if (group === 'stripes') {
    return '花色'
  }
  return '未定组'
}

// isSolid 判断一个球号是不是全色球，用于给球点选颜色。
// 黑八与母球都返回 false，由调用方单独处理。
export function isSolid(id: string): boolean {
  return SOLIDS.includes(id)
}
