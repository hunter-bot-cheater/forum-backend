package like

// 本文件集中存放点赞模块对外响应的结构体。
// service 返回 LikeResult（领域结论），这里再转成 likeResp（对外契约），
// 两者分开是为了以后加内部字段时不会顺手泄露给客户端。

// likeResp 是点赞与取消点赞接口 data 的形状。
//
// Count 是操作之后的最新点赞数，客户端应该直接用它刷新数字，
// 不要在前端对旧值加减：并发下加减会和真实值越差越远。
//
// Changed 表示这次调用有没有真的改到数据库，重复点赞、取消一个没点过的赞都是 false，
// 用来让前端提示「已经点过了」。Liked 是操作后的最终状态，决定按钮亮不亮；
// 它和 Changed 不是一回事，重复点赞时 Changed=false 但 Liked=true。
type likeResp struct {
	ArticleID uint  `json:"article_id"`
	Count     int64 `json:"like_count"`
	Changed   bool  `json:"changed"`
	Liked     bool  `json:"liked"`
}
