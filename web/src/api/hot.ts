import request from '@/utils/request'

// 本文件是后端 internal/hot/dto.go 的镜像。
// 热榜只回 id、标题、分数与点赞数，不带作者和时间：
// 榜单回答的是「哪几篇现在最热」，多带字段只会让这一个接口的响应体无谓变大。
// 需要作者信息时点进详情页看，那才是完整的文章。

// HotArticle 是榜单里的一条。
export interface HotArticle {
  // article_id 而不是 id：这个结构体的主语是文章，字段名带上主体更清楚，
  // 也和后端保持一致，避免两边各叫一个名字。
  article_id: number
  // score 是排序依据，等于「点赞 × 3 + 浏览」。
  // 展示「多少人赞过」要用 like_count，不要拿 score 当点赞数显示。
  score: number
  title: string
  like_count: number
  // view_count 是原始浏览数，和 like_count 一样只用来说明分数是怎么来的。
  view_count: number
  // liked 表示当前访问者赞过这一篇没，游客恒为 false。
  // 主语是「我」而不是文章：同一条榜单，登录的人和游客拿到的值不同，
  // 所以列表缓存要跟着身份走（见 store/modules/hot.ts）。
  liked: boolean
}

export interface HotListResp {
  list: HotArticle[]
}

// topArticles 取热榜前 n 名，默认 10 条。
//
// limit 的上限是 100，超过会拿到 400（后端 checkTopN 校验），前端固定传值不给用户输入。
// 空榜时 list 是 []，不是 null，可以直接遍历。
export function topArticles(limit = 10) {
  return request<HotListResp>({
    url: '/articles/hot',
    method: 'get',
    params: { limit },
  })
}
