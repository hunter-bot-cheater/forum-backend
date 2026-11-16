package router

import (
	"encoding/json"
	"os"
	"testing"
)

// TestSwaggerContractCoversCorePaths 核对生成物里的核心路径。
// 文件缺失直接失败，CI 里先跑 swag init 再跑本测试。
func TestSwaggerContractCoversCorePaths(t *testing.T) {
	raw, err := os.ReadFile("../../api/swagger/swagger.json")
	if err != nil {
		t.Fatalf("read swagger.json: %v", err)
	}
	var doc struct {
		Swagger  string                     `json:"swagger"`
		BasePath string                     `json:"basePath"`
		Paths    map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse swagger.json: %v", err)
	}
	if doc.Swagger != "2.0" {
		t.Fatalf("swagger version = %q, want 2.0", doc.Swagger)
	}
	if doc.BasePath != "/api" {
		t.Fatalf("basePath = %q, want /api", doc.BasePath)
	}

	want := []string{
		"/register",
		"/login",
		"/articles",
		"/articles/{id}",
		"/articles/hot",
		"/articles/{id}/like",
		"/articles/{id}/comments",
		"/comments/{id}",
		"/notifications",
		"/notifications/read-all",
		"/users/{id}",
		"/users/me",
		"/users/{id}/follow",
		"/users/{id}/following",
		"/users/{id}/followers",
		"/conversations",
		"/conversations/unread",
		"/conversations/with/{user_id}/messages",
		"/conversations/with/{user_id}/read",
	}
	for _, path := range want {
		if _, ok := doc.Paths[path]; !ok {
			t.Errorf("missing path %s", path)
		}
	}
}
