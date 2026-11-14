package hot

// 本文件集中存放热榜模块对外响应的结构体，以及从 service 结果到响应结构体的转换函数。

// HotArticleResp 是热榜里一条记录的对外形状。
type HotArticleResp struct {
	ArticleID uint    `json:"article_id"`
	Score     float64 `json:"score"`
	Title     string  `json:"title"`
	LikeCount int64   `json:"like_count"`
}

// HotListResp 让响应始终带一个 list 字段，空榜输出 []。
type HotListResp struct {
	List []HotArticleResp `json:"list"`
}

// ToHotArticleResp 把 service 的结果转成对外结构体，转换留在 module 内。
func ToHotArticleResp(item HotArticle) HotArticleResp {
	return HotArticleResp{
		ArticleID: item.ArticleID,
		Score:     item.Score,
		Title:     item.Title,
		LikeCount: item.LikeCount,
	}
}
