// 2D 台球的共享类型，与后端 internal/pool/dto.go 的 JSON 字段一一对应。
//
// 前端不在这里做校验或补默认值：服务端是唯一权威，缺字段就是协议不一致，
// 应该立刻在开发期暴露出来，而不是被一层「宽松解析」掩盖掉。

import type { Point } from './geometry'

// Seat 是席位号。0 是发起邀请的房主，1 是被邀请方。
export type Seat = 0 | 1

// 观战者不占座位，协议里用 -1 表示。
//
// 单独抽一个常量而不是到处写 -1：观战页与对局页都要靠它判断
// 「这份快照里有没有'我'」，写错一个数字就会把观战者当成座位 0。
export const SEAT_SPECTATOR = -1

// Viewer 是「看的人在哪个位置」：某一方，或者观战。
export type Viewer = Seat | typeof SEAT_SPECTATOR

// BallGroup 是定组结果。
//
// 未定组在协议里是**空串**，不是 'none'：后端 GroupNone 用的是 Go 零值，
// 这样「没赋值」与「故意置空」是同一件事，零值的 RuleState 天然就是未定组。
// 前端必须按空串判断——写成 'none' 会让未定组走进「已清台、该打黑八」的展示分支。
//
// 服务端给的是 [2]BallGroup 定长数组而不是稀疏对象：两个座位永远各有一份，
// 前端读 group[seat] 不必判 undefined。
export type BallGroup = '' | 'solids' | 'stripes'

// MatchStatus 与 pool_match.status 的取值一致。
// waiting 是房间已建、还有一方没进；playing 才允许出杆；
// expired 是 90 秒没人进齐被自动解散。
export type MatchStatus = 'waiting' | 'playing' | 'finished' | 'expired'

// PlaceZone 是手中球可以摆放的区域。
//
// anywhere 是犯规判罚的自由球，整张台面随便摆；behind_head 是开球那一杆，
// 只能摆在开球线之后。服务端已经校验，前端用它约束虚影并画开球线。
export type PlaceZone = 'anywhere' | 'behind_head'

// BallState 是一颗球在某一时刻的状态，单位米。
//
// z 是球底离台面的腾空高度，静止球为 0。它不是球心高度：
// pooltool 里球心静止时本来就在一个半径高处，直接序列化会让每颗球
// 都被当成跳球，渲染时全都放大、影子全都拉远。
//
// on_table 为 false 的球已经落袋，渲染时要跳过。
export interface BallState {
  id: string
  x: number
  y: number
  z: number
  on_table: boolean
}

// ShotParams 是一次击球的参数，服务端只认这五个数。
//
// phi 是方位角（弧度，0 指向 +x），power 是 0~1 的力度，
// theta 是杆头仰角（弧度），a / b 是击球点相对球心的偏移（-1~1）。
// 手机与电脑算出的都是这五个浮点数，因此两端的物理结果必然一致。
export interface ShotParams {
  phi: number
  power: number
  theta: number
  a: number
  b: number
}

// PlayerBrief 是对局双方展示用的资料。avatar 可能是站内相对地址，
// 也可能是空串（没上传过头像），渲染时交给头像工具兜底。
export interface PlayerBrief {
  user_id: number
  nickname: string
  avatar: string
}

// MatchSnapshot 是一次完整对局状态。进房、断线重连、观战收到的都是同一份，
// 客户端拿到它就丢掉本地状态重建场景，不做增量合并。
export interface MatchSnapshot {
  match_id: number
  room_code: string
  status: MatchStatus
  // seat 是「看的人坐哪」。观战时是 SEAT_SPECTATOR(-1)：
  // 观战页据此知道没有「我」这一侧，绝不能用它去下标 players / group。
  seat: Viewer
  turn: Seat
  deadline_ms: number
  balls: BallState[]
  group: [BallGroup, BallGroup]
  // ready 是房间页的两个准备标记，对局开始后恒为 [true, true]。
  // 能不能出杆只看 status 与 turn，不要拿它反推。
  ready: [boolean, boolean]
  ball_in_hand: boolean
  // place_zone 是手中球可以摆的区域，与后端 PlaceZone 一一对应。
  // behind_head 是开球专用：母球只能摆在开球线之后，越线会被服务端拒。
  place_zone: PlaceZone
  // cue_placed 表示这次手中球已经摆过至少一次。出杆的闸门看它：
  // ball_in_hand 在出杆前一直是 true，光看它分不出「还没摆」与「摆好了」。
  cue_placed: boolean
  shot_seq: number
  // players 是定长两项：没进房的那一侧 user_id 为 0，昵称与头像为空串。
  players: [PlayerBrief, PlayerBrief]
  // winner 只有结算后才有值，进行中的快照是 null。
  winner: Seat | null
  // win_reason 是赢法的机读码（eight_legal / eight_early / timeout / leave），
  // 中文文案在前端定，这样服务端改文案不会影响前端的样式分支。
  win_reason: string
}

// RoomSnapshot 是房间页轮询到的状态，比对局快照少一半字段。
// 单独声明而不是复用 MatchSnapshot：房间页不该拿到球位，两个结构混用
// 会让「房间页误读球位」这种 bug 有机会写出来。
export interface RoomSnapshot {
  match_id: number
  room_code: string
  status: MatchStatus
  seat: Seat
  ready: [boolean, boolean]
  players: [PlayerBrief, PlayerBrief]
}

// PoolDrag 是一次画布拖动。dx / dy 是相对按下位置的累计位移（CSS 像素），
// 不是逐帧增量：用累计值算角度不会把每一帧的小误差累加成偏移。
export interface PoolDrag {
  phase: 'start' | 'move' | 'end'
  dx: number
  dy: number
  // point 是当前指针所在的台面世界坐标（米）。拖动转角度只认 dx，
  // 而拖动母球要的是「指针现在在哪」，用位移反推落点会让球和手指错开一段。
  // 指针在画布之外时拿不到换算结果，这时为 null。
  point: Point | null
}
