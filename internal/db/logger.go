package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

// slogLogger adapts GORM's logger interface to slog. Errors are logged at
// error level (except ErrRecordNotFound), queries slower than the threshold
// at warn, and every query at debug when level is Info.
type slogLogger struct {
	log           *slog.Logger
	slowThreshold time.Duration
	level         logger.LogLevel
}

// NewLogger returns a GORM logger writing to slog. If base is nil the
// request-scoped logger from the context (or slog.Default) is used.
func NewLogger(base *slog.Logger, slowThreshold time.Duration, level logger.LogLevel) logger.Interface {
	return &slogLogger{log: base, slowThreshold: slowThreshold, level: level}
}

func (l *slogLogger) LogMode(level logger.LogLevel) logger.Interface {
	cp := *l
	cp.level = level
	return &cp
}

func (l *slogLogger) logger(ctx context.Context) *slog.Logger {
	if rl := logx.FromContext(ctx); rl != slog.Default() || l.log == nil {
		return rl
	}
	return l.log
}

func (l *slogLogger) Info(ctx context.Context, msg string, args ...any) {
	if l.level >= logger.Info {
		l.logger(ctx).InfoContext(ctx, fmt.Sprintf(msg, args...), "component", "gorm")
	}
}

func (l *slogLogger) Warn(ctx context.Context, msg string, args ...any) {
	if l.level >= logger.Warn {
		l.logger(ctx).WarnContext(ctx, fmt.Sprintf(msg, args...), "component", "gorm")
	}
}

func (l *slogLogger) Error(ctx context.Context, msg string, args ...any) {
	if l.level >= logger.Error {
		l.logger(ctx).ErrorContext(ctx, fmt.Sprintf(msg, args...), "component", "gorm")
	}
}

func (l *slogLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= logger.Silent {
		return
	}
	elapsed := time.Since(begin)
	switch {
	case err != nil && l.level >= logger.Error && !errors.Is(err, gorm.ErrRecordNotFound) && !errors.Is(err, context.Canceled):
		sql, rows := fc()
		l.logger(ctx).ErrorContext(ctx, "sql error", "component", "gorm", "error", err.Error(),
			"elapsed_ms", elapsed.Milliseconds(), "rows", rows, "sql", sql)
	case l.slowThreshold > 0 && elapsed > l.slowThreshold && l.level >= logger.Warn:
		sql, rows := fc()
		l.logger(ctx).WarnContext(ctx, "slow sql", "component", "gorm", "threshold_ms", l.slowThreshold.Milliseconds(),
			"elapsed_ms", elapsed.Milliseconds(), "rows", rows, "sql", sql)
	case l.level >= logger.Info:
		sql, rows := fc()
		l.logger(ctx).DebugContext(ctx, "sql", "component", "gorm", "elapsed_ms", elapsed.Milliseconds(), "rows", rows, "sql", sql)
	}
}
