import { describe, expect, it } from 'vitest'
import { DEFAULT_REDIRECT, safeRedirect } from './redirect'

describe('safeRedirect', () => {
  it('站内路径原样返回', () => {
    expect(safeRedirect('/hot')).toBe('/hot')
    expect(safeRedirect('/articles/7?comment=3')).toBe('/articles/7?comment=3')
  })

  it('缺省值与空串回文章列表', () => {
    expect(safeRedirect(undefined)).toBe(DEFAULT_REDIRECT)
    expect(safeRedirect(null)).toBe(DEFAULT_REDIRECT)
    expect(safeRedirect('')).toBe(DEFAULT_REDIRECT)
  })

  it('重复参数拿到数组时回文章列表', () => {
    // ?redirect=/a&redirect=/b 时 vue-router 给的是数组，不是字符串。
    expect(safeRedirect(['/a', '/b'])).toBe(DEFAULT_REDIRECT)
  })

  it('绝对地址被挡住', () => {
    // 不挡的话登一个记就能把用户送去外站钓鱼页。
    expect(safeRedirect('https://evil.example/login')).toBe(DEFAULT_REDIRECT)
    expect(safeRedirect('javascript:alert(1)')).toBe(DEFAULT_REDIRECT)
  })

  it('协议相对地址被挡住', () => {
    // //evil.example 在浏览器里等价于 https://evil.example。
    expect(safeRedirect('//evil.example')).toBe(DEFAULT_REDIRECT)
  })

  it('反斜杠开头的地址被挡住', () => {
    // 部分浏览器把 /\\ 当成 // 处理，同样能跳出站。
    expect(safeRedirect('/\\evil.example')).toBe(DEFAULT_REDIRECT)
  })

  it('可以指定别的兜底页', () => {
    expect(safeRedirect('https://evil.example', '/')).toBe('/')
  })
})
