import request from '@/utils/request'
import type { Brief } from '@/api/message'

// 本文件是后端 internal/comment/dto.go 的镜像，字段名保持 snake_case。
// 评论结构只有两层：根评论（root）和挂在它下面的回复，回复本身不再往下嵌套。

// CommentResp 是一条评论。
//
// 顶级评论的 parent_id 与 root_id 都是 0；
// 回复的 parent_id 指向它直接回复的那条（可能是根评论，也可能是别的回复），
// root_id 指向所属的根评论，后端按这两个字段组树，前端不用自己算。
export interface CommentResp {
  id: number
  article_id: number
  content: string
  created_at: string
  author?: Brief
  parent_id: number
  root_id: number
  // reply_to 只出现在回复上，用来显示「回复 @张三」。
  // 被回复者账号注销时整个字段省略，前端按「匿名」处理。
  reply_to?: Brief
  // replies 只出现在根评论上，且没有回复时字段整个缺失（后端 omitempty 对空切片同样生效）。
  // 所以读取时用 `item.replies ?? []`，不要直接 .length。
  replies?: CommentResp[]
  // reply_count 是这条根评论下回复的真实总数，可能大于 replies.length：
  // 后端对每条根评论最多带 200 条回复，超出的只计数不下发。
  reply_count: number
}

export interface CommentListResp {
  list: CommentResp[]
  total: number
  page: number
  page_size: number
}

// listComments 拉一页根评论，回复跟着各自根评论一起返回。
// page 指的是「根评论的第几页」，不是「所有评论的第几页」。
export function listComments(articleId: number, page = 1, pageSize = 20) {
  return request<CommentListResp>({
    url: '/articles/' + String(articleId) + '/comments',
    method: 'get',
    params: { page, page_size: pageSize },
  })
}

// CommentAnchor 描述一条评论在评论区里的位置，供「从通知点进来直接落到那条评论」使用。
//
// 列表按根评论分页，前端手上只有一个评论 id，自己数不出它在第几页，
// 所以这个位置由服务端算好再给。
export interface CommentAnchor {
  comment_id: number
  article_id: number
  // root_id 是所属根评论；评论本身就是根评论时等于 comment_id。
  root_id: number
  // index 是所属根评论在文章里的序号，从 1 开始。
  index: number
}

// locateComment 查一条评论的定位信息。
// 评论已删时后端返回 404，拦截器会弹提示，调用方按「定位失败」处理即可。
export function locateComment(id: number) {
  return request<CommentAnchor>({
    url: '/comments/' + String(id) + '/locate',
    method: 'get',
  })
}

// createComment 发表评论或回复。
//
// parentId 为 0（不传）是顶级评论；大于 0 表示回复那条评论，
// 由后端根据它推导 root_id，前端不需要自己算属于哪棵树。
// 注意返回值没有 author：后端新建评论时没有预加载用户，author 字段会被省略。
// 所以不要拿返回值直接塞进列表渲染，发完重新拉一次列表更稳。
export function createComment(articleId: number, content: string, parentId = 0) {
  return request<CommentResp>({
    url: '/articles/' + String(articleId) + '/comments',
    method: 'post',
    data: { content, parent_id: parentId },
  })
}

// deleteComment 删除自己的评论。删根评论会连带删掉它下面所有回复。
// 别人的评论会返回 404（后端把「不存在」与「无权删除」合并，不泄露归属）。
export function deleteComment(id: number) {
  return request<null>({
    url: '/comments/' + String(id),
    method: 'delete',
  })
}
