package auth

import (
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost matches Laravel's default (BCRYPT_ROUNDS=12).
const BcryptCost = 12

// bcryptMaxBytes is the bcrypt input limit. PHP's crypt() silently ignores
// bytes past 72, Go's bcrypt rejects them; truncating keeps both compatible.
const bcryptMaxBytes = 72

func clampPassword(pw string) []byte {
	b := []byte(pw)
	if len(b) > bcryptMaxBytes {
		b = b[:bcryptMaxBytes]
	}
	return b
}

// HashPassword returns a bcrypt ($2a$, cost 12) hash of pw.
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(clampPassword(pw), BcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword reports whether pw matches hash. It accepts Go ($2a$) and
// PHP/Laravel ($2y$, $2b$) bcrypt hashes; an empty hash never matches
// (OAuth-only accounts have no password).
func CheckPassword(hash, pw string) bool {
	if hash == "" {
		return false
	}
	// $2y$ (PHP) and $2b$ are the same algorithm as $2a$ for valid UTF-8 input.
	if strings.HasPrefix(hash, "$2y$") || strings.HasPrefix(hash, "$2b$") {
		hash = "$2a$" + hash[4:]
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), clampPassword(pw)) == nil
}
