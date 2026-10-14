package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestDefaultsLoadInDevelopment(t *testing.T) {
	cfg, err := LoadFrom(map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.App.Env != "development" || !cfg.App.Debug {
		t.Fatalf("expected development+debug, got %q/%v", cfg.App.Env, cfg.App.Debug)
	}
	if cfg.DB.Port != 5432 || cfg.DB.SlowQueryThreshold != 200*time.Millisecond {
		t.Fatalf("unexpected DB defaults: %+v", cfg.DB)
	}
	if len(cfg.HTTP.CORSAllowedOriginPatterns) != 4 {
		t.Fatalf("expected 4 default CORS patterns")
	}
}

func TestDSN(t *testing.T) {
	cfg, err := LoadFrom(map[string]string{"DB_HOST": "db", "DB_PORT": "5434", "DB_DATABASE": "x", "DB_USERNAME": "u", "DB_PASSWORD": "p@ss"})
	if err != nil {
		t.Fatal(err)
	}
	want := "postgres://u:p%40ss@db:5434/x?sslmode=disable&timezone=UTC"
	if got := cfg.DB.DSN(); got != want {
		t.Fatalf("DSN = %q, want %q", got, want)
	}
}

func TestS3EndpointScheme(t *testing.T) {
	cfg, err := LoadFrom(map[string]string{"S3_ENDPOINT": "https://s3.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Endpoint != "s3.example.com" || !cfg.Storage.UseSSL {
		t.Fatalf("got %q ssl=%v", cfg.Storage.Endpoint, cfg.Storage.UseSSL)
	}
}

func TestParseErrors(t *testing.T) {
	_, err := LoadFrom(map[string]string{"DB_PORT": "abc", "APP_DEBUG": "maybe", "APP_KEY": "short"})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, s := range []string{"DB_PORT", "APP_DEBUG", "APP_KEY"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error should mention %s: %v", s, err)
		}
	}
}

func TestProductionRequiresSecrets(t *testing.T) {
	_, err := LoadFrom(map[string]string{"APP_ENV": "production"})
	if err == nil {
		t.Fatal("expected production validation error")
	}
	for _, s := range []string{"APP_KEY", "APP_URL", "DB_PASSWORD", "S3_ACCESS_KEY", "AI_SERVICE_SHARED_SECRET"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error should mention %s: %v", s, err)
		}
	}

	key := "base64:" + base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg, err := LoadFrom(map[string]string{
		"APP_ENV": "production", "APP_KEY": key, "APP_URL": "https://eript-lms.io.vn",
		"DB_PASSWORD": "x", "S3_ACCESS_KEY": "a", "S3_SECRET_KEY": "b", "AI_SERVICE_SHARED_SECRET": "s",
		"CORS_ALLOWED_ORIGINS": "https://eript-lms.io.vn",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.App.Debug || len(cfg.App.Key) != 32 {
		t.Fatalf("bad prod config: debug=%v keylen=%d", cfg.App.Debug, len(cfg.App.Key))
	}
}
