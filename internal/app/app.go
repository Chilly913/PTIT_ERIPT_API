// Package app is the dependency container shared by the HTTP server, the
// worker and the CLI, plus the Module interface domain packages implement.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/storage"
)

// App holds process-wide dependencies. Fields are safe for concurrent use.
type App struct {
	Cfg     config.Config
	Log     *slog.Logger
	DB      *gorm.DB
	Redis   *redis.Client
	Storage *storage.Storage
}

// Module is a domain package that mounts its routes under /api.
//
//	type Module struct{ app *app.App }
//	func (m *Module) Register(api *gin.RouterGroup) { api.GET("/courses", m.list) }
type Module interface {
	Register(api *gin.RouterGroup)
}

// ModuleFunc adapts a function to Module.
type ModuleFunc func(api *gin.RouterGroup)

// Register implements Module.
func (f ModuleFunc) Register(api *gin.RouterGroup) { f(api) }

// New connects to PostgreSQL and Redis and builds the storage client.
// On error every already-opened resource is closed.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	a := &App{Cfg: cfg, Log: log}

	gdb, err := db.Open(ctx, cfg.DB, log)
	if err != nil {
		return nil, err
	}
	a.DB = gdb

	rdb, err := OpenRedis(ctx, cfg.Redis)
	if err != nil {
		_ = a.Close()
		return nil, err
	}
	a.Redis = rdb

	st, err := storage.New(cfg.Storage)
	if err != nil {
		_ = a.Close()
		return nil, err
	}
	a.Storage = st
	return a, nil
}

// OpenRedis creates a client from config and pings it.
func OpenRedis(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	var opts *redis.Options
	if cfg.URL != "" {
		o, err := redis.ParseURL(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("redis: parse REDIS_URL: %w", err)
		}
		opts = o
	} else {
		opts = &redis.Options{Addr: cfg.Addr(), Password: cfg.Password, DB: cfg.DB}
	}
	rdb := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis: ping %s: %w", opts.Addr, err)
	}
	return rdb, nil
}

// Close releases connections.
func (a *App) Close() error {
	var errs []error
	if a.Redis != nil {
		errs = append(errs, a.Redis.Close())
	}
	if a.DB != nil {
		errs = append(errs, db.Close(a.DB))
	}
	return errors.Join(errs...)
}
