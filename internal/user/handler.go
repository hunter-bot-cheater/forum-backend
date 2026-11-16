// Package user 用户模块：注册、登录、JWT 签发、当前用户资料。
// handler / service / dto 都在这个包里，按模块内聚而不是按技术分层。
package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/domain"
	"forum-backend/internal/httputil"
	"forum-backend/internal/model"
	"forum-backend/internal/session"
	"forum-backend/pkg/response"
)

// FollowStats 是个人主页要用的关注统计。
//
// 接口声明在使用方（这里），实现放在 follow 包：
// follow 已经依赖 user.Briefs，如果 user 反过来 import follow 就成环了。
// 方法签名只用基本类型，正是为了让两个包不共享结构体也能对上。
type FollowStats interface {
	Stats(ctx context.Context, userID, viewerID uint) (following, followers int64, isFollowing bool, err error)
}

// UserHandler 是用户模块的 HTTP 入口，只做三件事：
// 解析请求参数 → 调 service → 把结果或错误翻译成响应。
// 真正的业务规则（长度、重名、密码比对）都在 UserService 里，这里不重复判断。
type UserHandler struct {
	svc     *UserService
	session *session.Service
	guard   *LoginGuard
	follow  FollowStats
}

// NewUserHandler 注入用户 service、登录态管理、失败锁定与关注统计。
// 不注入 jwt.Manager：token 的签发与校验都由 session 包负责，
// 校验走中间件、签发走 session，handler 拿不到 Manager 也就不会有人绕过登记直接签一个。
// guard 允许为 nil：单元测试里不需要 Redis，这里判空跳过，生产装配时一定传。
func NewUserHandler(svc *UserService, sessionSvc *session.Service, guard *LoginGuard, followStats FollowStats) *UserHandler {
	return &UserHandler{svc: svc, session: sessionSvc, guard: guard, follow: followStats}
}

// registerReq 是注册与登录共用的请求体。两个接口要的字段完全一样，就共用一个结构体。
// binding 标签交给 Gin 做最基础的校验：字段缺失或空字符串时 ShouldBindJSON 直接返回错误。
// 长度和字符集的细化校验在 service 里做，两处职责不重叠。
type registerReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Register godoc
// @Summary      用户注册
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        body body user.registerReq true "用户名与密码"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=user.userResp}
// @Failure      400  {string}  string    "请求体格式不正确"
// @Failure      409  {string}  string    "用户名已存在"
// @Router       /register [post]
// Register 处理 POST /api/register。
// 成功后只返回用户资料，不自动登录；前端要再调一次 /login 才拿到 token。
func (h *UserHandler) Register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		// 收口 Gin 的原始绑定错误：EOF、字段类型不匹配这些细节只留在服务端，
		// 客户端拿固定文案，前端按 error_code=bad_request 判断即可。
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeBadRequest, "请求体格式不正确")
		return
	}

	user, err := h.svc.Register(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		// 按错误类型分别映射成 400 / 409 / 500。
		// 只有最后一个分支才记日志：前两种是用户输入问题，属于预期内。
		switch {
		case errors.Is(err, domain.ErrInvalidInput):
			response.Error(c, http.StatusBadRequest, "用户名或密码格式不符合要求")
		case errors.Is(err, domain.ErrUserNameTaken):
			response.Error(c, http.StatusConflict, "用户名已被使用")
		default:
			httputil.LogRequestError(c, "register", err)
			// 详细数据库错误留在服务端日志，客户端只收到通用提示。
			response.Error(c, http.StatusInternalServerError, "注册失败，请稍后重试")
		}
		return
	}
	// 交给 DTO 转一次再返回：数据库模型的字段不会直接暴露给客户端。
	response.OK(c, toUserResp(user))
}

