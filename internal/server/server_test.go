package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
)

func newTestRouter(t *testing.T, checks []Check) *gin.Engine {
	t.Helper()
	cfg, err := config.LoadFrom(map[string]string{"APP_ENV": "testing", "APP_DEBUG": "false", "CORS_ALLOWED_ORIGINS": "https://eript-lms.io.vn"})
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mod := app.ModuleFunc(func(api *gin.RouterGroup) {
		api.POST("/things", func(c *gin.Context) { c.JSON(201, gin.H{"ok": true}) })
	})
	r, err := NewRouter(a, checks, mod)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func do(r http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestFallbacks(t *testing.T) {
	r := newTestRouter(t, nil)
	w := do(r, "GET", "/api/nope", nil)
	if w.Code != 404 || w.Body.String() != `{"message":"The route api/nope could not be found."}` {
		t.Fatalf("404: %d %s", w.Code, w.Body.String())
	}
	w = do(r, "GET", "/api/things", nil)
	if w.Code != 405 || w.Body.String() != `{"message":"The GET method is not supported for route api/things. Supported methods: POST."}` {
		t.Fatalf("405: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get(RequestIDHeader) == "" {
		t.Fatal("missing request id")
	}
}

func TestHealth(t *testing.T) {
	r := newTestRouter(t, []Check{
		{"database", func(context.Context) error { return nil }},
		{"redis", func(context.Context) error { return errors.New("down") }},
	})
	if w := do(r, "GET", "/health", nil); w.Code != 200 {
		t.Fatalf("/health %d", w.Code)
	}
	w := do(r, "GET", "/api/health", nil)
	if w.Code != 503 || w.Body.String() != `{"checks":{"database":"ok","redis":"fail"},"status":"degraded"}` {
		t.Fatalf("/api/health %d %s", w.Code, w.Body.String())
	}
}

func TestCORS(t *testing.T) {
	r := newTestRouter(t, nil)
	pre := map[string]string{"Origin": "http://localhost:50830", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization,content-type"}
	w := do(r, "OPTIONS", "/api/things", pre)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:50830" ||
		w.Header().Get("Access-Control-Allow-Credentials") != "true" || w.Header().Get("Access-Control-Allow-Headers") != "authorization,content-type" {
		t.Fatalf("preflight: %d %v", w.Code, w.Header())
	}
	w = do(r, "POST", "/api/things", map[string]string{"Origin": "https://eript-lms.io.vn"})
	if w.Code != 201 || w.Header().Get("Access-Control-Allow-Origin") != "https://eript-lms.io.vn" {
		t.Fatalf("simple: %d %v", w.Code, w.Header())
	}
	w = do(r, "POST", "/api/things", map[string]string{"Origin": "https://evil.example"})
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("evil origin allowed")
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	cfg, err := config.LoadFrom(map[string]string{"APP_ENV": "testing", "PORT": "0", "HTTP_SHUTDOWN_TIMEOUT": "2s"})
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, a) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
