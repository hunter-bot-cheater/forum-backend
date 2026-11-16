package user

// BriefResp 是对外暴露的用户简介，字段与 Brief 一一对应，只是多了 json tag。
//
// 为什么不让 Brief 自己带 tag：Brief 是包内传递的值类型，
// 带上 tag 就等于把「对前端的传输格式」绑死在内部类型上，
// 以后改内部字段会顺带改掉接口契约，而这层改动很难在 review 时看出来。
//
// 文章、评论、私信三个模块原本各自声明了一份同样的四字段结构体，
// 加起来是第三份重复。口径一旦要变（比如以后加「是否已关注」）
// 就得同时改三处，漏一处只有前端能发现，所以收到这里。
type BriefResp struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatar_url"`
}

// NewBriefResp 把 Brief 转成对外结构体。
//
// Nickname 取的是 DisplayName 而不是原始 Nickname：
// 列表上永远要显示一个能渲染的名字，回退规则只在这里生效一次，
// 前端拿到就画，不需要判断空字符串。
func NewBriefResp(b Brief) BriefResp {
	return BriefResp{
		ID:        b.ID,
		Username:  b.Username,
		Nickname:  b.DisplayName(),
		AvatarURL: b.AvatarURL,
	}
}

// NewBriefRespPtr 供「作者可能缺失」的位置使用，零值 Brief 返回 nil。
//
// 返回指针是为了让调用方的 DTO 能用 omitempty 直接省掉整个字段，
// 而不是抛出一个 id=0、nickname="" 的空对象让前端显示成空白名字。
// 判据用 ID 是否为 0：Brief 里只要 ID 有值，其余字段就有意义。
func NewBriefRespPtr(b Brief) *BriefResp {
	if b.ID == 0 {
		return nil
	}
	resp := NewBriefResp(b)
	return &resp
}
