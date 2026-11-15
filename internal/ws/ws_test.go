package ws

import (
	"net/http/httptest"
	"testing"
)

func TestCheckOrigin(t *testing.T) {
	h := NewWSHandler(nil, nil, []string{"127.0.0.1:5173", "tunnel.example.com"})
	if h.upgrader.CheckOrigin == nil {
		t.Fatal("upgrader must have CheckOrigin")
	}

	cases := []struct {
		origin string
		want   bool
	}{
		{"http://127.0.0.1:5173", true},
		{"https://tunnel.example.com", true},
		{"https://evil.com", false},
		{"http://127.0.0.1:8080", false},
		{"", true},
		{"%", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/api/ws/notifications", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if got := h.upgrader.CheckOrigin(req); got != tc.want {
			t.Fatalf("origin %q = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

func TestCheckOriginEmptyAllowlist(t *testing.T) {
	h := NewWSHandler(nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/ws/notifications", nil)
	req.Header.Set("Origin", "https://tunnel.example.com")
	if h.upgrader.CheckOrigin(req) {
		t.Fatal("empty allowlist should reject browser origin")
	}
}
