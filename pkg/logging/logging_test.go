package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// useTempLogger 把 Setup 的结果装好，并在用例结束时还原全局 logger，
// 否则一个用例改了 slog.Default，后面的用例会跟着变。
func useTempLogger(t *testing.T, cfg Config) string {
	t.Helper()

	logPath := filepath.Join(t.TempDir(), "app.log")
	cfg.Console = false
	cfg.File.Path = logPath

	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	logger, closeFn, err := Setup(cfg)
	if err != nil {
		t.Fatalf("setup logger: %v", err)
	}
	t.Cleanup(func() { closeFn() })
	// 包级函数 slog.Info 用的是 SetDefault 的那个实例，
	// 断言里直接调用它，才能验证到「全局生效」这条链路。
	if slog.Default() != logger {
		t.Fatal("Setup 应当把 logger 设为全局默认")
	}
	return logPath
}

// readLines 读出日志文件的所有行并解析成 JSON。
// 文件写入是同步的（没有缓冲），所以调用前不需要 flush 或 sleep。
func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	lines := make([]map[string]any, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("日志不是合法 JSON: %q (%v)", line, err)
		}
		lines = append(lines, record)
	}
	return lines
}

// TestSetupWritesJSONWithRequestID 验证核心契约：
// 每条日志是合法 JSON，且用 ctx 打日志时会自动带上 request_id。
func TestSetupWritesJSONWithRequestID(t *testing.T) {
	path := useTempLogger(t, Config{Level: "info", Format: "json"})

	ctx := WithRequestID(context.Background(), "req-abc123")
	slog.InfoContext(ctx, "hello", "user_id", 7)

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("期望 1 条日志，实际 %d 条", len(lines))
	}
	record := lines[0]
	if record["msg"] != "hello" {
		t.Errorf("msg = %v", record["msg"])
	}
	if record["level"] != "INFO" {
		t.Errorf("level = %v", record["level"])
	}
	if record["request_id"] != "req-abc123" {
		t.Errorf("request_id = %v（ctx 里的追踪号没有自动附加）", record["request_id"])
	}
	// JSON 里数字会被解析成 float64。
	if record["user_id"] != float64(7) {
		t.Errorf("user_id = %v", record["user_id"])
	}
	if _, ok := record["time"]; !ok {
		t.Error("缺少 time 字段")
	}
}

// TestLevelFilter 验证级别过滤：配成 warn 时 info 不该出现。
func TestLevelFilter(t *testing.T) {
	path := useTempLogger(t, Config{Level: "warn", Format: "json"})

	slog.Info("这条应当被丢掉")
	slog.Warn("这条应当留下")

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("期望只剩 1 条，实际 %d 条", len(lines))
	}
	if lines[0]["msg"] != "这条应当留下" {
		t.Errorf("留下的日志不对: %v", lines[0]["msg"])
	}
}

// TestTextFormat 验证 format: text 走的是另一个 handler，输出不是 JSON。
func TestTextFormat(t *testing.T) {
	path := useTempLogger(t, Config{Level: "info", Format: "text"})

	slog.Info("plain text")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		t.Errorf("text 格式不该输出 JSON: %s", raw)
	}
	if !strings.Contains(string(raw), "msg=\"plain text\"") {
		t.Errorf("text 格式缺少 msg: %s", raw)
	}
}

// TestBadConfigRejected 覆盖配置写错的情况：
// 级别或格式拼错都要报错，不能悄悄降到默认值。
func TestBadConfigRejected(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{name: "级别拼错", cfg: Config{Level: "verbose", Console: true}},
		{name: "格式拼错", cfg: Config{Level: "info", Format: "xml", Console: true}},
		{name: "没有输出目标", cfg: Config{Level: "info", Format: "json", Console: false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := Setup(c.cfg); err == nil {
				t.Fatal("期望报错，实际通过了")
			}
		})
	}
}

// TestRequestIDEmpty 验证没塞追踪号时不会多出一个空字段。
func TestRequestIDEmpty(t *testing.T) {
	path := useTempLogger(t, Config{Level: "info", Format: "json"})

	slog.InfoContext(context.Background(), "no trace")
	lines := readLines(t, path)
	if _, ok := lines[0]["request_id"]; ok {
		t.Errorf("没有追踪号时不应出现 request_id 字段: %v", lines[0])
	}
}

// TestRotateBySize 验证写满上限时切分：当前文件被改名成 .1，新写入进新文件。
func TestRotateBySize(t *testing.T) {
	// 这里不能复用 useTempLogger 的路径：Setup 已经打开了同一个文件，
	// Windows 上两个句柄会让改名失败（真实环境里也只有一个进程写日志）。
	path := filepath.Join(t.TempDir(), "rotate.log")

	// 直接操作 rotateWriter，测的是切分逻辑本身，不必凑满 1MB 日志。
	writer, err := newRotateWriter(path, 200, 2, 24*time.Hour)
	if err != nil {
		t.Fatalf("new rotate writer: %v", err)
	}
	defer writer.Close()

	chunk := []byte(strings.Repeat("x", 100))
	for i := 0; i < 4; i++ {
		if _, err := writer.Write(chunk); err != nil {
			t.Fatalf("write #%d: %v", i, err)
		}
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("期望存在备份文件 %s.1: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 当前文件: %v", err)
	}
	// 每次写 100 字节、上限 200：第 3 条会触发切分，所以当前文件最多 200 字节。
	if info.Size() > 200 {
		t.Errorf("当前文件 %d 字节，超过了上限 200", info.Size())
	}
}

// TestRotateSingleOversizedWrite 验证单条日志本身超过上限时不会死循环，
// 只是写成一个超限的文件。这条边界很容易在实现里写错。
func TestRotateSingleOversizedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	writer, err := newRotateWriter(path, 10, 1, time.Hour)
	if err != nil {
		t.Fatalf("new rotate writer: %v", err)
	}
	defer writer.Close()

	big := []byte(strings.Repeat("y", 100))
	if _, err := writer.Write(big); err != nil {
		t.Fatalf("oversized write: %v", err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("空文件不该被切分，期望没有备份文件")
	}
}

// TestCleanupExcessBackups 验证启动时按份数清理多余备份。
func TestCleanupExcessBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// 先造 5 份备份，再以「只留 2 份」的配置打开，应当剩下 .1 和 .2。
	for i := 1; i <= 5; i++ {
		name := path + "." + string(rune('0'+i))
		if err := os.WriteFile(name, []byte("old"), 0o644); err != nil {
			t.Fatalf("准备备份文件: %v", err)
		}
	}

	writer, err := newRotateWriter(path, 1024, 2, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("new rotate writer: %v", err)
	}
	defer writer.Close()

	for i := 1; i <= 2; i++ {
		if _, err := os.Stat(path + "." + string(rune('0'+i))); err != nil {
			t.Errorf("第 %d 份备份应当保留: %v", i, err)
		}
	}
	for i := 3; i <= 5; i++ {
		name := path + "." + string(rune('0'+i))
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Errorf("超出份数的备份应当被删除: %s", name)
		}
	}
}
