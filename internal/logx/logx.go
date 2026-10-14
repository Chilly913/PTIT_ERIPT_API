// Package logx builds the process-wide slog logger and carries request-scoped
// loggers through context.
package logx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
)

// New builds a slog.Logger from config and installs it as the default.
func New(cfg config.LogConfig, app config.AppConfig) *slog.Logger {
	return NewWithWriter(os.Stdout, cfg, app)
}

// NewWithWriter is New with an explicit output (tests).
func NewWithWriter(w io.Writer, cfg config.LogConfig, app config.AppConfig) *slog.Logger {
	opts := &slog.HandlerOptions{Level: ParseLevel(cfg.Level)}
	var h slog.Handler
	if cfg.Format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	l := slog.New(h).With("app", app.Name, "env", app.Env)
	slog.SetDefault(l)
	return l
}

// ParseLevel maps a level name to slog.Level (default info).
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type ctxKey struct{}

// WithContext returns a context carrying l.
func WithContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the request-scoped logger or slog.Default().
func FromContext(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
			return l
		}
	}
	return slog.Default()
}
