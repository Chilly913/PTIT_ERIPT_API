// Package config loads typed application configuration from environment
// variables. Required values are validated strictly when APP_ENV=production.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Config is the root configuration object.
type Config struct {
	App     AppConfig
	HTTP    HTTPConfig
	DB      DBConfig
	Redis   RedisConfig
	Storage StorageConfig
	Google  GoogleConfig
	Mail    MailConfig
	AI      AIConfig
	PayOS   PayOSConfig
	Log     LogConfig
}

type AppConfig struct {
	Name        string
	Env         string // production | staging | development | local | testing
	Debug       bool
	URL         string
	FrontendURL string
	// Key is the 32-byte key used for at-rest encryption (APP_KEY,
	// "base64:<...>" like Laravel or a raw 32-character string).
	Key []byte
}

// IsProduction reports whether strict production rules apply.
func (a AppConfig) IsProduction() bool { return a.Env == "production" }

// IsDev reports whether the app runs in a developer environment.
func (a AppConfig) IsDev() bool {
	return a.Env == "development" || a.Env == "local" || a.Env == "testing"
}

type HTTPConfig struct {
	Port                      string
	CORSAllowedOrigins        []string
	CORSAllowedOriginPatterns []*regexp.Regexp
	// TrustedProxies is passed to gin.Engine.SetTrustedProxies. Empty means
	// trust none; "*" means trust every proxy (Laravel's trustProxies(at: '*')).
	TrustedProxies  []string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration // 0 = unlimited (needed for SSE streaming)
	ShutdownTimeout time.Duration
}

type DBConfig struct {
	// URL, when set, is used verbatim (postgres://... or key=value DSN).
	URL      string
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string

	MaxOpenConns       int
	MaxIdleConns       int
	ConnMaxLifetime    time.Duration
	ConnMaxIdleTime    time.Duration
	SlowQueryThreshold time.Duration
	LogLevel           string // silent | error | warn | info
}

// DSN returns a pgx connection string.
func (d DBConfig) DSN() string {
	if d.URL != "" {
		return d.URL
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(d.User, d.Password),
		Host:   fmt.Sprintf("%s:%d", d.Host, d.Port),
		Path:   "/" + d.Name,
	}
	q := url.Values{}
	q.Set("sslmode", d.SSLMode)
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	return u.String()
}

type RedisConfig struct {
	URL      string // redis://... overrides the discrete fields
	Host     string
	Port     int
	Password string
	DB       int
	Prefix   string
}

// Addr returns host:port.
func (r RedisConfig) Addr() string { return fmt.Sprintf("%s:%d", r.Host, r.Port) }

type StorageConfig struct {
	Endpoint  string // host:port, without scheme
	UseSSL    bool
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	PathStyle bool
	// PublicURL is the base URL objects are reachable at, including the bucket
	// for path-style setups (e.g. https://eript-lms.io.vn/minio/lms-videos).
	// When empty it is derived from the endpoint.
	PublicURL     string
	PresignExpiry time.Duration
}

type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// Enabled reports whether Google OAuth is configured.
func (g GoogleConfig) Enabled() bool { return g.ClientID != "" && g.ClientSecret != "" }

type MailConfig struct {
	Mailer      string // smtp | log
	Host        string
	Port        int
	Username    string
	Password    string
	Encryption  string // "" | tls | starttls
	FromAddress string
	FromName    string
}

type AIConfig struct {
	URL          string
	SharedSecret string
	StreamRelay  bool
	Timeout      time.Duration
}

type PayOSConfig struct {
	ClientID    string
	APIKey      string
	ChecksumKey string
	ReturnURL   string
	CancelURL   string
	WebhookURL  string
}

// Enabled reports whether PayOS credentials are configured.
func (p PayOSConfig) Enabled() bool {
	return p.ClientID != "" && p.APIKey != "" && p.ChecksumKey != ""
}

type LogConfig struct {
	Level  string // debug | info | warn | error
	Format string // json | text
}

// Default CORS origin patterns, copied from the Laravel config/cors.php
// (Flutter web dev servers use a random localhost port; ngrok tunnels).
const defaultCORSPatterns = `^https?://localhost(:\d+)?$,^https?://127\.0\.0\.1(:\d+)?$,.*\.ngrok-free\.app$,.*\.ngrok\.io$`

// Load reads configuration from the process environment.
func Load() (Config, error) { return load(osEnv{}) }

// LoadFrom reads configuration from the given key/value map (tests).
func LoadFrom(m map[string]string) (Config, error) { return load(mapEnv(m)) }

