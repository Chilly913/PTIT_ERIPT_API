package auth

import (
	"context"
	"fmt"
	"net/url"

	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/mail"
)

// SendPasswordReset e-mails a password reset link to the user (used by the admin "reset password" action).
// Unlike the public forgot-password endpoint it is not throttled; any pending token is replaced.
func (m *Module) SendPasswordReset(ctx context.Context, userID int64) error {
	var email string
	if err := m.db.WithContext(ctx).Raw("SELECT email::text FROM users WHERE id = ? AND deleted_at IS NULL", userID).Scan(&email).Error; err != nil {
		return err
	}
	if email == "" {
		return auth.ErrUserNotFound
	}
	u, err := auth.FindUserByEmail(ctx, m.db, email)
	if err != nil {
		return err
	}
	plain, err := auth.GenerateToken()
	if err != nil {
		return err
	}
	now := m.now()
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM user_action_tokens WHERE user_id = ? AND purpose = ? AND consumed_at IS NULL`, u.ID, purposePasswordReset).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO user_action_tokens (user_id, purpose, token_hash, email, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			u.ID, purposePasswordReset, auth.HashToken(plain), u.Email, now.Add(passwordResetExpire), now).Error
	})
	if err != nil {
		return err
	}
	link := fmt.Sprintf("%s/reset-password?token=%s&email=%s", m.cfg.App.FrontendURL, plain, url.QueryEscape(u.Email))
	msg, err := mail.PasswordReset(m.cfg.App.Name, u.Email, u.Name, link, int(passwordResetExpire.Minutes()))
	if err != nil {
		return err
	}
	return m.mailer.Send(ctx, msg)
}
