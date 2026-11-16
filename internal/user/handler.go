// Package user 用户模块：注册、登录、JWT 签发、当前用户资料。
// handler / service / dto 都在这个包里，按模块内聚而不是按技术分层。
package user

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/domain"
	"forum-backend/internal/httputil"
	"forum-backend/pkg/jwt"
	"forum-backend/pkg/response"
)

// UserHandler 是用户模块的 HTTP 入口，只做三件事：
// 解析请求参数 → 调 service → 把结果或错误翻译成响应。
// 真正的业务规则（长度、重名、密码比对）都在 UserService 里，这里不重复判断。
type UserHandler struct {
	svc *UserService
	jwt *jwt.Manager
}

// NewUserHandler 注入用户 service 和 JWT 管理器。
// jwt 只在登录成功后签发 token 时用到，其它接口不需要。
func NewUserHandler(svc *UserService, jwtManager *jwt.Manager) *UserHandler {
	return &UserHandler{svc: svc, jwt: jwtManager}
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
// 校验通过后签发 JWT，token 里只装 userID，前端后续请求放进 Authorization 头。
func (h *UserHandler) Login(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeBadRequest, "请求体格式不正确")
		return
	}

	user, err := h.svc.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		// 用户名不存在与密码错误在 service 里已合并成同一个错误，
		// 这里也只回一句「用户名或密码错误」，不告诉攻击者哪一半猜错了。
		switch {
		case errors.Is(err, domain.ErrInvalidInput):
			response.Error(c, http.StatusBadRequest, "用户名或密码格式不符合要求")
		case errors.Is(err, domain.ErrInvalidCredentials):
			response.Error(c, http.StatusUnauthorized, "用户名或密码错误")
		default:
			httputil.LogRequestError(c, "login", err)
			response.Error(c, http.StatusInternalServerError, "登录失败，请稍后重试")
		}
		return
	}

	// 登录成功才签发 token。签发失败属于服务端问题（密钥或时钟异常），与密码无关。
	token, err := h.jwt.Sign(user.ID)
	if err != nil {
		httputil.LogRequestError(c, "sign access token", err)
		response.Error(c, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	}
	// 用结构体代替 gin.H：字段名写错时编译期就报错，不用等运行时才发现响应少字段。
	resp := loginResp{
		Token: token,
		User:  toUserResp(user),
	}
	response.OK(c, resp)
}

// Detail godoc
// @Summary      用户主页
// @Description  公开信息，不需要登录；未设置昵称时 nickname 回退为用户名。
// @Tags         users
// @Produce      json
// @Param        id   path  int  true  "用户 ID"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=user.userResp}
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      404  {string}  string    "用户不存在"
// @Router       /users/{id} [get]
// Detail 处理 GET /api/users/:id。
// 关注数、粉丝数、文章数要等关注模块落地后再补，这里先只返回资料本体。
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
	response.OK(c, toUserResp(u))
}

// Me godoc
// @Summary      当前登录用户资料
// @Tags         users
// @Produce      json
// @Success      200  {object}  httputil.SwaggerEnvelope{data=user.userResp}
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /users/me [get]
// Me 处理 GET /api/users/me，身份只从 token 取，不接受查询参数指定用户。
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
	response.OK(c, toUserResp(u))
}

// UpdateMe godoc
// @Summary      修改当前用户资料
// @Description  局部更新：没传的字段保持不变，传空字符串表示清空。
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        body body user.updateProfileReq true "昵称、头像、简介，均可选"
// @Success      200  {object}  httputil.SwaggerEnvelope{data=user.userResp}
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
	response.OK(c, toUserResp(u))
}
