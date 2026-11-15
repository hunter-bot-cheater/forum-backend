package config

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"forum-backend/internal/metrics"
)

// defaultSlowQuery 是 mysql.slow_query_ms 没配或配成 0 时用的阈值。
// 200ms 是「本机也能感觉出卡」的量级，低于它的查询优化收益不大。
const defaultSlowQuery = 200 * time.Millisecond

// gormSlogLogger 把 GORM 的 SQL 日志接到 slog 上，
// 让慢查询和业务错误日志是同一套 JSON 字段（时间、级别、request_id 都在）。
//
// 不使用 gorm 自带的 logger.Default：它往 stdout 打带颜色和 \r\n 的文本，
// 混进 JSON 流里会让日志采集端解析失败。
type gormSlogLogger struct {
	level gormlogger.LogLevel
	slow  time.Duration
}

// newGormLogger 创建 SQL 日志器。level 固定 Warn：
// 只记错误和慢查询，成功的 SQL 不记，否则一条业务请求能刷十几行日志。
func newGormLogger(slow time.Duration) gormlogger.Interface {
	if slow <= 0 {
		slow = defaultSlowQuery
	}
	return &gormSlogLogger{level: gormlogger.Warn, slow: slow}
}

// LogMode 返回切换级别后的副本。GORM 内部会用 Session 调它，
// 必须返回新对象而不是改自己，否则并发请求会互相改级别。
func (l *gormSlogLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	clone := *l
	clone.level = level
	return &clone
}

// Info / Warn / Error 是 GORM 自己触发的日志（如「record not found」之外的内部提示）。
// 业务 SQL 都走 Trace，这里保持最小实现，交给 slog 打一条即可。
func (l *gormSlogLogger) Info(ctx context.Context, msg string, args ...any) {
	if l.level >= gormlogger.Info {
		slog.InfoContext(ctx, "gorm: "+msg, "args", args)
	}
}

func (l *gormSlogLogger) Warn(ctx context.Context, msg string, args ...any) {
	if l.level >= gormlogger.Warn {
		slog.WarnContext(ctx, "gorm: "+msg, "args", args)
	}
}

func (l *gormSlogLogger) Error(ctx context.Context, msg string, args ...any) {
	if l.level >= gormlogger.Error {
		slog.ErrorContext(ctx, "gorm: "+msg, "args", args)
	}
}

// Trace 是每条 SQL 的出口，GORM 在语句执行完后调用它。
//
// 三种情况分开记：
//   - 执行出错（除「查不到记录」）→ error，带上 sql 便于直接复制去 explain；
//   - 超过慢查询阈值 → warn，这是要加索引或改查询的信号；
//   - 其余成功语句 → 只有 level 调到 Info 才记，平时完全静默。
//
// fc 是个回调，只有真要输出时才调它取 SQL 文本：
// 这样可以避免为了一条不打算打的日志去做字符串拼接。
func (l *gormSlogLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	// ErrRecordNotFound 是业务上的「没查到」，不是故障；把它当错误记会把日志淹掉。
	notFound := errors.Is(err, gorm.ErrRecordNotFound)

	switch {
	case err != nil && !notFound:
		metrics.GormErrors.Inc()
		sql, rows := fc()
		slog.ErrorContext(ctx, "gorm: SQL 执行失败",
			"elapsed_ms", elapsedMS(elapsed),
			"rows", rows,
			"sql", sql,
			"error", err.Error(),
		)
	case l.slow > 0 && elapsed > l.slow:
		metrics.GormSlowQueries.Inc()
		sql, rows := fc()
		slog.WarnContext(ctx, "gorm: 慢查询",
			"elapsed_ms", elapsedMS(elapsed),
			"threshold_ms", elapsedMS(l.slow),
			"rows", rows,
			"sql", sql,
		)
	case l.level >= gormlogger.Info:
		sql, rows := fc()
		slog.InfoContext(ctx, "gorm: SQL",
			"elapsed_ms", elapsedMS(elapsed),
			"rows", rows,
			"sql", sql,
		)
	}
}

// elapsedMS 把耗时换成毫秒浮点数（保留一位小数）。
// 统一用毫秒是为了和访问日志的 latency_ms 同一个单位，看图时不用换算。
func elapsedMS(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
