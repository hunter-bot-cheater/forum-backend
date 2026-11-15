// Package logging 组装项目统一的日志输出：slog + JSON 格式 + 可选的文件轮转。
//
// 约定：业务代码一律用 slog（InfoContext / ErrorContext 等），不要再 import 标准库 log。
// 这样每条日志都是同样的字段结构，能按 level、request_id 直接筛。
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// ctxKey 是 request id 在 context.Context 里的键。
// 用自定义空结构体而不是字符串，避免和别的包塞进去的同名键撞车。
type ctxKey struct{}

// attrRequestID 是日志里追踪号的字段名，和响应体的 request_id 保持一致。
const attrRequestID = "request_id"

// FileConfig 描述日志文件的落盘策略。Path 为空表示不写文件。
type FileConfig struct {
	// Path 是当前日志文件路径，例如 logs/app.log，备份是同目录的 app.log.1、app.log.2。
	Path string
	// MaxSizeMB 是单个文件的上限，写满就切分。
	MaxSizeMB int
	// MaxBackups 是保留几份历史文件，超出从最旧的开始删。
	MaxBackups int
	// MaxAgeDays 是历史文件的保留天数，启动时清理一次。
	MaxAgeDays int
}

// Config 是日志的初始化参数，对应 yaml 里的 log 段。
type Config struct {
	// Level 取 debug / info / warn / error，留空按 info 处理。
	Level string
	// Format 取 json / text，留空按 json 处理。本机排查用 text 更易读。
	Format string
	// Console 为 true 时同时写到标准输出，容器里靠它收集日志。
	Console bool
	File    FileConfig
}

// Setup 按配置创建全局 logger，返回 logger 本身和一个关闭函数。
//
// 调用方（cmd/server）应当：
//   - 用返回的 closeFn 在退出时关掉日志文件，否则 Windows 上文件句柄不释放；
//   - 之后直接用 slog.Info 等包级函数，因为这里已经 SetDefault 过了。
//
// 返回错误表示配置写错（级别或格式拼错）或日志文件没权限写，
// 这两种都应当在启动阶段立刻失败，而不是带着一个悄悄丢日志的进程跑起来。
func Setup(cfg Config) (*slog.Logger, func() error, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, nil, err
	}

	// 输出目标可以同时有多个：控制台给容器收集，文件给没有日志系统的机器排障。
	writers := make([]io.Writer, 0, 2)
	closers := make([]io.Closer, 0, 1)
	if cfg.Console {
		// 用 stdout 而不是 stderr：systemd、docker logs、穿透工具默认都收 stdout。
		writers = append(writers, os.Stdout)
	}
	if cfg.File.Path != "" {
		fileWriter, err := newRotateWriter(
			cfg.File.Path,
			int64(fileMaxSizeMB(cfg.File.MaxSizeMB))*1024*1024,
			fileMaxBackups(cfg.File.MaxBackups),
			time.Duration(fileMaxAgeDays(cfg.File.MaxAgeDays))*24*time.Hour,
		)
		if err != nil {
			return nil, nil, err
		}
		writers = append(writers, fileWriter)
		closers = append(closers, fileWriter)
	}
	if len(writers) == 0 {
		return nil, nil, fmt.Errorf("logging: 没有输出目标，log.console 和 log.file.path 至少要配一个")
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "", "json":
		handler = slog.NewJSONHandler(io.MultiWriter(writers...), opts)
	case "text":
		handler = slog.NewTextHandler(io.MultiWriter(writers...), opts)
	default:
		return nil, nil, fmt.Errorf("logging: 不支持的 log.format %q，只能是 json 或 text", cfg.Format)
	}

	// 包一层 contextHandler，让每个请求链路上的日志自动带上 request_id，
	// 业务代码不必手动传这个字段。
	logger := slog.New(contextHandler{Handler: handler})
	slog.SetDefault(logger)

	closeFn := func() error {
		var firstErr error
		for _, c := range closers {
			if err := c.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	return logger, closeFn, nil
}

// WithRequestID 把追踪号放进 ctx，供后续日志自动附加。
// 只在 HTTP 入口（middleware.RequestID）调用一次，下游靠 slog.XxxContext 取。
func WithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil || id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID 从 ctx 里取追踪号，没有就返回空串。
// 除了日志，慢查询日志和 panic 兜底也用它把一次请求串起来。
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// contextHandler 给每条日志补上 ctx 里的 request_id。
// 只覆写 Handle，WithAttrs / WithGroup 必须原样包一层，
// 否则 handler.With(...) 会退化成不带追踪号的普通 handler。
type contextHandler struct {
	slog.Handler
}

// Handle 在真正写出之前补一个 request_id 字段（ctx 里有才补）。
func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String(attrRequestID, id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{Handler: h.Handler.WithGroup(name)}
}

// parseLevel 把配置里的字符串转成 slog 级别。
// 拼错时直接报错而不是悄悄降级到 info，否则「调了级别却不生效」会很难查。
func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging: 不支持的 log.level %q，只能是 debug / info / warn / error", raw)
	}
}

// 下面三个函数在没有配置时给出可用的默认值，
// 让「只写了 log.level: debug」这种最小配置也能正常切分文件。
func fileMaxSizeMB(v int) int {
	if v <= 0 {
		return 50
	}
	return v
}

func fileMaxBackups(v int) int {
	if v <= 0 {
		return 7
	}
	return v
}

func fileMaxAgeDays(v int) int {
	if v <= 0 {
		return 30
	}
	return v
}
