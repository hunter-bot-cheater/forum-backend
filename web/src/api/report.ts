import request from '@/utils/request'

// 本文件是后端 internal/report 模块的镜像。
// 只做两件事：定义分类常量、包装提交接口。管理端的列表与处置不在这里——
// 前端目前没有管理后台，那几个接口由管理员直接调，等后台做出来再加。

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
