import request from '@/utils/request'
import type { Brief } from '@/api/message'

// 本文件是后端 internal/article/dto.go 的镜像，字段名保持 snake_case。
// 与通知、私信共用 Brief：作者、actor、peer 都是同一份「展示一个用户」的结构，
// 各写一份迟早会出现一边加了头像、另一边还是旧字段。

// ArticleListItem 是文章列表里的一篇。
//
// 刻意没有 content：列表一页十篇，正文单篇上限 50000 字节，
// 带上全文会让响应体涨到几百 KB，而列表里只需要看摘要。
// excerpt 是服务端截好的（压过换行、按字符截断并补省略号），前端直接渲染，
// 不要拿 title 或别的字段自己再截一遍——两处截断长度迟早不一致。
export interface ArticleListItem {
  id: number
  title: string
  excerpt: string
  // author 可能缺失：后端在没预加载到作者时省掉整个字段（omitempty）。
  // 正常列表都有，缺了就当「匿名」显示，不要让整行变成空白。
  author?: Brief
  created_at: string
  // view_count 是库里的浏览数。列表上可能比详情页刚看到的少一点点
  // （详情页每次都会读最新值，列表取的是这一页查询那一刻的值），
  // 展示层面不需要精确到个位。
  view_count: number
  // tags 是标签名，没有标签时是空数组而不是 null：
  // 后端统一成 []，这里可以直接遍历，不用再判空。
  tags: string[]
}

export interface ArticleListResp {
  list: ArticleListItem[]
  total: number
  page: number
  page_size: number
}

// ArticleResp 是「带正文的一篇文章」，发布接口的返回就是它。
//
// 刚发布出来的文章点赞数必然是 0，作者本人也谈不上「点过赞」，
// 所以后端发布接口只回这个形状，不带点赞字段。
export interface ArticleResp {
  id: number
  title: string
  content: string
  author?: Brief
  created_at: string
  // tags 与列表同款：发布/编辑成功后前端直接拿它回显，不用再拉一次详情。
  tags: string[]
}

// ArticleDetailResp 是详情接口的形状：在 ArticleResp 之上多两个点赞字段。
//
// 用 extends 而不是把字段再抄一遍：以后给文章加字段（比如编辑时间），
// 只要改 ArticleResp 一处，详情不会漏。
//
// liked 是「当前登录用户点过没」，服务端按 token 算好再回填，前端不要自己维护。
// 游客恒为 false（路由守卫要求登录，实际上只有 token 过期的瞬间会以游客身份请求）。
export interface ArticleDetailResp extends ArticleResp {
  like_count: number
  // view_count 是服务端在本次访问之后读到的浏览数：打开这篇就计入一次
  // （同一访问者 30 分钟内只算一次），所以它包含你自己这次。
  view_count: number
  liked: boolean
}

// LikeResp 是点赞与取消点赞的返回。
// count 是操作后的真实点赞数，前端直接用它刷新数字，不要对旧值加减：
// 并发下加减会越差越远。
// changed 表示这次有没有真的改到数据，重复点赞、取消一个没点过的赞都是 false。
export interface LikeResp {
  article_id: number
  like_count: number
  changed: boolean
  liked: boolean
}

// listArticles 拉一页文章，page 从 1 开始。
//
// authorId 可选：传了就只看这个人的文章（用户主页的「文章」标签），不传是全站列表。
// keyword 可选：后端同时匹配标题与正文，不区分大小写。
// tag 可选：按标签名精确筛选，与 keyword 可以同时用。
// page_size 默认取 10（与后端一致），上限 50 由后端校验：
// 前端不重复判，传超了会拿到 400，拦截器会把提示弹出来。
// 空结果时 list 是 []，不是 null，可以直接遍历。
export function listArticles(page = 1, pageSize = 10, authorId = 0, keyword = '', tag = '') {
  const params: Record<string, number | string> = { page, page_size: pageSize }
  // 只在有作者时才带这个参数：后端对空串会当 400，
  // 而传 author_id=0 虽然等价于全站，但请求 URL 里多一个无意义参数，
  // 排查问题时容易误以为「按作者 0 过滤了」。
  if (authorId > 0) {
    params.author_id = authorId
  }
  // 空关键词不发：后端会把空白当成「没搜」，但 URL 里挂一个 keyword=
  // 会让日志和排查的人以为这次请求带了搜索条件。
  const kw = keyword.trim()
  if (kw !== '') {
    params.keyword = kw
  }
  const tagName = tag.trim()
  if (tagName !== '') {
    params.tag = tagName
  }
  return request<ArticleListResp>({
    url: '/articles',
    method: 'get',
    params,
  })
}

// TagCount 是标签云里的一项：标签名与它下面的文章数。
export interface TagCount {
  name: string
  count: number
}

// listTags 拉热门标签（后端按文章数倒序，最多 20 个）。
// 标签云不需要登录，游客也能看到有哪些标签可以点。
export function listTags() {
  return request<{ list: TagCount[] }>({
    url: '/tags',
    method: 'get',
  })
}

// getArticle 取一篇文章的全文与点赞状态。
// 详情服务端有缓存，命中时不查库，正常浏览不会给数据库压力。
export function getArticle(id: number) {
  return request<ArticleDetailResp>({
    url: '/articles/' + String(id),
    method: 'get',
  })
}

// createArticle 发布一篇文章，成功后返回落库后的完整记录（含服务端生成的 id）。
//
// 作者身份不在这里传：后端只认 token 里的 user_id，
// 请求体里多塞一个 user_id 会被忽略，前端也不必留这个参数。
// tags 是标签名数组，后端会去空白、去重并限制最多 3 个；
// 前端先做一次同样的上限校验只是为了即时提示，最终判定仍在后端。
// 标题与正文的合法性由后端最终判定，前端只做「明显不可能通过」的预校验。
export function createArticle(title: string, content: string, tags: string[]) {
  return request<ArticleResp>({
    url: '/articles',
    method: 'post',
    data: { title, content, tags },
  })
}

// updateArticle 编辑一篇文章，返回编辑后的详情（含点赞数），前端可直接替换本地状态。
//
// 只允许作者本人：不是作者会拿到 404，后端不区分「不存在」和「不是你的」，
// 所以前端不要指望用这个接口探测文章归属。
// 标题与正文都是全量提交，没有「只改一项」的用法；
// tags 同样是全量替换，传空数组就是把这篇文章的标签全部去掉。
export function updateArticle(id: number, title: string, content: string, tags: string[]) {
  return request<ArticleDetailResp>({
    url: '/articles/' + String(id),
    method: 'put',
    data: { title, content, tags },
  })
}

// deleteArticle 删除文章，仅作者本人可删。
//
// 「是不是作者」由后端在 WHERE 里判断，前端拿不到也不该拿这个决定权：
// 别人删别人的会拿到 404（后端不区分「不存在」和「不是你的」，避免探测）。
export function deleteArticle(id: number) {
  return request<null>({
    url: '/articles/' + String(id),
    method: 'delete',
  })
}

// likeArticle 点赞。重复点赞返回 200 且 changed=false，调用方按 liked 刷新按钮即可。
export function likeArticle(id: number) {
  return request<LikeResp>({
    url: '/articles/' + String(id) + '/like',
    method: 'put',
  })
}

// unlikeArticle 取消点赞。取消一个没点过的赞同样返回 200，接口是幂等的。
export function unlikeArticle(id: number) {
  return request<LikeResp>({
    url: '/articles/' + String(id) + '/like',
    method: 'delete',
  })
}
