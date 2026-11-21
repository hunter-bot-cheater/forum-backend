import request from '@/utils/request'
import type { PlayerBrief, RoomSnapshot, Seat } from '@/game/types'
import type { ShotFrame } from '@/game/animator'

// 本文件是后端 internal/pool/dto.go 的镜像，字段名保持 snake_case。
//
// 台球模块有两类接口：这里的 HTTP 与 /api/ws/game 的 WebSocket。
// 分工是「低频、可重试的走 HTTP，高频、要实时广播的走 WS」：
// 邀请、房间状态、准备按钮都是几秒一次的动作，用轮询让连接数少一个量级；
// 出杆与对手动作必须即时到达，只能走 WS。

// InviteResp 是发起邀请的返回。
// room_code 是给玩家念的六位房号，match_id 才是进房要用的主键。
export interface InviteResp {
  invite_id: string
  match_id: number
  room_code: string
  expires_in: number
}

// PendingInvite 是「我收到的邀请」。
// expires_in 是剩余秒数而不是截止时间戳：客户端与服务端的钟经常差几十秒，
// 用剩余量算倒计时两边看到的是同一个数。
export interface PendingInvite {
  invite_id: string
  match_id: number
  room_code: string
  from: PlayerBrief
  expires_in: number
}

// AcceptInviteResp 是接受邀请的返回，拿到就能跳房间页。
export interface AcceptInviteResp {
  match_id: number
  room_code: string
}

// PoolRoom 是房间页轮询的快照，比 RoomSnapshot 多一层字段校验的余地。
// 这里直接复用 game/types.ts 的 RoomSnapshot：
// 房间页与对局页面对的是同一份「谁坐哪、谁准备了」，各写一份迟早对不上。
export type PoolRoom = RoomSnapshot

// 邀请一个我关注的人。后端会校验关注关系，没关注返回 400。
//
// 不传房号：房号由服务端生成，客户端自己编会出现两局撞号。
export function createInvite(toUserId: number) {
  return request<InviteResp>({
    url: '/pool/invites',
    method: 'post',
    data: { to_user_id: toUserId },
  })
}

// 拉待接受的邀请。
//
// 实时到达靠全局 WS 推的 pool_invite，这个接口负责两件 WS 覆盖不到的事：
// 刷新页面后补回推送期间漏掉的邀请，以及弹层被关掉后重新找回入口。
export function listPendingInvites() {
  return request<PendingInvite[]>({
    url: '/pool/invites/pending',
    method: 'get',
  })
}

// 接受邀请。房号与 match_id 由服务端回显，不从邀请对象里抄——
// 两边若有一处不一致，跳错房间比报错更难查。
export function acceptInvite(inviteId: string) {
  return request<AcceptInviteResp>({
    url: '/pool/invites/' + encodeURIComponent(inviteId) + '/accept',
    method: 'post',
  })
}

// 拒绝邀请。服务端会顺手解散已经建好的那个房间。
export function declineInvite(inviteId: string) {
  return request<{ declined: boolean }>({
    url: '/pool/invites/' + encodeURIComponent(inviteId) + '/decline',
    method: 'post',
  })
}

// 读房间状态。房间页按 1.5 秒轮询它，看到 status 变 playing 就跳对局页。
//
// 房间页为什么不连 WebSocket：准备是低频动作，为它多建一条长连接不划算，
// 而且轮询天然带「进页面就有一份当前状态」，不用等首帧推送。
export function getRoom(matchId: number | string) {
  return request<PoolRoom>({
    url: '/pool/rooms/' + String(matchId),
    method: 'get',
  })
}

// 准备 / 取消准备。返回的是整份房间状态，前端直接替换本地快照，
// 不做增量合并：两份状态一旦分叉，页面会一直显示错的准备标记。
export function setReady(matchId: number | string, ready: boolean) {
  return request<PoolRoom>({
    url: '/pool/rooms/' + String(matchId) + '/ready',
    method: 'post',
    data: { ready },
  })
}

// PoolRating 是战绩汇总。胜率不在这里算：它是派生值，
// 后端只给胜负场次，页面上那一个百分比就不可能和列表对不上。
export interface PoolRating {
  elo: number
  wins: number
  losses: number
}

// PoolRecord 是战绩列表里的一行。
export interface PoolRecord {
  match_id: number
  // opponent 是对手；列表里没有「我」的资料，也不需要判断哪一份是自己。
  opponent: PlayerBrief
  won: boolean
  win_reason: string
  shot_count: number
  // ended_at 可能为空（早期数据没有结束时间），渲染时兜一下。
  ended_at: string | null
}

export interface RecordsResp {
  rating: PoolRating
  list: PoolRecord[]
  total: number
  page: number
  size: number
}

// ReplayShot 是回放里的一杆。frames 与对局时的 shot 帧同形，
// 复用同一个 animator 播放，不需要第二套解码。
export interface ReplayShot {
  seq: number
  seat: Seat
  duration: number
  frames: ShotFrame[]
}

export interface ReplayResp {
  match_id: number
  players: [PlayerBrief, PlayerBrief]
  winner: Seat
  win_reason: string
  shots: ReplayShot[]
}

// getRecords 拉我的战绩。分页参数异常时由后端退回默认值，前端不必校验。
export function getRecords(page = 1, size = 10) {
  return request<RecordsResp>({
    url: '/pool/records',
    method: 'get',
    params: { page, size },
  })
}

// getReplay 拉一局的回放。只有这一局的两位玩家能取到，其余返回 403。
export function getReplay(matchId: number | string) {
  return request<ReplayResp>({
    url: '/pool/matches/' + String(matchId) + '/replay',
    method: 'get',
  })
}
