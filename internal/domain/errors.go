// Package domain 存放跨模块复用的领域错误哨兵。
// 放在独立包里，是为了让 handler（错误映射）和各个业务 service 引用同一份定义，
// 避免「service.ErrNotFound」和「article.ErrNotFound」是两个不同变量导致 errors.Is 失配。
package domain

import "errors"

var (
	// ErrInvalidInput 参数不合法（校验失败、越界等）。
	ErrInvalidInput = errors.New("invalid input")
	// ErrNotFound 资源不存在。
	ErrNotFound = errors.New("not found")
	// ErrUserNameTaken 用户名已被占用。
	ErrUserNameTaken = errors.New("username already taken")
	// ErrInvalidCredentials 用户名或密码错误。
	ErrInvalidCredentials = errors.New("invalid username or password")
	// ErrFloorNotActive 抢楼活动未开始或已结束，或参数不合法。
	ErrFloorNotActive = errors.New("floor event is not active")
	// ErrFloorTaken 这一楼的名额已经被领走了（条件 UPDATE 的 RowsAffected 为 0）。
	ErrFloorTaken = errors.New("floor already taken")
	// ErrSelfAction 对自己做了只允许对别人做的动作，比如关注自己、给自己发私信。
	// 单独立一个哨兵而不是复用 ErrInvalidInput，是为了能给客户端一句准确文案。
	ErrSelfAction = errors.New("cannot act on yourself")
	// ErrDuplicateContent 短时间内提交了与上一篇完全相同的内容。
	// 与 ErrInvalidInput 分开：内容本身完全合法，只是重复提交，
	// 文案要说「刚发过一样的内容」而不是「参数不符合要求」。
	ErrDuplicateContent = errors.New("duplicate content submitted recently")
	// ErrRateLimited 表示某个操作的配额用完了。与中间件的按 IP 限流不是一回事：
	// 这里限制的是「同一个人对同一个昂贵操作的次数」，比如调大模型建议标签。
	ErrRateLimited = errors.New("rate limited")
)
