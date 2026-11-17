package upload

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"forum-backend/pkg/response"
)

// multipartOverhead 是 multipart 表单本身的开销：分隔符、字段名、结尾。
// 请求体上限要比文件上限多留这么多，否则「正好 5 MB 的文件」会被请求体限制拦掉，
// 用户看到的是「文件太大」，但他选的图其实在上限之内。
const multipartOverhead = 64 << 10

// Handler 是上传模块的 HTTP 入口。
type Handler struct {
	svc *Service
}

// NewHandler 注入上传 service，由 router 在装配期调用。
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// BodyLimit 是路由层应当设置的请求体上限，比文件上限多留 multipart 的开销。
func (h *Handler) BodyLimit() int64 {
	return h.svc.MaxBytes() + multipartOverhead
}

// Avatar godoc
// @Summary      上传头像
// @Description  接收一张图片，服务端按 EXIF 摆正、缩到最长边 256px 并重编码后存盘。
//
//	返回的 url 直接写进 PUT /users/me 的 avatar_url。同一张图重复上传返回同一地址。
//
// @Tags         uploads
// @Accept       multipart/form-data
// @Produce      json
// @Param        file  formData  file  true  "图片文件，支持 jpg/png/gif/webp，不超过 5 MB"
// @Success      200   {object}  httputil.SwaggerEnvelope{data=Saved}
// @Failure      400   {string}  string  "空的、不是图片、尺寸过大或过小"
// @Failure      413   {string}  string  "文件超过大小上限"
// @Failure      415   {string}  string  "不支持的图片格式"
// @Router       /uploads/avatar [post]
// @Security     Bearer
// Avatar 处理 POST /api/uploads/avatar。
//
// 只收 multipart 的 file 字段，不接受 base64：
// base64 会把体积撑大三分之一，而且要在 JSON 里先拼出一个大字符串再解析，
// 内存峰值翻倍，对 2 GB 的机器不友好。
func (h *Handler) Avatar(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		// 请求体超限时 FormFile 报的是「读 body 失败」，不是「没有 file 字段」。
		// 不区分的话，用户传一张 7 MB 的照片会看到「没有收到文件」，
		// 而文件明明选好了——他会以为是按钮或浏览器的问题，反复重试。
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			response.ErrorWithCode(c, http.StatusRequestEntityTooLarge, response.ErrCodeInvalidInput,
				fmt.Sprintf("图片不能超过 %d MB", h.svc.MaxBytes()>>20))
			return
		}
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeInvalidInput, "没有收到文件，请选择一张图片")
		return
	}
	// 先按声明的大小拒一次：能省掉把超限文件完整读进内存的过程。
	// 这个值来自客户端，可以撒谎，所以下面真正的上限仍然是路由上的 MaxBytesReader。
	if file.Size > h.svc.MaxBytes() {
		response.ErrorWithCode(c, http.StatusRequestEntityTooLarge, response.ErrCodeInvalidInput,
			fmt.Sprintf("图片不能超过 %d MB", h.svc.MaxBytes()>>20))
		return
	}

	f, err := file.Open()
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "open uploaded file", "error", err)
		response.Error(c, http.StatusInternalServerError, "文件读取失败，请重试")
		return
	}
	defer f.Close()

	saved, err := h.svc.SaveAvatar(f)
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, saved)
}

// fail 把 service 的哨兵错误翻成 HTTP 语义。
//
// 400 与 413、415 分开是有意义的：前端要据此决定提示「换一张图」
// 还是「这张图太大，先压一下」，笼统地回 400 让前端只能猜。
func (h *Handler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrEmpty):
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeInvalidInput, "没有收到文件，请选择一张图片")
	case errors.Is(err, ErrTooLarge):
		response.ErrorWithCode(c, http.StatusRequestEntityTooLarge, response.ErrCodeInvalidInput,
			fmt.Sprintf("图片不能超过 %d MB", h.svc.MaxBytes()>>20))
	case errors.Is(err, ErrTooLargeImg):
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeInvalidInput, "图片像素太多了，请先缩小再上传")
	case errors.Is(err, ErrTooSmallImg):
		response.ErrorWithCode(c, http.StatusBadRequest, response.ErrCodeInvalidInput, "图片太小了，换一张吧")
	case errors.Is(err, ErrUnsupported):
		response.ErrorWithCode(c, http.StatusUnsupportedMediaType, response.ErrCodeInvalidInput, "只支持 JPG、PNG、GIF、WebP 格式的图片")
	default:
		// 走到这里说明是写盘失败这类环境问题：日志里留完整原因，
		// 响应里只说「失败了」，避免把磁盘路径暴露给客户端。
		slog.ErrorContext(c.Request.Context(), "save avatar", "error", err)
		response.Error(c, http.StatusInternalServerError, "图片保存失败，请稍后重试")
	}
}
