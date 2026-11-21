import { describe, expect, it } from 'vitest'
import { ballsOfGroup, groupLabel, hasGroup, onTableIds, remainingOf } from '@/game/score'
import type { BallState, MatchSnapshot } from '@/game/types'

// snapshot 造一份最小快照。只填 score.ts 会读到的字段，
// 其余用类型断言跳过：这些用例关心的是「哪些球还在台面上」，
// 而不是快照的形状（那是 protocol 的契约，由后端保证）。
function snapshot(balls: BallState[], group: [string, string]): MatchSnapshot {
  return {
    match_id: 1,
    room_code: '123456',
    status: 'playing',
    seat: 0,
    turn: 0,
    deadline_ms: 0,
    balls,
    group,
    ready: [true, true],
    ball_in_hand: false,
    shot_seq: 1,
    players: [
      { user_id: 1, nickname: '甲', avatar: '' },
      { user_id: 2, nickname: '乙', avatar: '' },
    ],
    winner: null,
    win_reason: '',
  } as unknown as MatchSnapshot
}

// ball 造一颗球，位置随意：score.ts 只看 id 与 on_table。
function ball(id: string, onTable = true): BallState {
  return { id, x: 0, y: 0, z: 0, on_table: onTable }
}

describe('ballsOfGroup', () => {
  it('全色是 1 到 7', () => {
    expect(ballsOfGroup('solids')).toEqual(['1', '2', '3', '4', '5', '6', '7'])
  })

  it('花色是 9 到 15', () => {
    expect(ballsOfGroup('stripes')).toEqual(['9', '10', '11', '12', '13', '14', '15'])
  })

  it('未定组时没有任何「我的球」', () => {
    expect(ballsOfGroup('')).toEqual([])
  })
})

describe('onTableIds', () => {
  it('落袋的球不计入', () => {
    const ids = onTableIds(snapshot([ball('cue'), ball('1', false), ball('2')], ['solids', 'stripes']))
    expect(ids.has('1')).toBe(false)
    expect(ids.has('2')).toBe(true)
    expect(ids.has('cue')).toBe(true)
  })
})

describe('remainingOf', () => {
  it('只算自己组里还在台面上的球', () => {
    const balls = [ball('cue'), ball('1'), ball('2', false), ball('3'), ball('9'), ball('10', false)]
    const snap = snapshot(balls, ['solids', 'stripes'])
    // 自己是全色座位：1、3 还在，2 已落袋；9/10 是对方的球，不该出现在这里。
    expect(remainingOf(snap, 0)).toEqual(['1', '3'])
    expect(remainingOf(snap, 1)).toEqual(['9'])
  })

  // 未定组在协议里是空串（后端用 Go 零值当哨兵），不是 'none'。
  // 这条用例就是钉住这一点：写成 'none' 的话这里会红。
  it('未定组时返回空，不把台面上所有的球都算成自己的', () => {
    const snap = snapshot([ball('cue'), ball('1'), ball('9')], ['', ''])
    expect(remainingOf(snap, 0)).toEqual([])
  })

  it('清完自己组时返回空数组（页面据此提示打黑八）', () => {
    const snap = snapshot([ball('cue'), ball('1', false)], ['solids', 'stripes'])
    expect(remainingOf(snap, 0)).toEqual([])
  })

  it('结果按号码升序，不受台面上球的顺序影响', () => {
    const snap = snapshot([ball('7'), ball('cue'), ball('1'), ball('3')], ['solids', 'stripes'])
    expect(remainingOf(snap, 0)).toEqual(['1', '3', '7'])
  })
})

describe('hasGroup', () => {
  it('空串表示还没定组，两个非空值都算已定组', () => {
    expect(hasGroup('')).toBe(false)
    expect(hasGroup('solids')).toBe(true)
    expect(hasGroup('stripes')).toBe(true)
  })
})

describe('groupLabel', () => {
  it('三个组别各有一句中文', () => {
    expect(groupLabel('solids')).toBe('全色')
    expect(groupLabel('stripes')).toBe('花色')
    expect(groupLabel('')).toBe('未定组')
  })
})
