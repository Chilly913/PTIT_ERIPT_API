// Package server builds the Gin engine (middleware, health, fallbacks) and
// runs the HTTP server with graceful shutdown.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Check is a named readiness probe used by /api/health.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// DefaultChecks returns the database and Redis probes for a.
func DefaultChecks(a *app.App) []Check {
	var checks []Check
	if a.DB != nil {
		checks = append(checks, Check{"database", func(ctx context.Context) error { return db.Ping(ctx, a.DB) }})
	}
	if a.Redis != nil {
		checks = append(checks, Check{"redis", func(ctx context.Context) error { return a.Redis.Ping(ctx).Err() }})
	}
	return checks
}

// NewRouter builds the engine: recovery, request logging, CORS, trusted
// proxies, health endpoints, JSON 404/405, then mounts modules under /api.
func NewRouter(a *app.App, checks []Check, modules ...app.Module) (*gin.Engine, error) {
	if a.Cfg.App.Debug {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	httpx.SetDebug(a.Cfg.App.Debug)

	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.ContextWithFallback = true

	proxies := a.Cfg.HTTP.TrustedProxies
	if len(proxies) == 1 && proxies[0] == "*" {
		proxies = []string{"0.0.0.0/0", "::/0"}
	}
	if err := r.SetTrustedProxies(proxies); err != nil {
		return nil, fmt.Errorf("server: trusted proxies: %w", err)
	}

	r.Use(RequestLogger(a.Log), httpx.Recovery(), CORS(a.Cfg.HTTP))

	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/api/health", healthHandler(checks, a.Cfg.App.Debug))

	r.NoRoute(func(c *gin.Context) {
		httpx.NotFound(c, fmt.Sprintf("The route %s could not be found.", strings.TrimPrefix(c.Request.URL.Path, "/")))
	})
	r.NoMethod(func(c *gin.Context) {
		allow := c.Writer.Header().Get("Allow")
		if allow != "" && strings.Contains(allow, http.MethodGet) && !strings.Contains(allow, http.MethodHead) {
			allow += ", " + http.MethodHead
		}
		httpx.Abort(c, http.StatusMethodNotAllowed, fmt.Sprintf(
			"The %s method is not supported for route %s. Supported methods: %s.",
			c.Request.Method, strings.TrimPrefix(c.Request.URL.Path, "/"), allow))
	})

	api := r.Group("/api")
	for _, m := range modules {
		m.Register(api)
	}
	return r, nil
}

func healthHandler(checks []Check, debug bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		status := http.StatusOK
		results := gin.H{}
		for _, ch := range checks {
			if err := ch.Fn(ctx); err != nil {
				status = http.StatusServiceUnavailable
				msg := "fail"
				if debug {
					msg = "fail: " + err.Error()
				}
				results[ch.Name] = msg
				continue
			}
			results[ch.Name] = "ok"
		}
		overall := "ok"
		if status != http.StatusOK {
			overall = "degraded"
		}
		c.JSON(status, gin.H{"status": overall, "checks": results})
	}
}

// Run serves until ctx is cancelled (SIGINT/SIGTERM), then drains in-flight
// requests for up to HTTP_SHUTDOWN_TIMEOUT.
func Run(ctx context.Context, a *app.App, modules ...app.Module) error {
	r, err := NewRouter(a, DefaultChecks(a), modules...)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              ":" + a.Cfg.HTTP.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       a.Cfg.HTTP.ReadTimeout,
		WriteTimeout:      a.Cfg.HTTP.WriteTimeout,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		a.Log.Info("http server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	a.Log.Info("shutting down http server", "timeout", a.Cfg.HTTP.ShutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.Cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server: shutdown: %w", err)
	}
	return <-errCh
}
