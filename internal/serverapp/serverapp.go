// Package serverapp wires modules and runs the HTTP API; shared by `go run .` and cmd/server.
package serverapp

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
	academicmod "github.com/Chilly913/PTIT_ERIPT_API/internal/modules/academic"
	adminmod "github.com/Chilly913/PTIT_ERIPT_API/internal/modules/admin"
	authmod "github.com/Chilly913/PTIT_ERIPT_API/internal/modules/auth"
	chatbotmod "github.com/Chilly913/PTIT_ERIPT_API/internal/modules/chatbot"
	contentmod "github.com/Chilly913/PTIT_ERIPT_API/internal/modules/content"
	learningmod "github.com/Chilly913/PTIT_ERIPT_API/internal/modules/learning"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/rbac"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/server"
)

// Run loads .env (if present, never overriding real environment variables), then serves until SIGINT/SIGTERM.
func Run() error {
	if _, err := config.LoadDotEnv(".env"); err != nil {
		return fmt.Errorf(".env: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logx.New(cfg.Log, cfg.App)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := a.Close(); err != nil {
			log.Error("close resources", "error", err.Error())
		}
	}()

	// Domain modules are mounted under /api as they are ported (see docs/MIGRATION_PLAN.md).
	roles := rbac.FromApp(a)
	authModule := authmod.New(a, roles)
	modules := []app.Module{
		authModule,
		contentmod.New(a, authModule.Tokens(), roles),
		academicmod.New(a, authModule.Tokens(), roles),
		chatbotmod.New(a),
		learningmod.New(a, authModule.Tokens(), roles),
		adminmod.New(a, authModule.Tokens(), roles, authModule),
	}

	return server.Run(ctx, a, modules...)
}
