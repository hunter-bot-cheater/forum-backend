import request from '@/utils/request'
import type { MatchStatus, PlayerBrief, RoomSnapshot, Seat } from '@/game/types'
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

// 邀请一个在线的人打一局。
//
// 不要求关注：谁是好友是社交关系，能不能一起打球是另一件事。
// 但要求对方在线——离线的邀请只能干等一分钟，对方根本不会知道。
//
// 不传房号：邀请指向发起人自己的房间，房号由服务端生成。
// 客户端自己编房号会出现两局撞号，而且对方接受之后进的是哪个房间也说不清。
export function createInvite(toUserId: number) {
  return request<InviteResp>({
    url: '/pool/invites',
    method: 'post',
    data: { to_user_id: toUserId },
  })
}

// OnlineUser 是「现在在线的人」里的一条。
// following 只用来分组（关注的人排前面），不作为「能不能邀请」的条件。
export interface OnlineUser {
  user_id: number
  nickname: string
  avatar: string
  following: boolean
}

// 拉当前在线、且没在打的人。邀请面板用。
//
// 排除自己与正在对局的人由服务端做：名单要按「谁更可能答应」排序，
// 而排序需要知道谁在打，这些判断在服务端一次做完比前端拼两三个接口简单。
// quiet：大厅每几秒轮询一次，失败时页面自己有「暂无其他人在线」这一态，
// 再弹一条全局红条只会在限流时刷屏。
export function getOnlineUsers() {
  return request<OnlineUser[]>({
    url: '/pool/online',
    method: 'get',
    quiet: true,
  })
}

// 读我的房间，没有就开一间。
//
// 用 POST 而不是 GET：它可能建一条新房间（写操作），
// 而 GET 在半路被浏览器或代理重试时留下两行房间是个很难查的 bug。
// 房间页每两秒轮询它一次：同一间房会被反复返回，房号不会变。
// quiet：与在线名单同理，房间拉不到时大厅显示「房间没开出来 + 重试」，
// 不需要再来一条弹到右上角的红条。
export function getMyRoom() {
  return request<PoolRoom>({
    url: '/pool/rooms/my',
    method: 'post',
    quiet: true,
  })
}

// 按房号进别人的房间。房号不对 / 房间已开局 / 座位被占各有各的文案，
// 服务端直接给中文说明，前端原样弹出来即可。
//
// ready 只由分享链接传：点链接进来的人显然是想打的，让服务端顺手替他
// 按下准备，房主点一下就能开局。手动输房号的入口不传它。
export function joinRoom(roomCode: string, ready = false) {
  return request<PoolRoom>({
    url: '/pool/rooms/join',
    method: 'post',
    data: { room_code: roomCode, ready },
  })
}

// 退出别人的房间。自己的房间没有这个动作（房号还要留给别人用）。
export function leaveRoom() {
  return request<{ left: boolean }>({
    url: '/pool/rooms/leave',
    method: 'post',
  })
}

// 开一局人机对战。
//
// 服务端直接开打，不经过房间与准备那两步：人机局不需要房号，也不需要等谁点准备。
// 返回的是房间快照，status 已经是 playing，拿到 match_id 就能直接进对局页。
//
// 不静默失败：这是「点了一下什么都没有发生」的典型场景，必须让拦截器弹出来。
export function startBotMatch() {
  return request<PoolRoom>({
    url: '/pool/rooms/bot',
    method: 'post',
  })
}

// 换一间房：把当前这间还没开打的作废，另开一间（房号会变）。
//
// 对面可能早就关了页面，而房间只要还有人轮询就一直活着。
// 没有这个入口，房主面对的是一个永远不点准备的人，只能等两小时清扫。
export function reopenRoom() {
  return request<PoolRoom>({
    url: '/pool/rooms/reopen',
    method: 'post',
  })
}

// 拉待接受的邀请。
//
// 实时到达靠全局 WS 推的 pool_invite，这个接口负责两件 WS 覆盖不到的事：
// 刷新页面后补回推送期间漏掉的邀请，以及弹层被关掉后重新找回入口。
// quiet：这是后台补拉，没有待接受邀请本来就是常态，
// 一次失败不该在用户读文章的时候弹一条红条出来。
export function listPendingInvites() {
  return request<PendingInvite[]>({
    url: '/pool/invites/pending',
    method: 'get',
    quiet: true,
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

// 拒绝邀请。房间不跟着解散：邀请人还在自己那间房里等着，房号还要留给别人用，
// 收掉反而会让报给第三个人的那串数字突然失效。
export function declineInvite(inviteId: string) {
  return request<{ declined: boolean }>({
    url: '/pool/invites/' + encodeURIComponent(inviteId) + '/decline',
    method: 'post',
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

// RematchStarted 是双方都点了「再约一局」之后新开出来的那一局。
// breaker 是这一局的开球方：结算界面可以据此说一句「这局你先开球」，
// 前端不必自己推算「上一局谁开的、这局轮到谁」。
export interface RematchStarted {
  match_id: number
  room_code: string
  breaker: Seat
}

// RematchResp 是「再约一局」的返回。
// started 与 waiting 互斥：前者表示已经开局，后者表示只记下了我这一票。
export interface RematchResp {
  started?: RematchStarted
  waiting: boolean
}

// requestRematch 告诉服务端「我想再打一局」。
//
// 不再走「回大厅 → 发邀请 → 等对方接受 → 双方准备」那一串：
// 两个人都已经表态要再打，多出来的三步只是让人多点三次按钮。
// 对方也点过的那一刻，服务端直接把新局开起来，两边同时跳过去。
export function requestRematch(matchId: number | string) {
  return request<RematchResp>({
    url: '/pool/matches/' + String(matchId) + '/rematch',
    method: 'post',
  })
}

// LiveMatch 是观战列表里的一条。
//
// 只有「要不要点进去看」需要的信息：谁在打、打到第几杆、还有没有位置。
// 球位不在这里：那是点进去之后由 WebSocket 快照给的，
// 列表每次都拖着十六颗球的坐标没有意义，还会让人以为列表是实时的。
export interface LiveMatch {
  match_id: number
  status: MatchStatus
  turn: Seat
  // players 是定长两项，两个座位按先后顺序固定，前端不做左右翻转。
  players: [PlayerBrief, PlayerBrief]
  shot_seq: number
  spectators: number
  spectators_max: number
}

// getLive 拉正在进行的对局。返回的是内存里的对局，已结束的立刻消失。
// quiet：这是首页列表的轮询接口，失败时保留上一份列表即可。
export function getLiveMatches() {
  return request<LiveMatch[]>({
    url: '/pool/live',
    method: 'get',
    quiet: true,
  })
}
