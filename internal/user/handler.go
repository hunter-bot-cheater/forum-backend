// Package user 用户模块：注册、登录、JWT 签发、当前用户资料。
// handler / service / repository / DTO 都在这个包里，按模块内聚。
package user

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/domain"
	"forum-backend/internal/httputil"
	"forum-backend/pkg/jwt"
	"forum-backend/pkg/response"
)

// UserHandler 用户相关接口
type UserHandler struct {
	svc *UserService
	jwt *jwt.Manager
}

// NewUserHandler 注入用户 service 和 JWT 管理器。
func NewUserHandler(svc *UserService, jwtManager *jwt.Manager) *UserHandler {
	return &UserHandler{svc: svc, jwt: jwtManager}
}

type registerReq struct {
	Username string `json:"username" binding:"required"` // Gin 内置的参数校验，字段没传或为空就自动报参数错误
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
// Register POST /api/register
func (h *UserHandler) Register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		// 收口 Gin 的原始绑定错误。EOF、字段类型错误这些细节只留在服务端，
		// 客户端拿固定文案，前端按 error_code=bad_request 判断即可。
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeBadRequest, "请求体格式不正确")
		return
	}
	user, err := h.svc.Register(c.Request.Context(), req.Username, req.Password)
	if err != nil {
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
	// 只把 DTO 交给响应包，字段形状由 dto.go 统一定义。
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
// Login POST /api/login
func (h *UserHandler) Login(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		// 收口 Gin 的原始绑定错误。EOF、字段类型错误这些细节只留在服务端，
		// 客户端拿固定文案，前端按 error_code=bad_request 判断即可。
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeBadRequest, "请求体格式不正确")
		return
	}
	user, err := h.svc.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
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
	// 登录成功才签发 token。
	token, err := h.jwt.Sign(user.ID)
	if err != nil {
		httputil.LogRequestError(c, "sign access token", err)
		response.Error(c, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	}
	// 用结构体代替 gin.H，字段写错时编译器会报出来，运行时才发现的拼写错误少一类。
	resp := loginResp{
		Token: token,
		User:  toUserResp(user),
	}
	response.OK(c, resp)
}
