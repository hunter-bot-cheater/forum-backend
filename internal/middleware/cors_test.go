package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCORSConfigValidation(t *testing.T) {
	invalid := [][]string{
		{"*"},
		{"front.example.com"},
		{"https://front.example.com/"},
		{"https://front.example.com/path"},
	}
	for _, origins := range invalid {
		if _, err := CORS(origins); err == nil {
			t.Fatalf("origins %v should be rejected", origins)
		}
	}

	valid := []string{"https://front.example.com", "http://127.0.0.1:5173"}
	if _, err := CORS(valid); err != nil {
		t.Fatalf("valid origins rejected: %v", err)
	}
}

func TestCORSResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	cors, err := CORS([]string{"https://front.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	r.Use(cors)
	r.GET("/probe", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	cases := []struct {
		name        string
		method      string
		origin      string
		wantStatus  int
		wantAllow   string
		wantMethods string
	}{
		{"allowed GET", http.MethodGet, "https://front.example.com", http.StatusOK, "https://front.example.com", ""},
		{"denied GET", http.MethodGet, "https://evil.example.com", http.StatusOK, "", ""},
		{"no origin", http.MethodGet, "", http.StatusOK, "", ""},
		{"allowed preflight", http.MethodOptions, "https://front.example.com", http.StatusNoContent, "https://front.example.com", corsAllowMethods},
		{"denied preflight", http.MethodOptions, "https://evil.example.com", http.StatusNoContent, "", ""},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, "/probe", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != tc.wantStatus {
			t.Errorf("%s: status = %d, want %d", tc.name, w.Code, tc.wantStatus)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllow {
			t.Errorf("%s: allow-origin = %q, want %q", tc.name, got, tc.wantAllow)
		}
		if got := w.Header().Get("Access-Control-Allow-Methods"); got != tc.wantMethods {
			t.Errorf("%s: allow-methods = %q, want %q", tc.name, got, tc.wantMethods)
		}
		if got := w.Header().Get("Vary"); got != "Origin" {
			t.Errorf("%s: vary = %q", tc.name, got)
		}
	}
}

func TestCORSPreflightHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	cors, err := CORS([]string{"https://front.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	r.Use(cors)

	req := httptest.NewRequest(http.MethodOptions, "/api/articles", nil)
	req.Header.Set("Origin", "https://front.example.com")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Headers"); got != corsAllowHeaders {
		t.Fatalf("allow-headers = %q", got)
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != corsMaxAge {
		t.Fatalf("max-age = %q", got)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); got != corsExposeHeaders {
		t.Fatalf("expose-headers = %q", got)
	}
}