// Login godoc
// @Summary      用户登录
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        body body user.registerReq true "用户名与密码"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=user.loginResp}
// @Failure      401  {string}  string    "凭据错误"
// @Router       /login [post]
// Login 处理 POST /api/login。
// 校验通过后签发一对 token：access 15 分钟随请求发送，refresh 7 天只在刷新接口用。
func (h *UserHandler) Login(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeBadRequest, "请求体格式不正确")
		return
	}

	// 先看这个用户名是不是在锁定中。
	// Redis 故障时选择放行（fail open）：锁定是「减少被猜中的机会」，不是身份校验的一环，
	// 它挂了不该让所有人都登不进来；IP 维度的限流仍在，不至于完全裸奔。
	if h.guard != nil {
		locked, retryAfter, err := h.guard.Check(c.Request.Context(), req.Username)
		if err != nil {
			httputil.LogRequestError(c, "check login lock", err)
		} else if locked {
			// Retry-After 给秒数，前端可以直接显示「还有几分钟」。
			seconds := int(retryAfter.Seconds() + 0.5)
			c.Header("Retry-After", strconv.Itoa(seconds))
			minutes := seconds / 60
			if minutes < 1 {
				minutes = 1
			}
			response.ErrorWithCode(c, http.StatusTooManyRequests, response.ErrCodeRateLimited,
				fmt.Sprintf("失败次数过多，请 %d 分钟后再试", minutes))
			return
		}
	}

	user, err := h.svc.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		// 用户名不存在与密码错误在 service 里已合并成同一个错误，
		// 这里也只回一句「用户名或密码错误」，不告诉攻击者哪一半猜错了。
		switch {
		case errors.Is(err, domain.ErrInvalidInput):
			response.Error(c, http.StatusBadRequest, "用户名或密码格式不符合要求")
		case errors.Is(err, domain.ErrInvalidCredentials):
			// 只对「凭据错误」计数。格式不合法与数据库故障都不算一次猜测：
			// 前者是用户手滑，后者会把一次基础设施抖动变成一堆被锁的账号。
			h.recordLoginFailure(c, req.Username)
			response.Error(c, http.StatusUnauthorized, "用户名或密码错误")
		default:
			httputil.LogRequestError(c, "login", err)
			response.Error(c, http.StatusInternalServerError, "登录失败，请稍后重试")
		}
		return
	}

	// 登录成功要清零：不清的话，白天错四次、晚上再错一次就会被锁，
	// 而中间他明明成功过一次，用户完全无法理解这个「突然被锁」。
	if h.guard != nil {
		if err := h.guard.Reset(c.Request.Context(), req.Username); err != nil {
			// 清零失败不影响登录本身，最坏结果是这个人的失败计数继续累积。
			httputil.LogRequestError(c, "reset login failures", err)
		}
	}

	// 登录成功才签发 token。签发要同时登记 refresh 的 jti，
	// 任何一步失败都属于服务端问题（Redis 不可用、密钥或时钟异常），与密码无关。
	tokens, err := h.session.Issue(c.Request.Context(), user.ID)
	if err != nil {
		httputil.LogRequestError(c, "issue tokens", err)
		response.Error(c, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	}
	// 用结构体代替 gin.H：字段名写错时编译期就报错，不用等运行时才发现响应少字段。
	resp := loginResp{
		Tokens: tokens,
		User:   toUserResp(user),
	}
	response.OK(c, resp)
}

// recordLoginFailure 记一次凭据失败。
//
// 刚好触发锁定的那一次仍然回 401，不改成 429：
// 让攻击者从状态码变化里看不出「这是第几次」，下一次请求才会收到明确的锁定提示。
// 用户体验上也没有损失——第 6 次尝试时才被拦住，说明前 5 次都没成功。
func (h *UserHandler) recordLoginFailure(c *gin.Context, username string) {
	if h.guard == nil {
		return
	}
	if _, _, err := h.guard.Fail(c.Request.Context(), username); err != nil {
		// 计数失败不影响本次 401 响应：密码本来就是错的，用户该看到的就是这句。
		httputil.LogRequestError(c, "record login failure", err)
	}
}

// Refresh godoc
// @Summary      用 refresh token 换一对新 token
// @Description  旧的 refresh 用一次即作废；检测到重放会撤销整条刷新链，需要重新登录
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        body body user.refreshReq true "refresh_token"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=session.Tokens}
// @Failure      401  {string}  string    "refresh 无效、已用过或已过期"
// @Failure      503  {string}  string    "登记表不可用，暂时无法刷新"
// @Router       /auth/refresh [post]
// Refresh 处理 POST /api/auth/refresh。
//
// 这是唯一接受 refresh 的入口，所以它自己不能挂 JWTAuth：
// 调用它的那一刻 access 往往已经过期了，用 access 做前置校验会让刷新永远失败。
// 身份完全由 body 里的 refresh 证明。
func (h *UserHandler) Refresh(c *gin.Context) {
	var req refreshReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeBadRequest, "请求体格式不正确")
		return
	}

	tokens, err := h.session.Refresh(c.Request.Context(), req.RefreshToken)
	switch {
	case errors.Is(err, session.ErrRefreshReused):
		// 重放意味着这串 refresh 被复制过。日志里不带 token 本身，只记事件。
		slog.WarnContext(c.Request.Context(), "refresh token reused, family revoked")
		response.ErrorWithCode(c, http.StatusUnauthorized, response.ErrCodeTokenInvalid, "登录状态已失效，请重新登录")
	case errors.Is(err, session.ErrInvalidRefresh):
		response.ErrorWithCode(c, http.StatusUnauthorized, response.ErrCodeTokenInvalid, "登录已过期，请重新登录")
	case err != nil:
		// Redis 不可用。这里必须明确失败：放行等于绕过重放检测。
		httputil.LogRequestError(c, "refresh tokens", err)
		response.Error(c, http.StatusServiceUnavailable, "登录服务暂时不可用，请稍后重试")
	default:
		response.OK(c, tokens)
	}
}

