// Command server runs the HTTP API.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
	authmod "github.com/Chilly913/PTIT_ERIPT_API/internal/modules/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/modules/catalog"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/modules/interaction"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/modules/study"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/rbac"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}

func run() error {
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
	tokens := authModule.Tokens()
	modules := []app.Module{
		authModule,
		catalog.New(a, roles, tokens),
		study.New(a, roles, tokens),
		interaction.New(a, roles, tokens),
	}

	return server.Run(ctx, a, modules...)
}
