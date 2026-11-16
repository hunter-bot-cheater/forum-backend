package article

import (
	"strings"
	"time"

	"forum-backend/internal/model"
	"forum-backend/internal/user"
)

// 本文件集中存放文章模块对外响应的结构体，以及从 model 到响应结构体的转换函数。
// 单独成文件，是为了让「客户端能看见哪些字段」在目录树里一眼可查，
// 也避免把数据库模型直接交给 response.OK 序列化（那样加一个字段就会意外对外暴露）。
//
// 三个接口各用各的形状，故意不共用一个结构体：
//   - 列表  articleListItem：只有摘要，不含正文
//   - 详情  articleDetailResp：含正文，另加点赞数
//   - 发布  articleResp：含正文，作者刚提交完，回显全文是合理的

// authorBrief 从模型里取作者信息，没 Preload 时返回 nil。
// 没 Preload 时 a.User 是零值、ID 为 0，这时不给 author 字段，
// 而不是返回一个 id=0、username="" 的空作者对象让前端显示成空白名字。
//
// 结构体统一用 user.BriefResp：原来本模块自己声明了一份四字段的 userBrief，
// 评论和私信各有一份完全相同的，三份迟早分叉。
func authorBrief(a *model.Article) *user.BriefResp {
	if a.User.ID == 0 {
		return nil
	}
	return user.NewBriefRespPtr(user.NewBrief(&a.User))
}

// articleResp 是详情与发布接口的正文形状。
// 列表不用它：列表一页 10 篇，正文单篇上限 50000 字节，
// 带上全文会让响应体到几百 KB，客户端在列表里也用不到。
type articleResp struct {
	ID        uint            `json:"id"`
	Title     string          `json:"title"`
	Content   string          `json:"content"`
	Author    *user.BriefResp `json:"author,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	// Tags 是这篇文章挂的标签名，发布与编辑之后前端直接拿它回显。
	Tags []string `json:"tags"`
}

// toArticleResp 把数据库模型转成带正文的对外结构体，转换只发生在 handler 层。
// service 继续只认识 model.Article，不需要反过来 import 这个文件。
func toArticleResp(a *model.Article) articleResp {
	return articleResp{
		ID:        a.ID,
		Title:     a.Title,
		Content:   a.Content,
		Author:    authorBrief(a),
		CreatedAt: a.CreatedAt,
		Tags:      tagsOrEmpty(a.Tags),
	}
}

// tagsOrEmpty 把空标签统一成空切片。
//
// nil 会被序列化成 null，前端就得同时处理 null、[] 和 undefined 三种「没有标签」，
// 而这三种在页面上表现完全一样。统一成 [] 之后只有一个分支。
func tagsOrEmpty(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

// articleExcerptLimit 是列表摘要保留的字符数（按 rune 算，不是字节）。
// 一百多字在列表里大约两三行，够读者判断要不要点进去，又不会把列表撑长。
const articleExcerptLimit = 160

// articleExcerptSourceLimit 是 service 从 MySQL 里取回的正文字符数上限，
// 必须大于 articleExcerptLimit：压掉换行和连续空白之后长度还会变短，
// 多取一些才能保证截出来的摘要足够长。SQL 里截断只为了少搬数据，
// 真正决定展示多长的是 articleExcerptLimit。
const articleExcerptSourceLimit = 400

// articleListItem 是文章列表里的一篇，刻意不含 content 全文。
// Excerpt 是服务端截好的摘要，客户端不需要自己去截正文。
type articleListItem struct {
	ID        uint            `json:"id"`
	Title     string          `json:"title"`
	Excerpt   string          `json:"excerpt"`
	Author    *user.BriefResp `json:"author,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	// ViewCount 给列表卡片上那行小字用。它是详情缓存之外的一份库内值，
	// 可能比详情页刚显示的数字略旧（详情页会补上本次访问的一次），
	// 但列表本来就不需要精确到个位。
	ViewCount int64 `json:"view_count"`
	// Tags 是这篇文章挂的标签名，按标签筛选时前端靠它高亮当前标签，
	// 也让卡片上能显示「这篇属于什么」。
	Tags []string `json:"tags"`
}

