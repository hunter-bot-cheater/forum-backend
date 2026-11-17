package upload

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// handler 的用例只关心一件事：service 的哨兵错误有没有被翻成正确的状态码。
// 前端靠 413 与 415 决定提示什么，全都回 400 的话，用户只能看到「上传失败」。

func newTestRouter(t *testing.T, cfg Config) (*gin.Engine, string) {
	t.Helper()
	svc, dir := newService(t, cfg)
	h := NewHandler(svc)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 和 router.go 里那条 limitBody 一样限制请求体：
	// 少了它，「请求体超限」这条分支在测试里永远走不到。
	r.Use(func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.BodyLimit())
	})
	r.POST("/api/uploads/avatar", h.Avatar)
	return r, dir
}

// multipartBody 拼一个带 file 字段的表单。
// filename 为空表示故意不带文件字段，用来测「没选文件」。
func multipartBody(t *testing.T, field, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	if filename != "" {
		part, err := w.CreateFormFile(field, filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("write form file: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return body, w.FormDataContentType()
}

// doUpload 发一次请求并把响应解成通用结构。
func doUpload(t *testing.T, r *gin.Engine, body *bytes.Buffer, contentType string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/uploads/avatar", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON（%d）: %s", w.Code, w.Body.String())
	}
	return w.Code, resp
}

func TestAvatarUploadSucceeds(t *testing.T) {
	r, _ := newTestRouter(t, Config{})
	raw := encodeTestJPEG(t, noiseImage(800, 600))

	body, contentType := multipartBody(t, "file", "photo.jpg", raw)
	status, resp := doUpload(t, r, body, contentType)

	if status != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200，响应: %v", status, resp)
	}
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 data 对象: %v", resp)
	}
	url, _ := data["url"].(string)
	if url == "" {
		t.Fatalf("data.url 为空: %v", data)
	}
	// original_bytes 是给前端显示「267 KB → 24 KB」用的，
	// 少了它用户不知道自己省了多少，也没法判断图有没有被压坏。
	if data["original_bytes"].(float64) <= data["bytes"].(float64) {
		t.Errorf("original_bytes 应当大于 bytes: %v", data)
	}
}

func TestAvatarUploadWithoutFile(t *testing.T) {
	r, _ := newTestRouter(t, Config{})

	// 只发一个空表单，不带 file 字段。
	body, contentType := multipartBody(t, "file", "", nil)
	status, resp := doUpload(t, r, body, contentType)

	if status != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400", status)
	}
	if resp["message"] == "" {
		t.Error("应给出人能看懂的提示")
	}
}

func TestAvatarUploadRejectsNonImage(t *testing.T) {
	r, _ := newTestRouter(t, Config{})

	body, contentType := multipartBody(t, "file", "note.txt", []byte("这不是图片"))
	status, _ := doUpload(t, r, body, contentType)

	// 415 而不是 400：文件类型不对和参数缺失是两种错，
	// 前端要提示「换一张图」而不是「重新检查表单」。
	if status != http.StatusUnsupportedMediaType {
		t.Fatalf("状态码 = %d, 期望 415", status)
	}
}

func TestAvatarUploadRejectsOversizedBody(t *testing.T) {
	// 这里模拟的是另一条路径：请求体在解析阶段就被 MaxBytesReader 掐断，
	// 连 file 字段都没解析出来。把它当成「没有收到文件」回 400 是错的，
	// 用户明明选了文件，只是太大。
	r, _ := newTestRouter(t, Config{MaxBytes: 2048})

	big := bytes.Repeat([]byte("x"), 200<<10)
	body, contentType := multipartBody(t, "file", "big.jpg", big)
	status, resp := doUpload(t, r, body, contentType)

	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d, 期望 413，响应: %v", status, resp)
	}
}

func TestAvatarUploadRejectsTooLarge(t *testing.T) {
	// 把上限压到 2 KB，任何一张正常 JPEG 都超。
	r, _ := newTestRouter(t, Config{MaxBytes: 2048})
	raw := encodeTestJPEG(t, noiseImage(300, 300))
	if len(raw) <= 2048 {
		t.Fatalf("测试数据只有 %d 字节，不够触发上限", len(raw))
	}

	body, contentType := multipartBody(t, "file", "big.jpg", raw)
	status, _ := doUpload(t, r, body, contentType)

	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d, 期望 413", status)
	}
}
