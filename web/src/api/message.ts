import request from '@/utils/request'

// 本文件是后端 internal/message/dto.go 的镜像。
// 字段名一律 snake_case，和后端 json tag 对齐，中间不做驼峰转换：
// 多一层转换就多一处会漏改的地方，而且对着抓包和 swagger 看代码时不用心算。

// Brief 是用户的展示信息，对应后端 user.BriefResp。
// nickname 已经由服务端做过「空昵称回退用户名」，前端拿到直接渲染，不用判空。
export interface Brief {
  id: number
  username: string
  nickname: string
  avatar_url: string
}

// MessageResp 是一条消息，HTTP 历史消息与 WebSocket 实时推送共用这个形状。
// mine 由服务端算：前端拿自己的 id 和 sender_id 比也能算，但会话列表里
// 「最后一条是不是我发的」也要用同一个判断，两处各写一份迟早对不上。
export interface MessageResp {
  id: number
  sender_id: number
  content: string
  read_at: string | null
  created_at: string
  mine: boolean
  // client_msg_id 只有客户端发过幂等键才会出现（后端 omitempty）。
  // 前端靠它把「本地先画出来的灰气泡」替换成服务端返回的那条。
  client_msg_id?: string
}

// ConversationResp 是会话列表的一行。
// last_message 可能为 null：会话行先建好、消息还没写进来的瞬间存在这种状态。
export interface ConversationResp {
  peer: Brief
  last_message: MessageResp | null
  unread: number
  updated_at: string
}

export interface ConversationListResp {
  list: ConversationResp[]
  total: number
  page: number
  page_size: number
}

// MessageListResp 是「进入与某人的聊天页」所需的全部数据。
// 用游标而不是页码：before_id 是本页最早一条的 id，往上翻时原样传回；
// has_more 为 false 表示已经到最早一条，不用再请求。
export interface MessageListResp {
  list: MessageResp[]
  has_more: boolean
  next_before_id: number
  peer: Brief
}

// 会话列表：一页拉够即可，暂不做无底洞滚动，聊天列表超过 50 人时再说。
export function listConversations(page = 1, pageSize = 50) {
  return request<ConversationListResp>({
    url: '/conversations',
    method: 'get',
    params: { page, page_size: pageSize },
  })
}

// 全部会话的未读合计，给侧边栏角标用。
export function getUnreadTotal() {
  return request<{ unread: number }>({ url: '/conversations/unread', method: 'get' })
}

// 与某人的消息列表。beforeId 传 0（或不传）= 取最新一页。
// 返回里带 peer，所以「从别人主页点私信」进来只需要这一个请求。
export function listMessages(peerId: number, beforeId = 0, limit = 20) {
  const params: Record<string, number> = { limit }
  if (beforeId > 0) {
    params.before_id = beforeId
  }
  return request<MessageListResp>({
    url: `/conversations/with/${peerId}/messages`,
    method: 'get',
    params,
  })
}

// 发一条私信。clientMsgId 是幂等键：弱网重试、用户双击都不会产生重复气泡。
// 同一个 key 重复提交时后端返回第一次那条消息，id 与内容都不变。
export function sendMessage(peerId: number, content: string, clientMsgId: string) {
  return request<MessageResp>({
    url: `/conversations/with/${peerId}/messages`,
    method: 'post',
    data: { content, client_msg_id: clientMsgId },
  })
}

// 把与某人的会话标记为已读，返回清掉多少条。
export function markConversationRead(peerId: number) {
  return request<{ cleared: number }>({
    url: `/conversations/with/${peerId}/read`,
    method: 'put',
  })
}
