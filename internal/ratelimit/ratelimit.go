// Package ratelimit is a Redis fixed-window limiter with Laravel
// ThrottleRequests-compatible responses (429 {"message":"Too Many Attempts."},
// Retry-After, X-RateLimit-Limit/Remaining/Reset headers).
package ratelimit

import (
	"context"
	"crypto/sha1" //nolint:gosec // key hashing only
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

// hitScript increments the counter, starts the window on the first hit and
// returns {count, ttl_ms}.
var hitScript = redis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if c == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then redis.call('PEXPIRE', KEYS[1], ARGV[1]); ttl = tonumber(ARGV[1]) end
return {c, ttl}
`)

// Limiter counts attempts per key. A nil *Limiter or nil Redis client allows
// everything; Redis errors fail open (logged).
type Limiter struct {
	rdb    *redis.Client
	prefix string
}

// New returns a limiter; prefix namespaces keys (e.g. REDIS_PREFIX).
func New(rdb *redis.Client, prefix string) *Limiter {
	return &Limiter{rdb: rdb, prefix: prefix + "ratelimit:"}
}

// Result of an attempt.
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
}

func (l *Limiter) key(name, key string) string {
	sum := sha1.Sum([]byte(key)) //nolint:gosec
	return l.prefix + name + ":" + hex.EncodeToString(sum[:])
}

// Hit records one attempt and reports whether it is within max per window.
func (l *Limiter) Hit(ctx context.Context, name, key string, max int, window time.Duration) (Result, error) {
	if l == nil || l.rdb == nil || max <= 0 {
		return Result{Allowed: true, Limit: max, Remaining: max}, nil
	}
	vals, err := hitScript.Run(ctx, l.rdb, []string{l.key(name, key)}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return Result{Allowed: true, Limit: max, Remaining: max}, fmt.Errorf("ratelimit: %w", err)
	}
	count, ttl := int(vals[0]), time.Duration(vals[1])*time.Millisecond
	res := Result{Allowed: count <= max, Limit: max, Remaining: max - count, RetryAfter: ttl}
	if res.Remaining < 0 {
		res.Remaining = 0
	}
	return res, nil
}

// TooMany reports whether key already used max attempts, without counting.
func (l *Limiter) TooMany(ctx context.Context, name, key string, max int) (bool, time.Duration, error) {
	if l == nil || l.rdb == nil || max <= 0 {
		return false, 0, nil
	}
	k := l.key(name, key)
	pipe := l.rdb.Pipeline()
	get := pipe.Get(ctx, k)
	ttl := pipe.PTTL(ctx, k)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return false, 0, fmt.Errorf("ratelimit: %w", err)
	}
	n, _ := get.Int()
	return n >= max, ttl.Val(), nil
}

// Clear resets key (e.g. after a successful login).
func (l *Limiter) Clear(ctx context.Context, name, key string) error {
	if l == nil || l.rdb == nil {
		return nil
	}
	return l.rdb.Del(ctx, l.key(name, key)).Err()
}

// Middleware throttles requests by keyFn (default: client IP), max per window,
// like Laravel's throttle:max,minutes middleware.
func (l *Limiter) Middleware(name string, max int, window time.Duration, keyFn func(*gin.Context) string) gin.HandlerFunc {
	if keyFn == nil {
		keyFn = func(c *gin.Context) string { return c.ClientIP() }
	}
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		res, err := l.Hit(ctx, name, keyFn(c), max, window)
		if err != nil {
			logx.FromContext(ctx).WarnContext(ctx, "rate limiter unavailable, allowing request", "limiter", name, "error", err.Error())
			c.Next()
			return
		}
		if !res.Allowed {
			Reject(c, res.Limit, res.RetryAfter)
			return
		}
		c.Header("X-RateLimit-Limit", strconv.Itoa(res.Limit))
		c.Header("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
		c.Next()
	}
}

// Reject writes Laravel's 429 response with rate-limit headers.
func Reject(c *gin.Context, limit int, retryAfter time.Duration) {
	secs := int((retryAfter + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	c.Header("X-RateLimit-Limit", strconv.Itoa(limit))
	c.Header("X-RateLimit-Remaining", "0")
	c.Header("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Duration(secs)*time.Second).Unix(), 10))
	httpx.TooManyRequests(c, secs)
}

// LogError logs a limiter error at warn level (fail-open helper).
func LogError(ctx context.Context, name string, err error) {
	if err != nil {
		logx.FromContext(ctx).WarnContext(ctx, "rate limiter unavailable", "limiter", name, "error", err.Error())
	}
}