func load(src envSource) (Config, error) {
	e := &env{src: src}
	appName := e.str("APP_NAME", "PTIT_ERIPT_API")
	cfg := Config{
		App: AppConfig{
			Name:        appName,
			Env:         strings.ToLower(e.str("APP_ENV", "development")),
			URL:         strings.TrimRight(e.str("APP_URL", "http://localhost:8080"), "/"),
			FrontendURL: strings.TrimRight(e.str("FRONTEND_URL", "http://localhost:3000"), "/"),
		},
		HTTP: HTTPConfig{
			Port:               e.str("PORT", "8080"),
			CORSAllowedOrigins: e.list("CORS_ALLOWED_ORIGINS", "http://localhost,http://localhost:3000,http://127.0.0.1:3000"),
			TrustedProxies:     e.list("TRUSTED_PROXIES", "127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"),
			ReadTimeout:        e.duration("HTTP_READ_TIMEOUT", 30*time.Second),
			WriteTimeout:       e.duration("HTTP_WRITE_TIMEOUT", 0),
			ShutdownTimeout:    e.duration("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
		},
		DB: DBConfig{
			URL:                e.str("DB_URL", ""),
			Host:               e.str("DB_HOST", "127.0.0.1"),
			Port:               e.int("DB_PORT", 5432),
			Name:               e.str("DB_DATABASE", "eript"),
			User:               e.str("DB_USERNAME", "eript"),
			Password:           e.str("DB_PASSWORD", ""),
			SSLMode:            e.str("DB_SSLMODE", "disable"),
			MaxOpenConns:       e.int("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:       e.int("DB_MAX_IDLE_CONNS", 10),
			ConnMaxLifetime:    e.duration("DB_CONN_MAX_LIFETIME", 30*time.Minute),
			ConnMaxIdleTime:    e.duration("DB_CONN_MAX_IDLE_TIME", 5*time.Minute),
			SlowQueryThreshold: e.duration("DB_SLOW_QUERY_THRESHOLD", 200*time.Millisecond),
			LogLevel:           strings.ToLower(e.str("DB_LOG_LEVEL", "warn")),
		},
		Redis: RedisConfig{
			URL:      e.str("REDIS_URL", ""),
			Host:     e.str("REDIS_HOST", "127.0.0.1"),
			Port:     e.int("REDIS_PORT", 6379),
			Password: e.str("REDIS_PASSWORD", ""),
			DB:       e.int("REDIS_DB", 0),
			Prefix:   e.str("REDIS_PREFIX", "eript:"),
		},
		Storage: StorageConfig{
			AccessKey:     e.str("S3_ACCESS_KEY", ""),
			SecretKey:     e.str("S3_SECRET_KEY", ""),
			Bucket:        e.str("S3_BUCKET", "lms-videos"),
			Region:        e.str("S3_REGION", "us-east-1"),
			PathStyle:     e.bool("S3_USE_PATH_STYLE", true),
			PublicURL:     strings.TrimRight(e.str("S3_PUBLIC_URL", ""), "/"),
			PresignExpiry: e.duration("S3_PRESIGN_EXPIRY", time.Hour),
		},
		Google: GoogleConfig{
			ClientID:     e.str("GOOGLE_CLIENT_ID", ""),
			ClientSecret: e.str("GOOGLE_CLIENT_SECRET", ""),
			RedirectURL:  e.str("GOOGLE_REDIRECT_URI", ""),
		},
		Mail: MailConfig{
			Mailer:      strings.ToLower(e.str("MAIL_MAILER", "log")),
			Host:        e.str("MAIL_HOST", "127.0.0.1"),
			Port:        e.int("MAIL_PORT", 2525),
			Username:    e.str("MAIL_USERNAME", ""),
			Password:    e.str("MAIL_PASSWORD", ""),
			Encryption:  strings.ToLower(e.str("MAIL_ENCRYPTION", "")),
			FromAddress: e.str("MAIL_FROM_ADDRESS", "noreply@lms.local"),
			FromName:    e.str("MAIL_FROM_NAME", appName),
		},
		AI: AIConfig{
			URL:          strings.TrimRight(e.str("AI_SERVICE_URL", "http://localhost:8001"), "/"),
			SharedSecret: e.str("AI_SERVICE_SHARED_SECRET", ""),
			StreamRelay:  e.bool("AI_STREAM_RELAY", false),
			Timeout:      e.duration("AI_SERVICE_TIMEOUT", 120*time.Second),
		},
		PayOS: PayOSConfig{
			ClientID:    e.str("PAYOS_CLIENT_ID", ""),
			APIKey:      e.str("PAYOS_API_KEY", ""),
			ChecksumKey: e.str("PAYOS_CHECKSUM_KEY", ""),
			ReturnURL:   e.str("PAYOS_RETURN_URL", "http://localhost/payment/payos/callback"),
			CancelURL:   e.str("PAYOS_CANCEL_URL", "http://localhost/payment/payos/callback?cancelled=1"),
			WebhookURL:  e.str("PAYOS_WEBHOOK_URL", "http://localhost/api/payos/webhook"),
		},
		Log: LogConfig{
			Level:  strings.ToLower(e.str("LOG_LEVEL", "info")),
			Format: strings.ToLower(e.str("LOG_FORMAT", "json")),
		},
	}
	cfg.App.Debug = e.bool("APP_DEBUG", cfg.App.IsDev())

	if raw := e.str("APP_KEY", ""); raw != "" {
		key, err := decodeAppKey(raw)
		if err != nil {
			e.errs = append(e.errs, err)
		}
		cfg.App.Key = key
	}

	for _, p := range e.list("CORS_ALLOWED_ORIGIN_PATTERNS", defaultCORSPatterns) {
		re, err := regexp.Compile(p)
		if err != nil {
			e.errs = append(e.errs, fmt.Errorf("CORS_ALLOWED_ORIGIN_PATTERNS: invalid regexp %q: %w", p, err))
			continue
		}
		cfg.HTTP.CORSAllowedOriginPatterns = append(cfg.HTTP.CORSAllowedOriginPatterns, re)
	}

	// S3 endpoint: accept "http://minio:9000" (Laravel AWS_ENDPOINT style) or "minio:9000".
	endpoint := e.str("S3_ENDPOINT", "localhost:9000")
	cfg.Storage.UseSSL = e.bool("S3_USE_SSL", strings.HasPrefix(endpoint, "https://"))
	endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	cfg.Storage.Endpoint = strings.TrimRight(endpoint, "/")

	if err := cfg.validate(); err != nil {
		e.errs = append(e.errs, err)
	}
	if len(e.errs) > 0 {
		return cfg, fmt.Errorf("invalid configuration: %w", errors.Join(e.errs...))
	}
	return cfg, nil
}

func decodeAppKey(raw string) ([]byte, error) {
	key := []byte(raw)
	if b64, ok := strings.CutPrefix(raw, "base64:"); ok {
		k, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("APP_KEY: invalid base64: %w", err)
		}
		key = k
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("APP_KEY: must decode to 32 bytes (got %d); generate with `echo base64:$(openssl rand -base64 32)`", len(key))
	}
	return key, nil
}

func (c Config) validate() error {
	var errs []error
	req := func(ok bool, msg string) {
		if !ok {
			errs = append(errs, errors.New(msg))
		}
	}

	switch c.App.Env {
	case "production", "staging", "development", "local", "testing":
	default:
		errs = append(errs, fmt.Errorf("APP_ENV: unknown value %q", c.App.Env))
	}
	switch c.DB.LogLevel {
	case "silent", "error", "warn", "info":
	default:
		errs = append(errs, fmt.Errorf("DB_LOG_LEVEL: unknown value %q", c.DB.LogLevel))
	}
	req(c.DB.URL != "" || (c.DB.Host != "" && c.DB.Name != "" && c.DB.User != ""), "DB_URL or DB_HOST/DB_DATABASE/DB_USERNAME is required")
	req(c.DB.MaxOpenConns > 0, "DB_MAX_OPEN_CONNS must be > 0")
	req(c.DB.MaxIdleConns >= 0 && c.DB.MaxIdleConns <= c.DB.MaxOpenConns, "DB_MAX_IDLE_CONNS must be between 0 and DB_MAX_OPEN_CONNS")

	if c.App.IsProduction() {
		req(len(c.App.Key) == 32, "APP_KEY is required in production")
		req(!c.App.Debug, "APP_DEBUG must be false in production")
		req(strings.HasPrefix(c.App.URL, "https://"), "APP_URL must be an https URL in production")
		req(c.App.FrontendURL != "", "FRONTEND_URL is required in production")
		req(c.DB.URL != "" || c.DB.Password != "", "DB_PASSWORD (or DB_URL) is required in production")
		req(len(c.HTTP.CORSAllowedOrigins) > 0, "CORS_ALLOWED_ORIGINS is required in production")
		for _, o := range c.HTTP.CORSAllowedOrigins {
			req(o != "*", "CORS_ALLOWED_ORIGINS must not contain * in production (credentials are allowed)")
		}
		req(c.Storage.AccessKey != "" && c.Storage.SecretKey != "", "S3_ACCESS_KEY and S3_SECRET_KEY are required in production")
		req(c.Storage.Bucket != "", "S3_BUCKET is required in production")
		req(c.AI.SharedSecret != "", "AI_SERVICE_SHARED_SECRET is required in production")
		if c.Mail.Mailer == "smtp" {
			req(c.Mail.Host != "" && c.Mail.FromAddress != "", "MAIL_HOST and MAIL_FROM_ADDRESS are required when MAIL_MAILER=smtp")
		}
		if c.Google.ClientID != "" {
			req(c.Google.ClientSecret != "" && c.Google.RedirectURL != "", "GOOGLE_CLIENT_SECRET and GOOGLE_REDIRECT_URI are required when GOOGLE_CLIENT_ID is set")
		}
		if c.PayOS.ClientID != "" {
			req(c.PayOS.APIKey != "" && c.PayOS.ChecksumKey != "", "PAYOS_API_KEY and PAYOS_CHECKSUM_KEY are required when PAYOS_CLIENT_ID is set")
		}
	}
	return errors.Join(errs...)
}
