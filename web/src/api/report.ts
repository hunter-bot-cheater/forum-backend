import request from '@/utils/request'
import type { Brief } from '@/api/message'

// 本文件是后端 internal/report 模块的镜像：举报弹窗要的分类与提交接口，
// 以及管理端的列表与处置。管理端那几个接口只有 admin.user_ids 里的人能调。

// ReportReason 与后端 service.go 里的 reportReasons 一一对应。
// 后端只认这几个 code，文案留在这里翻译：以后想改分类说法，
// 改这一处就行，不用动服务端也不用发版后端。
export type ReportReason = 'spam' | 'abuse' | 'porn' | 'illegal' | 'other'

// REPORT_REASONS 是弹窗里列的选项，顺序即展示顺序。
// 「其他」放最后：前面四类覆盖了绝大多数情况，把兜底项放第一位会让人懒得选。
export const REPORT_REASONS: { value: ReportReason; label: string }[] = [
  { value: 'spam', label: '广告或刷屏' },
  { value: 'abuse', label: '人身攻击' },
  { value: 'porn', label: '色情低俗' },
  { value: 'illegal', label: '违法违规' },
  { value: 'other', label: '其他' },
]

// ReportTarget 是举报对象：文章或评论，id 是那条记录的 id。
export type ReportTarget = 'article' | 'comment'

// SubmitReportResp 只回 id 与状态，前端拿它把按钮切成「已举报」。
export interface SubmitReportResp {
  id: number
  status: string
}

// submitReport 提交一条举报。
//
// 同一用户对同一条内容重复提交会拿到 409（error_code 是 already_exists），
// 拦截器会把「你已经举报过这条内容」直接弹出来，调用方不需要单独处理。
// detail 是补充说明，后端最多收 200 字，这里也按 200 限制输入。
export function submitReport(
  targetType: ReportTarget,
  targetId: number,
  reason: ReportReason,
  detail: string,
) {
  return request<SubmitReportResp>({
    url: '/reports',
    method: 'post',
    data: {
      target_type: targetType,
      target_id: targetId,
      reason,
      detail,
    },
  })
}

// reasonLabel 把分类 code 翻成中文。
// 列表里展示的是「为什么被举报」，管理端不能用 code：
// 一屏几十条举报，没人愿意把 spam/abuse/porn 在脑子里翻译一遍再判断。
// 找不到对应项时原样返回 code，而不是显示空串——
// 后端以后加了新分类，旧前端至少还能看见它是什么。
export function reasonLabel(code: string): string {
  for (const item of REPORT_REASONS) {
    if (item.value === code) {
      return item.label
    }
  }
  return code
}

// 管理端。下面这些接口都在 /api/admin 下，非管理员调用会拿到 403。

// ReportStatus 与后端 model.ReportStatus* 一一对应。
// 待处理之外三种都是终态：一条举报被处置后不能改判，要改就重新举报。
export type ReportStatus = 'pending' | 'dismissed' | 'removed' | 'banned'

// ReportItem 是管理端列表的一行，也是判断一条举报要不要处理所需的全部信息。
//
// 一次拿齐「谁举报了谁、举报什么、内容长什么样」是刻意的：
// 管理员若要点进文章、再点进评论、再找用户主页才能判断，
// 一屏十条举报要开三十个页面，这个后台就没人用了。
export interface ReportItem {
  id: number
  target_type: ReportTarget
  target_id: number
  // article_id 让列表能给出「查看所在文章」的链接；评论类举报时它指向所在文章。
  article_id: number
  target_title: string
  // excerpt 是被举报评论的摘要；文章类举报为空串（正文太长，点进去看更准）。
  excerpt: string
  // target_exists 为 false 表示内容已经被删，这条举报可以直接驳回。
  target_exists: boolean
  // reporter / target_author 在触发者账号注销时整个字段缺失，展示要兜底。
  reporter?: Brief
  target_author?: Brief
  reason: string
  detail: string
  status: ReportStatus
  created_at: string
  // handled_at 只有处理过的举报才有值。
  handled_at: string | null
}

export interface ReportListResp {
  list: ReportItem[]
  total: number
  page: number
  page_size: number
}

// listReports 拉管理端举报列表。
//
// status 不传时后端按 pending 处理（默认只看要干的活），
// 这里也把默认值写在函数签名里，调用方不必记后端的缺省规则。
export function listReports(status: ReportStatus = 'pending', page = 1, pageSize = 10) {
  return request<ReportListResp>({
    url: '/admin/reports',
    method: 'get',
    params: { status, page, page_size: pageSize },
  })
}

// HandleAction 是处置动作：驳回、仅下架内容、下架并封禁作者。
export type HandleAction = 'dismiss' | 'remove' | 'ban'

// handleReport 处置一条举报。
//
// banDays 只在 action='ban' 时被后端读取，范围 1..3650（3650 表示「十年」，
// 后端有意不提供「永久」——误封永久账号没法靠回滚补救）。
// 其它两种动作传 0，后端会忽略这个字段。
export function handleReport(id: number, action: HandleAction, banDays = 0) {
  return request<null>({
    url: '/admin/reports/' + String(id),
    method: 'put',
    data: { action, ban_days: banDays },
  })
}
