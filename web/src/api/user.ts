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

export interface LoginResp {
  token: string
  user: UserResp
}

export function login(data: { username: string; password: string }) {
  return request<LoginResp>({ url: '/login', method: 'post', data })
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
