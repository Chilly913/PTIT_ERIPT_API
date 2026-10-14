package main

import (
	"os"
	"testing"

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

// TestRoutesDoNotConflict builds the real router with every module: gin panics
// on conflicting wildcard names or duplicate routes, which no single module
// test can catch.
func TestRoutesDoNotConflict(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	t.Setenv("DB_URL", os.Getenv("TEST_DATABASE_URL"))
	t.Setenv("REDIS_URL", os.Getenv("TEST_REDIS_URL"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(t.Context(), cfg, logx.New(cfg.Log, cfg.App))
	if err != nil {
		t.Skip("cannot build app (MinIO?): ", err)
	}
	defer a.Close()
	roles := rbac.FromApp(a)
	am := authmod.New(a, roles)
	tok := am.Tokens()
	r, err := server.NewRouter(a, nil, am, catalog.New(a, roles, tok), study.New(a, roles, tok), interaction.New(a, roles, tok))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d routes registered", len(r.Routes()))
}
