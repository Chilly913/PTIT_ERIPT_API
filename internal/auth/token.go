package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Token defaults: long-lived opaque bearer tokens with sliding expiry.
const (
	DefaultTokenTTL           = 30 * 24 * time.Hour
	DefaultTokenTouchInterval = 5 * time.Minute
	DefaultTokenName          = "auth-token"
)

// ErrInvalidToken is returned for unknown, revoked or expired tokens and for
// tokens of soft-deleted users.
var ErrInvalidToken = errors.New("auth: invalid token")

// TokenOptions tunes the TokenService; zero values use the defaults.
type TokenOptions struct {
	// TTL is the idle lifetime; every use (at most once per TouchInterval)
	// pushes expires_at to now+TTL.
	TTL time.Duration
	// TouchInterval throttles the last_used_at/expires_at write per token.
	TouchInterval time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
}

// TokenService issues, verifies and revokes opaque access tokens stored as
// SHA-256 digests in access_tokens.
type TokenService struct {
	db    *gorm.DB
	ttl   time.Duration
	touch time.Duration
	now   func() time.Time
}

// NewTokenService builds a service over db.
func NewTokenService(db *gorm.DB, opts TokenOptions) *TokenService {
	s := &TokenService{db: db, ttl: opts.TTL, touch: opts.TouchInterval, now: opts.Now}
	if s.ttl <= 0 {
		s.ttl = DefaultTokenTTL
	}
	if s.touch <= 0 {
		s.touch = DefaultTokenTouchInterval
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	return s
}

// TTL returns the sliding lifetime.
func (s *TokenService) TTL() time.Duration { return s.ttl }

// IssuedToken is a freshly created token; PlainText is shown to the client once.
type IssuedToken struct {
	ID        int64
	UserID    int64
	PlainText string
	ExpiresAt time.Time
}

// GenerateToken returns 32 random bytes, base64url encoded (43 chars).
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the SHA-256 digest stored in the database.
func HashToken(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

// BearerToken extracts the token from "Authorization: Bearer <token>".
func BearerToken(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// Issue creates a token for userID. db may be a transaction; nil uses the
// service's connection.
func (s *TokenService) Issue(ctx context.Context, db *gorm.DB, userID int64, name string) (IssuedToken, error) {
	if db == nil {
		db = s.db
	}
	if name == "" {
		name = DefaultTokenName
	}
	plain, err := GenerateToken()
	if err != nil {
		return IssuedToken{}, err
	}
	exp := s.now().Add(s.ttl)
	var id int64
	err = db.WithContext(ctx).Raw(
		`INSERT INTO access_tokens (user_id, token_hash, name, expires_at) VALUES (?, ?, ?, ?) RETURNING id`,
		userID, HashToken(plain), name, exp,
	).Scan(&id).Error
	if err != nil {
		return IssuedToken{}, fmt.Errorf("auth: issue token: %w", err)
	}
	return IssuedToken{ID: id, UserID: userID, PlainText: plain, ExpiresAt: exp}, nil
}

type tokenRow struct {
	ID         int64
	UserID     int64
	ExpiresAt  time.Time
	LastUsedAt *time.Time
}

// needsTouch reports whether the sliding-expiry write is due.
func needsTouch(lastUsed *time.Time, now time.Time, interval time.Duration) bool {
	return lastUsed == nil || now.Sub(*lastUsed) >= interval
}

// Verify resolves a plaintext token to its principal. Revoked, expired,
// soft-deleted-user and disabled-user tokens yield ErrInvalidToken. Expiry slides to now+TTL,
// written at most once per TouchInterval.
func (s *TokenService) Verify(ctx context.Context, plain string) (Principal, error) {
	if plain == "" || len(plain) > 512 {
		return Principal{}, ErrInvalidToken
	}
	now := s.now()
	var rows []tokenRow
	err := s.db.WithContext(ctx).Raw(`
SELECT t.id, t.user_id, t.expires_at, t.last_used_at
FROM access_tokens t
JOIN users u ON u.id = t.user_id AND u.deleted_at IS NULL AND u.disabled_at IS NULL
WHERE t.token_hash = ? AND t.revoked_at IS NULL AND t.expires_at > ?
LIMIT 1`, HashToken(plain), now).Scan(&rows).Error
	if err != nil {
		return Principal{}, fmt.Errorf("auth: verify token: %w", err)
	}
	if len(rows) == 0 {
		return Principal{}, ErrInvalidToken
	}
	t := rows[0]
	if needsTouch(t.LastUsedAt, now, s.touch) {
		// Guarded by last_used_at so concurrent requests write once.
		err := s.db.WithContext(ctx).Exec(`
UPDATE access_tokens SET last_used_at = ?, expires_at = ?
WHERE id = ? AND revoked_at IS NULL AND (last_used_at IS NULL OR last_used_at <= ?)`,
			now, now.Add(s.ttl), t.ID, now.Add(-s.touch)).Error
		if err != nil {
			return Principal{}, fmt.Errorf("auth: touch token: %w", err)
		}
	}
	return Principal{UserID: t.UserID, TokenID: t.ID}, nil
}

// Revoke revokes one token (logout).
func (s *TokenService) Revoke(ctx context.Context, tokenID int64) error {
	return s.db.WithContext(ctx).Exec(
		`UPDATE access_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, s.now(), tokenID).Error
}

// RevokeAllForUser revokes every active token of userID. db may be a transaction.
func (s *TokenService) RevokeAllForUser(ctx context.Context, db *gorm.DB, userID int64) error {
	if db == nil {
		db = s.db
	}
	return db.WithContext(ctx).Exec(
		`UPDATE access_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, s.now(), userID).Error
}

// PurgeExpired deletes tokens that expired (or were revoked) before cutoff.
// Intended for a periodic worker job.
func (s *TokenService) PurgeExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Exec(
		`DELETE FROM access_tokens WHERE expires_at < ? OR revoked_at < ?`, cutoff, cutoff)
	return res.RowsAffected, res.Error
}
