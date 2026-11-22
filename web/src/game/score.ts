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

// GROUP_NONE 是「还没定组」。协议里就是空串（后端用 Go 零值当哨兵），
// 给个名字是为了让下面几处判断读起来是「有没有组」而不是「是不是空串」。
export const GROUP_NONE: BallGroup = ''

// hasGroup 判断这一侧是否已经定组。
// 页面用这个而不是 `group === ''`：以后哨兵若改成别的值，只改这里。
export function hasGroup(group: BallGroup): boolean {
  return group !== GROUP_NONE
}

// ballsOfGroup 返回某个分组的全部球号。未定组返回空数组。
export function ballsOfGroup(group: BallGroup): ReadonlyArray<string> {
  if (group === 'solids') {
    return SOLIDS
  }
  if (group === 'stripes') {
    return STRIPES
  }
  return []
}

// groupOfBall 返回一颗球属于哪一组。母球与 8 号不属于任何组。
//
// 与后端 state.go 的 ballGroup 同一份规则，只在这里写一次：
// 判「这颗球是不是我的」时，两边各写一份必然有一处维护漏掉。
export function groupOfBall(id: string): BallGroup {
  if (SOLIDS.includes(id)) {
    return 'solids'
  }
  if (STRIPES.includes(id)) {
    return 'stripes'
  }
  return GROUP_NONE
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
  if (!hasGroup(group)) {
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

// aimForbidden 判断瞄准线上最先碰到的那颗球「该不该由我打」。
//
// 只回答「这一杆会先碰到对手的球」这一种情况，用来在台面上打一个禁止符号：
//   - 还没定组（开球那几杆）：谁都能碰，不算违规；
//   - 8 号：自己组的球清完之前碰到它也是犯规，清完之后才是唯一目标；
//   - 其余：组别与我的不同就算违规。
// 先碰到库边（targetId 为 null）不算——那是空杆，另外判。
//
// 这里不拦出杆：规则判定在服务端，客户端只负责在打之前提醒一次。
export function aimForbidden(
  snapshot: MatchSnapshot,
  seat: Seat,
  targetId: string | null,
): boolean {
  if (!targetId) {
    return false
  }
  const group = snapshot.group[seat]
  if (!hasGroup(group)) {
    return false
  }
  if (targetId === '8') {
    return remainingOf(snapshot, seat).length > 0
  }
  return groupOfBall(targetId) !== group
}
