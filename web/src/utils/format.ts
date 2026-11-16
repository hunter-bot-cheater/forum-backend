// 时间与 id 的小工具，聊天界面专用。
// 不引 dayjs / date-fns：这里只需要两种固定格式，
// 为两行格式化代码加一个依赖包，包体积和升级成本都不划算。

// pad2 补零到两位：9 -> "09"。
function pad2(n: number): string {
  if (n < 10) {
    return '0' + String(n)
  }
  return String(n)
}

// isSameDay 判断两个时间是不是同一天（按本地时区）。
function isSameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  )
}

// formatChatTime 是聊天气泡上方/旁边的时间。
// 今天的消息只显示时分，昨天显示「昨天 HH:mm」，
// 更早的带上日期——聊天记录容易跨天，只给时分会让「这条是今天的还是上周的」分不清。
export function formatChatTime(input: string): string {
  const d = new Date(input)
  if (Number.isNaN(d.getTime())) {
    return ''
  }
  const now = new Date()
  const hm = pad2(d.getHours()) + ':' + pad2(d.getMinutes())

  if (isSameDay(d, now)) {
    return hm
  }
  const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)
  if (isSameDay(d, yesterday)) {
    return '昨天 ' + hm
  }
  if (d.getFullYear() === now.getFullYear()) {
    return d.getMonth() + 1 + '月' + d.getDate() + '日 ' + hm
  }
  return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) + ' ' + hm
}

// formatListTime 是会话列表右侧那一小列时间。
// 位置窄，只保留最粗的粒度：今天给时分，昨天给「昨天」，更早给月日。
export function formatListTime(input: string): string {
  const d = new Date(input)
  if (Number.isNaN(d.getTime())) {
    return ''
  }
  const now = new Date()
  if (isSameDay(d, now)) {
    return pad2(d.getHours()) + ':' + pad2(d.getMinutes())
  }
  const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)
  if (isSameDay(d, yesterday)) {
    return '昨天'
  }
  if (d.getFullYear() === now.getFullYear()) {
    return String(d.getMonth() + 1) + '/' + String(d.getDate())
  }
  return String(d.getFullYear()) + '/' + String(d.getMonth() + 1) + '/' + String(d.getDate())
}

// newClientMsgId 生成发送幂等键。
//
// 优先用 crypto.randomUUID：浏览器原生、碰撞概率可以忽略。
// 但它只在安全上下文（https 或 localhost）里存在，
// 用局域网 IP 走 http 调试时是 undefined，所以必须有兜底——
// 少了这个兜底，测试环境发送会直接抛异常，而生产环境却正常，最难查的那种。
export function newClientMsgId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  const rand = Math.random().toString(36).slice(2, 10)
  return Date.now().toString(36) + '-' + rand
}

// initials 取昵称首字给头像兜底。
// 用户没上传头像时 el-avatar 会显示一片空白，放一个字至少能区分不同的人。
export function initials(name: string): string {
  if (!name) {
    return '?'
  }
  // 用 Array.from 而不是 name[0]：emoji 和部分生僻字是代理对，
  // 直接取下标会截出半个字符，渲染成乱码方块。
  return Array.from(name)[0]
}
