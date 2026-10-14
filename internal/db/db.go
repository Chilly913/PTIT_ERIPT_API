// Package db opens the PostgreSQL connection (GORM over pgx) and runs
// golang-migrate migrations embedded from the migrations/ directory.
package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
)

// Open connects to PostgreSQL, applies pool settings and pings the server.
func Open(ctx context.Context, cfg config.DBConfig, log *slog.Logger) (*gorm.DB, error) {
	gdb, err := gorm.Open(postgres.New(postgres.Config{DSN: cfg.DSN()}), &gorm.Config{
		Logger:                                   NewLogger(log, cfg.SlowQueryThreshold, parseLogLevel(cfg.LogLevel)),
		NowFunc:                                  func() time.Time { return time.Now().UTC() },
		DisableForeignKeyConstraintWhenMigrating: true,
		TranslateError:                           true,
	})
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("db: underlying sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return gdb, nil
}

// Ping checks connectivity (used by /api/health).
func Ping(ctx context.Context, gdb *gorm.DB) error {
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// Close closes the underlying pool.
func Close(gdb *gorm.DB) error {
	if gdb == nil {
		return nil
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func parseLogLevel(s string) logger.LogLevel {
	switch s {
	case "silent":
		return logger.Silent
	case "error":
		return logger.Error
	case "info":
		return logger.Info
	default:
		return logger.Warn
	}
}
