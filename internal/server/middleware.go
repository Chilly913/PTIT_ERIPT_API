package server

import (
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

// RequestIDHeader is echoed on every response.
const RequestIDHeader = "X-Request-ID"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestLogger assigns a request id, stores a request-scoped slog logger in
// the request context and writes one JSON access-log line per request.
func RequestLogger(base *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		rid := c.GetHeader(RequestIDHeader)
		if !validRequestID.MatchString(rid) {
			rid = uuid.NewString()
		}
		c.Header(RequestIDHeader, rid)
		c.Set("request_id", rid)

		l := base.With("request_id", rid)
		c.Request = c.Request.WithContext(logx.WithContext(c.Request.Context(), l))

		c.Next()

		status := c.Writer.Status()
		attrs := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"latency_ms", float64(time.Since(start).Microseconds()) / 1000,
			"bytes", c.Writer.Size(),
			"client_ip", c.ClientIP(),
			"user_agent", c.Request.UserAgent(),
		}
		if uid, ok := c.Get("user_id"); ok {
			attrs = append(attrs, "user_id", uid)
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "errors", c.Errors.String())
		}
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case c.Request.URL.Path == "/health":
			level = slog.LevelDebug
		}
		l.Log(c.Request.Context(), level, "http request", attrs...)
	}
}

// CORS mirrors the Laravel config/cors.php behaviour: exact origins or regexp
// patterns, any method/header, credentials allowed, max-age 0.
func CORS(cfg config.HTTPConfig) gin.HandlerFunc {
	allowAll := slices.Contains(cfg.CORSAllowedOrigins, "*")
	allowed := func(origin string) bool {
		if allowAll || slices.Contains(cfg.CORSAllowedOrigins, origin) {
			return true
		}
		for _, re := range cfg.CORSAllowedOriginPatterns {
			if re.MatchString(origin) {
				return true
			}
		}
		return false
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		preflight := c.Request.Method == http.MethodOptions && c.GetHeader("Access-Control-Request-Method") != ""
		if origin == "" {
			c.Next()
			return
		}
		h := c.Writer.Header()
		h.Add("Vary", "Origin")
		if !allowed(origin) {
			if preflight {
				c.AbortWithStatus(http.StatusNoContent)
				return
			}
			c.Next()
			return
		}
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		if preflight {
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			h.Set("Access-Control-Allow-Methods", strings.ToUpper(c.GetHeader("Access-Control-Request-Method")))
			if reqHeaders := c.GetHeader("Access-Control-Request-Headers"); reqHeaders != "" {
				h.Set("Access-Control-Allow-Headers", reqHeaders)
			}
			h.Set("Access-Control-Max-Age", "0")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
