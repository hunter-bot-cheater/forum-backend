package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"forum-backend/pkg/logging"
)

// setupGormLog 把全局 logger 指到临时文件，返回日志路径。
// 用真文件和真 handler，才能验证 request_id 从 ctx 自动附加这条链路。
func setupGormLog(t *testing.T) string {
	t.Helper()

	logPath := filepath.Join(t.TempDir(), "gorm.log")
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
	return logPath
}

// readGormLog 读出日志并解析成 JSON 记录。
func readGormLog(t *testing.T, path string) []map[string]any {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	records := make([]map[string]any, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("不是合法 JSON: %q (%v)", line, err)
		}
		records = append(records, record)
	}
	return records
}

// TestGormSlowQueryLogged 验证超过阈值的 SQL 记一条 warn，并带上 sql、耗时与 request_id。
func TestGormSlowQueryLogged(t *testing.T) {
	path := setupGormLog(t)

	logger := newGormLogger(10 * time.Millisecond)
	ctx := logging.WithRequestID(context.Background(), "trace-slow")
	fc := func() (string, int64) { return "SELECT * FROM articles WHERE id = ?", 1 }

	// begin 往前推 50ms，模拟一条耗时 50ms 的查询。
	logger.Trace(ctx, time.Now().Add(-50*time.Millisecond), fc, nil)

	records := readGormLog(t, path)
	if len(records) != 1 {
		t.Fatalf("期望 1 条慢查询日志，实际 %d 条", len(records))
	}
	record := records[0]
	if record["level"] != "WARN" {
		t.Errorf("慢查询应当是 WARN，实际 %v", record["level"])
	}
	if record["sql"] != "SELECT * FROM articles WHERE id = ?" {
		t.Errorf("sql = %v", record["sql"])
	}
	if record["request_id"] != "trace-slow" {
		t.Errorf("request_id = %v（业务链路上的追踪号没进慢查询日志）", record["request_id"])
	}
	elapsed, ok := record["elapsed_ms"].(float64)
	if !ok || elapsed < 40 {
		t.Errorf("elapsed_ms = %v，应当接近 50", record["elapsed_ms"])
	}
}

// TestGormFastQuerySilent 验证没超阈值的查询默认不记日志，
// 否则每个请求都要刷十几行 SQL。
func TestGormFastQuerySilent(t *testing.T) {
	path := setupGormLog(t)

	logger := newGormLogger(200 * time.Millisecond)
	fc := func() (string, int64) { return "SELECT 1", 1 }
	logger.Trace(context.Background(), time.Now(), fc, nil)

	if info, err := os.Stat(path); err != nil {
		t.Fatalf("stat log: %v", err)
	} else if info.Size() != 0 {
		t.Fatalf("快查询不该写日志，实际写了 %d 字节", info.Size())
	}
}

// TestGormErrorAndNotFound 覆盖两个易错分支：
// 真错误要记 error；ErrRecordNotFound 是业务上的「没查到」，不能当故障刷日志。
func TestGormErrorAndNotFound(t *testing.T) {
	path := setupGormLog(t)

	logger := newGormLogger(200 * time.Millisecond)
	fc := func() (string, int64) { return "SELECT * FROM users WHERE id = ?", 0 }

	logger.Trace(context.Background(), time.Now(), fc, gorm.ErrRecordNotFound)
	if info, _ := os.Stat(path); info.Size() != 0 {
		t.Fatalf("ErrRecordNotFound 不该记日志，实际写了 %d 字节", info.Size())
	}

	logger.Trace(context.Background(), time.Now(), fc, gorm.ErrInvalidDB)

	records := readGormLog(t, path)
	if len(records) != 1 {
		t.Fatalf("期望 1 条错误日志，实际 %d 条", len(records))
	}
	if records[0]["level"] != "ERROR" {
		t.Errorf("SQL 执行失败应当是 ERROR，实际 %v", records[0]["level"])
	}
	if records[0]["error"] == nil {
		t.Error("错误日志缺少 error 字段")
	}
}

// TestGormLogModeIsolatesLevel 验证 LogMode 返回副本：
// GORM 会用 Session 切级别，如果改到原对象，并发请求会互相影响。
func TestGormLogModeIsolatesLevel(t *testing.T) {
	base := newGormLogger(200 * time.Millisecond)
	quiet := base.LogMode(gormlogger.Silent)

	if quiet == base {
		t.Fatal("LogMode 应当返回新实例")
	}
	if _, ok := base.(*gormSlogLogger); !ok {
		t.Fatalf("类型不对: %T", base)
	}
	// 原对象级别必须还是 Warn，只有副本变成 Silent。
	original := base.(*gormSlogLogger)
	silent := quiet.(*gormSlogLogger)
	if original.level != gormlogger.Warn {
		t.Errorf("原实例的级别被改掉了: %v", original.level)
	}
	if silent.level != gormlogger.Silent {
		t.Errorf("副本级别应当是 Silent，实际 %v", silent.level)
	}
}
