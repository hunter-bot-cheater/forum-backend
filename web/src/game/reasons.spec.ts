import { describe, expect, it } from 'vitest'
import { reasonLabel, reasonNeutral, reasonShort } from '@/game/reasons'

// 这一组用例钉住「码是赢家视角」这件事。
// 曾经漏了 won 参数，输家那一行写成「对手中途离开」——
// 而真正离开的是他自己，看起来像在替对手道别。

describe('reasonLabel', () => {
  it('自己打进黑八时说明赢在哪', () => {
    expect(reasonLabel('eight_legal', true)).toBe('打进黑八，赢下这一局')
  })

  it('输的那一方，离开/超时/提前打黑八都算在自己头上', () => {
    expect(reasonLabel('leave', false)).toBe('你中途离开，判负')
    expect(reasonLabel('timeout', false)).toBe('你连续两次超时，判负')
    expect(reasonLabel('eight_early', false)).toBe('你提前打进黑八')
  })

  it('赢的那一方，同样的码说的是对手', () => {
    expect(reasonLabel('leave', true)).toBe('对手中途离开，判负')
    expect(reasonLabel('timeout', true)).toBe('对手连续两次超时，判负')
  })

  it('未登记的码不猜原因', () => {
    expect(reasonLabel('who_knows', true)).toBe('本局结束')
  })
})

describe('reasonShort', () => {
  it('输家那一行不许出现「对手」两个字', () => {
    for (const code of ['leave', 'timeout', 'eight_early']) {
      expect(reasonShort(code, false)).not.toContain('对手')
    }
  })

  it('黑八与犯规打进黑八不区分视角', () => {
    expect(reasonShort('eight_legal', false)).toBe('打进黑八')
    expect(reasonShort('eight_with_foul', true)).toBe('犯规打进黑八')
  })
})

describe('reasonNeutral', () => {
  // 回放页没有「我」这一侧，所以这里必须一组不带主语的说法。
  it('不出现「你」和「对手」', () => {
    for (const code of ['eight_legal', 'eight_early', 'eight_with_foul', 'timeout', 'leave']) {
      const text = reasonNeutral(code)
      expect(text).not.toContain('你')
      expect(text).not.toContain('对手')
    }
  })

  it('每种码都说得出这一局怎么结束的', () => {
    expect(reasonNeutral('leave')).toBe('中途离开')
    expect(reasonNeutral('timeout')).toBe('连续两次超时')
    expect(reasonNeutral('eight_legal')).toBe('打进黑八')
  })

  it('未登记的码同样不猜', () => {
    expect(reasonNeutral('')).toBe('已结束')
  })
})
