package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/middleware"
)

// 跨域的 404 / 405 也必须带 CORS 头，否则浏览器读不到错误体和 request_id。
func TestCORSOnFallbackResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	installFallbacks(r)
	cors, err := middleware.CORS([]string{"https://front.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	r.Use(cors)
	r.GET("/only-get", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	cases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/nope", http.StatusNotFound},
		{http.MethodPost, "/only-get", http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Origin", "https://front.example.com")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != tc.want {
			t.Errorf("%s %s: status = %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://front.example.com" {
			t.Errorf("%s %s: allow-origin = %q", tc.method, tc.path, got)
		}
	}
}
