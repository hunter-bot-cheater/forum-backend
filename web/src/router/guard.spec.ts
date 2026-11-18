import { describe, expect, it } from 'vitest'
import { guardTarget } from './guard'

// route 造一个守卫的输入。meta 只带 public，因为 guardTarget 只读这一个字段。
function route(name: string, isPublic: boolean, fullPath = '/articles') {
  return { name, meta: { public: isPublic }, fullPath }
}

describe('路由守卫', () => {
  describe('游客', () => {
    it('访问公开页直接放行', () => {
      // 文章列表、热榜、用户主页、文章详情都是公开页，
      // 后端对应接口也都在公开组里，两边口径必须一致。
      const publics = ['ArticleList', 'Hot', 'UserProfile', 'ArticleDetail']
      for (const name of publics) {
        expect(guardTarget(route(name, true), '')).toBe(true)
      }
    })

    it('访问私有页跳登录并带上原地址', () => {
      expect(guardTarget(route('Messages', false, '/messages/12'), '')).toEqual({
        path: '/login',
        query: { redirect: '/messages/12' },
      })
    })

    it('redirect 保留查询串', () => {
      // 用 fullPath 而不是 path：通知里的 ?comment= 这类锚点在登录后还得生效，
      // 只存 path 会把它丢掉，登录完落在详情页顶部而不是那条评论。
      expect(guardTarget(route('ArticleEdit', false, '/articles/7/edit?from=admin'), '')).toEqual({
        path: '/login',
        query: { redirect: '/articles/7/edit?from=admin' },
      })
    })
  })

  describe('已登录', () => {
    it('访问登录页与注册页时回文章列表', () => {
      expect(guardTarget(route('Login', true), 'token-abc')).toBe('/articles')
      expect(guardTarget(route('Register', true), 'token-abc')).toBe('/articles')
    })

    it('访问公开页照样放行', () => {
      // 回归用例。守则一旦写成「已登录访问任何 public 页就回列表」，
      // 登录用户点开任意一篇文章都会被弹回列表，这条会红。
      expect(guardTarget(route('ArticleDetail', true, '/articles/7'), 'token-abc')).toBe(true)
      expect(guardTarget(route('Hot', true, '/hot'), 'token-abc')).toBe(true)
    })

    it('访问私有页放行', () => {
      expect(guardTarget(route('Messages', false, '/messages'), 'token-abc')).toBe(true)
    })
  })
})
