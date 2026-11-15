package middleware_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/middleware"
	"forum-backend/pkg/logging"
)

// setupAccessLog 装一个只含 RequestID + AccessLog 的引擎，
// 日志按生产配置落到临时文件，再从文件里读回来断言字段。
//
// 用真文件而不是内存 buffer，是因为要验证「request_id 从 ctx 自动流到日志」
// 这条完整链路，中间少一步（比如忘了把 id 塞进 ctx）就得能测出来。
func setupAccessLog(t *testing.T, skipPaths ...string) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	logPath := filepath.Join(t.TempDir(), "access.log")
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	_, closeLog, err := logging.Setup(logging.Config{
		Level:   "debug",
		Format:  "json",
		Console: false,
		File:    logging.FileConfig{Path: logPath},
	})
	if err != nil {
		t.Fatalf("setup logging: %v", err)
	}
	t.Cleanup(func() { closeLog() })

	r := gin.New()
	r.Use(middleware.RequestID())
	r.Use(middleware.AccessLog(skipPaths...))
	return r, logPath
}

// readRecords 把日志文件逐行解析成 JSON map。
func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read access log: %v", err)
	}
	records := make([]map[string]any, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("访问日志不是合法 JSON: %q (%v)", line, err)
		}
		records = append(records, record)
	}
	return records
}

// TestAccessLogFields 验证一次正常请求会记下状态码、耗时、路由和追踪号。
func TestAccessLogFields(t *testing.T) {
	r, logPath := setupAccessLog(t)
	r.GET("/api/articles/:id", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/articles/42", nil)
	req.Header.Set(middleware.HeaderRequestID, "trace-from-client")
	r.ServeHTTP(httptest.NewRecorder(), req)

	records := readRecords(t, logPath)
	if len(records) != 1 {
		t.Fatalf("期望 1 条访问日志，实际 %d 条", len(records))
	}
	record := records[0]

	if record["msg"] != "http request" {
		t.Errorf("msg = %v", record["msg"])
	}
	if record["level"] != "INFO" {
		t.Errorf("2xx 应当是 INFO，实际 %v", record["level"])
	}
	if record["status"] != float64(200) {
		t.Errorf("status = %v", record["status"])
	}
	// route 是注册模板，path 是真实路径：两者都要在，才能既聚合又定位。
	if record["route"] != "/api/articles/:id" {
		t.Errorf("route = %v", record["route"])
	}
	if record["path"] != "/api/articles/42" {
		t.Errorf("path = %v", record["path"])
	}
	if record["request_id"] != "trace-from-client" {
		t.Errorf("request_id = %v（客户端传入的追踪号没有进日志）", record["request_id"])
	}
	if _, ok := record["latency_ms"]; !ok {
		t.Error("缺少 latency_ms")
	}
	if record["method"] != http.MethodGet {
		t.Errorf("method = %v", record["method"])
	}
}

// TestAccessLogLevelByStatus 验证状态码到日志级别的映射：
// 4xx 是调用方问题记 warn，5xx 是服务端问题记 error，方便按级别配置告警。
func TestAccessLogLevelByStatus(t *testing.T) {
	r, logPath := setupAccessLog(t)
	r.GET("/bad", func(c *gin.Context) {
		c.Status(http.StatusBadRequest)
	})
	r.GET("/boom", func(c *gin.Context) {
		c.Status(http.StatusInternalServerError)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/bad", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	records := readRecords(t, logPath)
	if len(records) != 2 {
		t.Fatalf("期望 2 条，实际 %d 条", len(records))
	}
	if records[0]["level"] != "WARN" {
		t.Errorf("400 应当是 WARN，实际 %v", records[0]["level"])
	}
	if records[1]["level"] != "ERROR" {
		t.Errorf("500 应当是 ERROR，实际 %v", records[1]["level"])
	}
}

// TestAccessLogSkipPaths 验证探活路径不产生日志，避免刷屏。
func TestAccessLogSkipPaths(t *testing.T) {
	r, logPath := setupAccessLog(t, "/healthz")
	r.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, `{"status":"ok"}`)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	// 文件已经创建但内容为空，两者都算「没写日志」。
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat access log: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("探活路径不该写访问日志，实际写了 %d 字节", info.Size())
	}
}
