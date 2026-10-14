package auth

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // sha1 of the e-mail is the legacy verify-email {hash} segment, not a security primitive
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Signer produces HMAC-SHA256 signatures with the application key. It
// replaces Laravel's URL::temporarySignedRoute for e-mail verification links
// and signs the OAuth state.
type Signer struct {
	key []byte
	now func() time.Time
}

// NewSigner returns a signer for key (APP_KEY bytes). now may be nil.
func NewSigner(key []byte, now func() time.Time) *Signer {
	if now == nil {
		now = time.Now
	}
	return &Signer{key: append([]byte(nil), key...), now: now}
}

// Sign returns the hex HMAC of the parts joined with "|".
func (s *Signer) Sign(parts ...string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(m.Sum(nil))
}

// Valid checks sig in constant time.
func (s *Signer) Valid(sig string, parts ...string) bool {
	want := s.Sign(parts...)
	return hmac.Equal([]byte(strings.ToLower(sig)), []byte(want))
}

// EmailHash is sha1(email) as used in the legacy verify-email/{id}/{hash} route.
func EmailHash(email string) string {
	sum := sha1.Sum([]byte(email)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// VerificationLink holds the signed parameters of an e-mail verification link
// (same query parameter names as Laravel: id, hash, expires, signature).
type VerificationLink struct {
	ID        int64
	Hash      string
	Expires   int64
	Signature string
}

// SignVerification signs a verification link for userID/email valid for ttl.
func (s *Signer) SignVerification(userID int64, email string, ttl time.Duration) VerificationLink {
	exp := s.now().Add(ttl).Unix()
	hash := EmailHash(email)
	return VerificationLink{
		ID:        userID,
		Hash:      hash,
		Expires:   exp,
		Signature: s.Sign("verify-email", strconv.FormatInt(userID, 10), hash, strconv.FormatInt(exp, 10)),
	}
}

// ValidVerification checks the signature and expiry of a verification link
// (Laravel's `signed` middleware). id and hash are the raw route segments.
func (s *Signer) ValidVerification(id, hash, expires, signature string) bool {
	if signature == "" || expires == "" {
		return false
	}
	exp, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || s.now().Unix() > exp {
		return false
	}
	return s.Valid(signature, "verify-email", id, hash, expires)
}