// toArticleListItem 把数据库模型转成列表项。
// a.Content 在列表查询里根本没查（service 只取了摘要），所以这里不能读它。
func toArticleListItem(a *model.Article) articleListItem {
	return articleListItem{
		ID:        a.ID,
		Title:     a.Title,
		Excerpt:   excerptOf(a.Excerpt),
		Author:    authorBrief(a),
		CreatedAt: a.CreatedAt,
		ViewCount: a.ViewCount,
		Tags:      tagsOrEmpty(a.Tags),
	}
}

// excerptOf 把正文压成一行摘要。
//
// 先把所有空白（换行、制表、连续空格）压成单个空格，列表里一行显示时
// 才不会出现大段空行；再按 rune 截断，避免把中文汉字截成半个导致乱码。
// 截断时补一个省略号，让读者知道后面还有正文。
func excerptOf(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")

	runes := []rune(collapsed)
	if len(runes) <= articleExcerptLimit {
		return collapsed
	}
	return string(runes[:articleExcerptLimit]) + "…"
}

// articleListResp 是文章列表接口 data 的形状。
// 字段写错由编译器拦截，别在响应里用 gin.H 拼字符串键。
//
// 四个分页字段与其它列表接口长得一样，但没有抽成泛型结构体：swag 解析
// 匿名嵌入的泛型实例会失败，接口文档会变成空壳，详见 internal/paging 的说明。
type articleListResp struct {
	// List 声明成切片而不是指针，且调用方保证给它空切片而非 nil，
	// 这样没有数据时序列化出来是 []，不会变成 null。
	List     []articleListItem `json:"list"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// createArticleReq 是发布与编辑共用的请求体。
// 两个接口能改的字段完全一样，各写一份只会让绑定与校验慢慢分叉。
// 字段里刻意没有 UserID：作者 id 只从 token 取，
// 客户端传 user_id 会被 encoding/json 忽略，伪造不了作者。
type createArticleReq struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	// Tags 是标签名，最多 3 个、每个最长 32 字，具体校验在 service 的 normalizeTags。
	// 用名字而不是 tag id：前端发帖时标签可能是当场新建的，还没有 id。
	Tags []string `json:"tags"`
}

// articleDetailResp 是详情接口专用的响应，比列表多点赞数与浏览数。
// 匿名嵌入 articleResp，encoding/json 会把里面的字段平铺到同一层，
// 输出仍是 id、title、content、author、created_at，再加 like_count 与 view_count。
// 发布接口继续用 articleResp，不带这两个字段。
type articleDetailResp struct {
	articleResp
	LikeCount int64 `json:"like_count"`

	// ViewCount 是库里的浏览数。缓存里也存了一份，但 handler 会用
	// ViewCount() 查到的实时值覆盖它（见 Detail），因为缓存里的值只是
	// 回填那一刻的快照，展示出来会偏旧甚至变小。
	ViewCount int64 `json:"view_count"`

	// Liked 是「当前访问者点过没」，决定详情页的点赞按钮要不要点亮。
	//
	// 它只取决于发请求的人，所以不跟着文章进缓存——缓存键里没有用户维度，
	// 存进去就会让 A 点过赞之后 B 也看到高亮的按钮。
	// 游客（没登录或 token 过期）恒为 false：没有身份就无从判断。
	Liked bool `json:"liked"`
}

// toArticleDetailResp 把详情模型转成对外结构体。
// LikeCount 由 queryDetail 在回源时算好，随文章一起写进缓存，命中缓存时直接带出来。
func toArticleDetailResp(a *model.Article) articleDetailResp {
	return articleDetailResp{
		articleResp: toArticleResp(a),
		LikeCount:   a.LikeCount,
		ViewCount:   a.ViewCount,
	}
}