// Logout godoc
// @Summary      退出登录
// @Description  撤销当前 refresh；access 会自然过期，不单独维护黑名单
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        body body user.refreshReq true "refresh_token"
// @Success      200  {string}  string    "已退出"
// @Router       /auth/logout [post]
// Logout 处理 POST /api/auth/logout。
//
// 幂等：token 无效、已撤销、格式不对都返回成功。
// 登出的语义是「让这串凭证以后不能用」，本来就不能用的时候，这个目的已经达到了；
// 回 401 只会让前端在退出时还要处理一次错误。
func (h *UserHandler) Logout(c *gin.Context) {
	var req refreshReq
	// 绑定失败也照样回成功，理由同上：前端只关心退出这件事有没有完成。
	if err := c.ShouldBindJSON(&req); err == nil {
		if err := h.session.Revoke(c.Request.Context(), req.RefreshToken); err != nil {
			// Redis 出错时撤销没有落地，但客户端已经清掉本地状态。
			// 记日志留证据，仍然回成功：这条 refresh 会在 TTL 到期后自然失效。
			httputil.LogRequestError(c, "revoke refresh", err)
		}
	}
	response.OK(c, logoutResp{Message: "已退出"})
}

// Detail godoc
// @Summary      用户主页
// @Description  公开信息，不需要登录；未设置昵称时 nickname 回退为用户名。
// @Tags         users
// @Produce      json
// @Param        id   path  int  true  "用户 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=user.profileResp}
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      404  {string}  string    "用户不存在"
// @Router       /users/{id} [get]
// Detail 处理 GET /api/users/:id，返回资料本体加关注统计。
// 带 token 时多算一个 is_following，不带 token 也能看，只是这一项固定 false。
func (h *UserHandler) Detail(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	u, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		httputil.MapServiceError(c, "user detail", err)
		return
	}

	// 主页是公开接口，带 token 时把访问者身份传下去，用来算 is_following；
	// 没带 token 就是 0，统计照常返回，只有这一项是 false。
	var viewerID uint
	if current, ok := httputil.CurrentUserID(c); ok {
		viewerID = current
	}

	profile, err := h.buildProfile(c, u, viewerID)
	if err != nil {
		httputil.MapServiceError(c, "user detail stats", err)
		return
	}
	response.OK(c, profile)
}

// buildProfile 组装个人主页响应：资料本体加关注统计。
// 抽成方法是因为公开主页与 /users/me 用的是同一份形状，区别只有 viewerID 是谁。
func (h *UserHandler) buildProfile(c *gin.Context, u *model.User, viewerID uint) (profileResp, error) {
	following, followers, isFollowing, err := h.follow.Stats(c.Request.Context(), u.ID, viewerID)
	if err != nil {
		return profileResp{}, err
	}
	return toProfileResp(u, following, followers, isFollowing), nil
}

// Me godoc
// @Summary      当前登录用户资料
// @Tags         users
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=user.profileResp}
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /users/me [get]
// Me 处理 GET /api/users/me，身份只从 token 取，不接受查询参数指定用户。
// 返回形状与公开主页完全一致，前端个人中心只写一套渲染。
func (h *UserHandler) Me(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	u, err := h.svc.GetByID(c.Request.Context(), userID)
	if err != nil {
		httputil.MapServiceError(c, "current user", err)
		return
	}

	// 看自己的主页时 viewer 就是本人，is_following 恒为 false（不能关注自己）。
	profile, err := h.buildProfile(c, u, userID)
	if err != nil {
		httputil.MapServiceError(c, "current user stats", err)
		return
	}
	response.OK(c, profile)
}

// UpdateMe godoc
// @Summary      修改当前用户资料
// @Description  局部更新：没传的字段保持不变，传空字符串表示清空。
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        body body user.updateProfileReq true "昵称、头像、简介，均可选"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=user.profileResp}
// @Failure      400  {string}  string    "字段不合法"
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /users/me [put]
// UpdateMe 处理 PUT /api/users/me，返回更新后的完整资料。
func (h *UserHandler) UpdateMe(c *gin.Context) {
	userID, ok := httputil.CurrentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 绑定失败通常是字段类型不对（比如 nickname 传了数字），
	// 统一回固定文案，具体的 JSON 解析错误只进服务端日志。
	var req updateProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeBadRequest, "请求体格式不正确")
		return
	}

	u, err := h.svc.UpdateProfile(c.Request.Context(), userID, toProfilePatch(req))
	if err != nil {
		httputil.MapServiceError(c, "update profile", err)
		return
	}

	// 返回形状必须和 GET /users/me 一模一样。
	// 只回 toUserResp 的话，前端「保存后用返回值替换本地状态」会把
	// following_count、follower_count 覆盖成 0，主页上的关注数凭空消失。
	// 看自己时 viewer 就是本人，is_following 恒为 false。
	profile, err := h.buildProfile(c, u, userID)
	if err != nil {
		httputil.MapServiceError(c, "update profile stats", err)
		return
	}
	response.OK(c, profile)
}
