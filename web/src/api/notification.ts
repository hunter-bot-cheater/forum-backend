import request from '@/utils/request'
import type { Brief } from '@/api/message'

// 本文件是后端 internal/notification/dto.go 的镜像，字段名保持 snake_case。
// 与私信共用 Brief：actor 和 peer 是同一份「展示一个用户」的结构，
// 各写一份迟早会出现一边加了头像、另一边还是旧字段。

// NotificationResp 是一条站内通知。
// type 是事件类型（like.created / comment.created），前端按它决定文案；
// target_type + target_id 是要跳转的对象，article_id 是展示上下文。
export interface NotificationResp {
  id: number
  event_id: string
  actor_id: number
  type: string
  article_id: number
  target_type: string
  target_id: number
  is_read: boolean
  created_at: string
  // actor 可能缺失：触发者注销后后端省掉整个字段（omitempty），
  // 此时只能显示「有人」，跳主页也没有意义。
  actor?: Brief
  // article_title 是互动发生的那篇文章的标题。
  // 文章已删或非文章域事件（article_id=0）时整个字段不存在，
  // 前端据此决定要不要渲染标题行，而不是显示一个空引用框。
  article_title?: string
}

// NotificationKind 是消息中心的分类，和后端 service 里的常量一一对应。
// 空串表示「全部」：后端把空分类当成不过滤，前端就不用为「全部」单独写一套接口。
export type NotificationKind = '' | 'like' | 'comment' | 'follow'

export interface NotificationListResp {
  list: NotificationResp[]
  total: number
  unread_count: number
  page: number
  page_size: number
  // kind 是服务端回显的分类。连续切 tab 时会有多个请求同时在飞，
  // 慢的那次回来若属于旧 tab，必须丢弃，否则列表会闪回上一个分类。
  kind: NotificationKind
}

// UnreadSummaryResp 是各分类未读数，对应顶部 tab 与侧边栏角标。
// by_kind 一定包含全部分类（没有未读时是 0），前端不用判 undefined。
export interface UnreadSummaryResp {
  total: number
  by_kind: Record<string, number>
}

// listNotifications 拉一页通知，kind 为空表示全部。
// unread_count 是当前分类的未读数，不是全站未读；全站未读看 getUnreadSummary。
export function listNotifications(kind: NotificationKind = '', page = 1, pageSize = 20) {
  return request<NotificationListResp>({
    url: '/notifications',
    method: 'get',
    params: { page, page_size: pageSize, kind },
  })
}

// getUnreadSummary 只取计数，不取列表。
// 侧边栏角标和三个 tab 的红点都要数字，拉一页通知来数太浪费。
export function getUnreadSummary() {
  return request<UnreadSummaryResp>({
    url: '/notifications/summary',
    method: 'get',
  })
}

// readAllNotifications 把全部未读标记为已读，返回受影响条数。
// 只去掉红点、不删记录：历史消息一直留在列表里可翻可点，
// 和抖音的消息中心一致（已读 = 红点消失）。
export function readAllNotifications() {
  return request<{ affected: number }>({
    url: '/notifications/read-all',
    method: 'put',
  })
}

// readNotification 把单条通知标记为已读，返回受影响条数（已读过是 0）。
// 用户点开哪条就清哪条的红点，「全部已读」会把还没看的也一起清掉，
// 两个入口的语义不能混。
export function readNotification(id: number) {
  return request<{ affected: number }>({
    url: '/notifications/' + String(id) + '/read',
    method: 'put',
  })
}
