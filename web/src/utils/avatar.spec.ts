import { describe, expect, it } from 'vitest'
import { avatarColor } from './avatar'

describe('avatarColor', () => {
  it('同一个昵称反复取色结果不变', () => {
    const first = avatarColor('九十九')
    expect(avatarColor('九十九')).toBe(first)
    expect(avatarColor('九十九')).toBe(first)
  })

  it('昵称缺失时退回中性色', () => {
    expect(avatarColor('')).toBe('#8b93a3')
    expect(avatarColor(undefined)).toBe('#8b93a3')
    expect(avatarColor(null)).toBe('#8b93a3')
  })

  it('任何名字都返回合法的 #rrggbb', () => {
    const names = ['a', '张三', 'dash-and_underscore', '🐱', '很长的昵称'.repeat(20)]
    for (const name of names) {
      expect(avatarColor(name)).toMatch(/^#[0-9a-f]{6}$/)
    }
  })

  it('一批常见昵称能散开，不会十几个人挤在两三个颜色上', () => {
    const seen = new Set<string>()
    for (let i = 1; i <= 20; i += 1) {
      seen.add(avatarColor('用户' + String(i)))
    }
    expect(seen.size).toBeGreaterThanOrEqual(5)
  })
})
