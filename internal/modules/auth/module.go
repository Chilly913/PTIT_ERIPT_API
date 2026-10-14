// Package auth ports the Laravel auth endpoints (AuthController): register,
// login, logout, me, profile, change-password, upload, password reset, e-mail
// verification and Google OAuth, plus the legacy /api/register|login|logout|me|user aliases.
package auth

import (
	"crypto/sha256"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/mail"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/ratelimit"
)

// Lifetimes copied from Laravel config/auth.php.
const (
	passwordResetExpire   = 60 * time.Minute // auth.passwords.users.expire
	passwordResetThrottle = 60 * time.Second // auth.passwords.users.throttle
	verificationExpire    = 60 * time.Minute // auth.verification.expire
)

// Limits are the per-minute request limits (0 disables a limit).
type Limits struct {
	LoginFailures int // failed logins per email+IP per minute
	Register      int // per IP per minute
	Sensitive     int // forgot/reset password, resend/verify e-mail, per IP per minute
}

// DefaultLimits are used unless WithLimits is given.
var DefaultLimits = Limits{LoginFailures: 5, Register: 20, Sensitive: 10}

// Module implements app.Module.
type Module struct {
	cfg     config.Config
	log     *slog.Logger
	db      *gorm.DB
	roles   auth.RoleService
	tokens  *auth.TokenService
	signer  *auth.Signer
	mailer  mail.Mailer
	limiter *ratelimit.Limiter
	store   ObjectStore
	media   mediaResolver
	google  GoogleProvider
	limits  Limits
	tokOpts auth.TokenOptions
	now     func() time.Time
}

// Option customises the module (mainly for tests).
type Option func(*Module)

// WithMailer overrides the mailer built from MAIL_* config.
func WithMailer(m mail.Mailer) Option { return func(x *Module) { x.mailer = m } }

// WithStore overrides the object store (default a.Storage).
func WithStore(s ObjectStore) Option { return func(x *Module) { x.store = s } }

// WithGoogle overrides the Google OAuth provider.
func WithGoogle(g GoogleProvider) Option { return func(x *Module) { x.google = g } }

// WithTokenOptions tunes token TTL / touch interval / clock.
func WithTokenOptions(o auth.TokenOptions) Option { return func(x *Module) { x.tokOpts = o } }

// WithLimits overrides the rate limits.
func WithLimits(l Limits) Option { return func(x *Module) { x.limits = l } }

// WithClock overrides the clock used for signed links and reset tokens.
func WithClock(now func() time.Time) Option { return func(x *Module) { x.now = now } }

// New builds the module. roles is the RBAC implementation (internal/rbac).
func New(a *app.App, roles auth.RoleService, opts ...Option) *Module {
	m := &Module{cfg: a.Cfg, log: a.Log, db: a.DB, roles: roles, limits: DefaultLimits}
	if a.Storage != nil {
		m.store = a.Storage
	}
	for _, o := range opts {
		o(m)
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.now == nil {
		m.now = func() time.Time { return time.Now().UTC() }
	}
	if m.tokOpts.Now == nil {
		m.tokOpts.Now = m.now
	}
	m.tokens = auth.NewTokenService(a.DB, m.tokOpts)

	key := a.Cfg.App.Key
	if len(key) == 0 {
		// Dev only: production config validation requires APP_KEY.
		m.log.Warn("APP_KEY is empty; signing e-mail links with an insecure development key")
		sum := sha256.Sum256([]byte("insecure-dev-key|" + a.Cfg.App.Name))
		key = sum[:]
	}
	m.signer = auth.NewSigner(key, m.now)

	if m.mailer == nil {
		mm, err := mail.New(a.Cfg.Mail, m.log)
		if err != nil {
			m.log.Error("mail driver misconfigured, falling back to log driver", "error", err.Error())
			mm = &mail.LogMailer{Log: m.log, From: a.Cfg.Mail.FromAddress}
		}
		m.mailer = mm
	}
	if a.Redis != nil {
		m.limiter = ratelimit.New(a.Redis, a.Cfg.Redis.Prefix)
	}
	if m.google == nil && a.Cfg.Google.Enabled() {
		m.google = newGoogleOAuth(a.Cfg.Google)
	}
	m.media = newMediaResolver(m.store, a.Cfg.App.URL)
	return m
}

// Tokens exposes the token service (its Required/Optional middleware guard
// the other modules' routes).
func (m *Module) Tokens() *auth.TokenService { return m.tokens }

// Register implements app.Module.
func (m *Module) Register(api *gin.RouterGroup) {
	required := m.tokens.Required()
	perMin := func(name string, n int) gin.HandlerFunc {
		return m.limiter.Middleware("auth."+name, n, time.Minute, nil)
	}

	g := api.Group("/auth")
	g.POST("/register", perMin("register", m.limits.Register), m.register)
	g.POST("/login", m.login)
	g.POST("/forgot-password", perMin("forgot", m.limits.Sensitive), m.forgotPassword)
	g.POST("/reset-password", perMin("reset", m.limits.Sensitive), m.resetPassword)
	g.POST("/resend-verification-email", perMin("resend", m.limits.Sensitive), m.resendVerificationEmail)
	g.GET("/verify-email/:id/:hash", perMin("verify", m.limits.Sensitive), m.verifyEmail)
	g.GET("/google/url", m.googleLoginURL)
	g.GET("/google/redirect", m.redirectToGoogle)
	g.GET("/google/callback", m.handleGoogleCallback)

	ga := g.Group("", required)
	ga.POST("/logout", m.logout)
	ga.GET("/me", m.me)
	ga.PUT("/profile", m.updateProfile)
	ga.POST("/upload", m.uploadMedia)
	ga.PUT("/change-password", m.changePassword)

	// Backward-compatible aliases.
	api.POST("/register", perMin("register", m.limits.Register), m.register)
	api.POST("/login", m.login)
	api.POST("/logout", required, m.logout)
	api.GET("/me", required, m.me)
	api.GET("/user", required, m.rawUser)
}

var _ app.Module = (*Module)(nil)
