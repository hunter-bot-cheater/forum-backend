package hot

// 本文件集中存放热榜模块对外响应的结构体，以及从 service 结果到响应结构体的转换函数。
// 结构体是导出的大写名字，因为 handler 在 like 模块里，需要跨包构造这些类型。

// HotArticleResp 是热榜里一条记录的对外形状。
//
// Score 是榜单排序依据，当前等于点赞数；以后换成加权热度算法时它会和 LikeCount 分叉，
// 所以两个字段都保留：Score 用来解释排名，LikeCount 用来展示「多少人赞过」。
type HotArticleResp struct {
	ArticleID uint    `json:"article_id"`
	Score     float64 `json:"score"`
	Title     string  `json:"title"`
	LikeCount int64   `json:"like_count"`
}

// HotListResp 让响应始终带一个 list 字段，空榜输出 []。
// 不直接返回数组：以后加 total、更新时间这类字段时不用改响应外层结构。
type HotListResp struct {
	List []HotArticleResp `json:"list"`
}

// ToHotArticleResp 把 service 的结果转成对外结构体，转换留在 module 内。
// 导出是因为 handler 在 like 模块，跨包调用必须大写。
func ToHotArticleResp(item HotArticle) HotArticleResp {
	return HotArticleResp{
		ArticleID: item.ArticleID,
		Score:     item.Score,
		Title:     item.Title,
		LikeCount: item.LikeCount,
	}
}
