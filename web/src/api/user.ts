import request from '@/utils/request'

// UserResp 是后端 userResp 的镜像：资料本体。
// nickname 已经由服务端做过回退（空昵称填成 username），可以直接渲染。
// has_custom_nickname 用来区分「用户真设了昵称」和「用户没设、这里是回退出来的」，
// 编辑表单靠它决定输入框回填 nickname 还是回填空串。
export interface UserResp {
  id: number
  username: string
  nickname: string
  avatar_url: string
  bio: string
  has_custom_nickname: boolean
  created_at: string
}

// UserProfile 是后端 profileResp：资料本体 + 关注统计。
// 个人主页和自己主页用同一个结构，只有 is_following 的取值不同。
export interface UserProfile extends UserResp {
  following_count: number
  follower_count: number
  is_following: boolean
}

// LoginResp 是登录与刷新共用的 token 形状。
//
// 双 token：access 15 分钟，随每个请求发送；refresh 7 天，只在刷新接口用。
// expires_in 是 access 的剩余秒数，后端算好给前端，避免两边各自维护一份有效期常量。
// 刷新接口不返回 user（身份没变，前端也不需要重填资料）。
export interface LoginResp {
  access_token: string
  refresh_token: string
  expires_in: number
  user: UserResp
}

export function login(data: { username: string; password: string }) {
  return request<LoginResp>({ url: '/login', method: 'post', data })
}

// logout 让服务端撤销这条 refresh 链。
//
// 必须调：只清本地存储是「假装退出」，token 还在有效期内，捡到它的人照样能用。
// 这个接口不挂鉴权，access 过期了也能调，所以不需要等待刷新。
// 失败由调用方吞掉：退出动作在本地已经完成，不该因为网络问题卡住用户。
export function logout(refreshToken: string) {
  return request<{ message: string }>({
    url: '/auth/logout',
    method: 'post',
    data: { refresh_token: refreshToken },
  })
}

// getMe 取当前登录用户的资料 + 关注统计。
//
// 为什么需要它：登录接口只返回 token 和用户名，前端把 token 存进 localStorage 后
// 刷新页面，内存里的用户信息全没了，而聊天、点赞、个人中心都要用到 id 与昵称。
// 靠登录响应在本地持久化也能凑合，但头像昵称改过之后本地就是旧值，
// 走一次 /users/me 才是唯一可信的来源。
export function getMe() {
  return request<UserProfile>({ url: '/users/me', method: 'get' })
}

export function register(data: { username: string; password: string }) {
  return request<UserResp>({ url: '/register', method: 'post', data })
}

// 下面是用户主页要用的接口，对应后端 internal/user 与 internal/follow。

// getProfile 看别人的主页（也看得到自己）。
// 与 getMe 的区别：getMe 走 auth 组、只能看自己，profile 走公开组、要带 id。
// 路由挂了可选鉴权，所以带 token 时 is_following 才是真实值，游客恒为 false。
export function getProfile(id: number) {
  return request<UserProfile>({ url: '/users/' + String(id), method: 'get' })
}

// ProfilePatch 是编辑资料的请求体，对应后端 updateProfileReq。
// 三个字段都可选：不传 = 不改这一项，传空串 = 清空这一项。
// 表单是完整回填的，保存时三项目前都会传，但类型仍按可选写，
// 与后端的指针语义保持一致，以后加「只改头像」这类入口不用改类型。
export interface ProfilePatch {
  nickname?: string
  avatar_url?: string
  bio?: string
}

// updateMe 保存当前用户的资料，返回更新后的完整主页结构。
// 返回的是 profileResp 而不是 userResp：调用方拿到后直接覆盖本地 profile，
// 关注计数不会因为这次更新被抹成 0。
export function updateMe(patch: ProfilePatch) {
  return request<UserProfile>({ url: '/users/me', method: 'put', data: patch })
}

// FollowState 是关注 / 取关之后的返回。
// follower_count 是操作后的真实粉丝数，直接覆盖，不要在旧值上加减：
// 并发下加减会越差越远。
// changed 表示这次有没有真的改到数据（重复关注、取消没关注的人都是 false）。
export interface FollowState {
  following: boolean
  follower_count: number
  changed: boolean
}

export function followUser(id: number) {
  return request<FollowState>({ url: '/users/' + String(id) + '/follow', method: 'put' })
}

export function unfollowUser(id: number) {
  return request<FollowState>({ url: '/users/' + String(id) + '/follow', method: 'delete' })
}

// FollowItem 是关注 / 粉丝列表里的一行。
// followed_by_me 供粉丝列表画「回关」按钮（在「我的关注」里恒为 true）。
export interface FollowItem {
  id: number
  username: string
  nickname: string
  avatar_url: string
  followed_at: string
  followed_by_me: boolean
}

export interface FollowListResp {
  list: FollowItem[]
  total: number
  page: number
  page_size: number
}

// followKind 只有两个取值，用联合类型而不是 string：
// 写错成 'following'/'fans' 时编译期就能发现，不用等到 404。
export type FollowKind = 'following' | 'followers'

// listFollows 拉某个人的关注或粉丝列表。
// 两个接口形状完全一样，只是路径不同，所以合成一个函数 + 一个 kind 参数，
// 免得在两处各写一份相同的分页与类型。
export function listFollows(id: number, kind: FollowKind, page = 1, pageSize = 20) {
  return request<FollowListResp>({
    url: '/users/' + String(id) + '/' + kind,
    method: 'get',
    params: { page, page_size: pageSize },
  })
}
