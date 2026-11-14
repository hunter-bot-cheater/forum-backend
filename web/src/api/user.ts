import request from '@/utils/request'

export interface UserBrief {
  id: number
  username: string
}

export interface LoginResp {
  token: string
  user: UserBrief & {
    created_at: string
    updated_at: string
  }
}

export function login(data: { username: string; password: string }) {
  return request<LoginResp>({ url: '/login', method: 'post', data })
}

export function register(data: { username: string; password: string }) {
  return request<UserBrief>({ url: '/register', method: 'post', data })
}
