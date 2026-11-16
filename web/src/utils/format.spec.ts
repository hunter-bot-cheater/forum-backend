import { describe, expect, it, vi, afterEach } from 'vitest'
import { formatCount, formatRelativeTime, initials } from '@/utils/format'

// 这三个函数是纯计算，没有网络与状态依赖，适合作为前端测试的基准线：
// 它们被列表、详情、通知、私信多处复用，改错一处全站都错，而且错得不明显。

describe('formatCount', () => {
  it('一万以内原样显示', () => {
    expect(formatCount(0)).toBe('0')
    expect(formatCount(9999)).toBe('9999')
  })

  it('上万按万显示并去掉多余的 .0', () => {
    expect(formatCount(10000)).toBe('1 万')
    expect(formatCount(12345)).toBe('1.2 万')
    expect(formatCount(123456)).toBe('12.3 万')
  })

  it('负数与非数字兜底成 0', () => {
    // 计数理论上不会为负，但接口返回 null 被转成 NaN 时不能让页面出现「NaN 阅读」。
    expect(formatCount(-1)).toBe('0')
    expect(formatCount(Number.NaN)).toBe('0')
  })
})

describe('initials', () => {
  it('取首字', () => {
    expect(initials('张三')).toBe('张')
  })

  it('emoji 不会被截成半个字符', () => {
    // 用 name[0] 取会拿到代理对的前半段，渲染成方块。
    expect(initials('🐱猫')).toBe('🐱')
  })

  it('空昵称给问号', () => {
    expect(initials('')).toBe('?')
  })
})

describe('formatRelativeTime', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('未来时间按刚刚处理', () => {
    // 服务端时钟比客户端快几秒时，差值会是负数，
    // 不兜住就会显示「-1 分钟前」。
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-17T10:00:00'))
    expect(formatRelativeTime('2026-11-17T10:00:30')).toBe('刚刚')
  })

  it('一天内给分钟与小时', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-17T10:00:00'))
    expect(formatRelativeTime('2026-11-17T09:30:00')).toBe('30 分钟前')
    expect(formatRelativeTime('2026-11-17T07:00:00')).toBe('3 小时前')
  })

  it('昨天给「昨天」而不是小时数', () => {
    // 凌晨看昨晚 23 点的内容，按时间差只有几小时，
    // 但读者想确认的是「哪一天的」，所以按日历天判断。
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-17T01:00:00'))
    expect(formatRelativeTime('2026-11-16T23:00:00')).toBe('昨天')
  })

  it('更早给月日，跨年给完整日期', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-17T10:00:00'))
    expect(formatRelativeTime('2026-11-01T10:00:00')).toBe('11月1日')
    expect(formatRelativeTime('2025-12-31T10:00:00')).toBe('2025-12-31')
  })

  it('非法时间返回空串', () => {
    expect(formatRelativeTime('not-a-date')).toBe('')
  })
})
