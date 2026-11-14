package like

// 本文件集中存放点赞模块对外响应的结构体。

// likeResp 是点赞与取消点赞接口 data 的形状。
type likeResp struct {
	ArticleID uint  `json:"article_id"`
	Count     int64 `json:"like_count"`
	Changed   bool  `json:"changed"`
	Liked     bool  `json:"liked"`
}
