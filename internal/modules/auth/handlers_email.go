package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/mail"
)

// Laravel lang/en/passwords.php (APP_LOCALE=en) and AuthController messages.
const (
	msgResetSent        = "We have emailed your password reset link."
	msgResetThrottled   = "Please wait before retrying."
	msgResetDone        = "Your password has been reset."
	msgResetBadToken    = "This password reset token is invalid."
	msgResetNoUser      = "We can't find a user with that email address."
	msgResendNoUser     = "Không tìm thấy tài khoản phù hợp."
	msgAlreadyVerified  = "Email này đã được xác nhận."
	msgResent           = "Đã gửi lại email xác nhận. Vui lòng kiểm tra hộp thư của bạn."
	msgInvalidSignature = "Invalid signature."
	msgVerifyBadLink    = "Liên kết xác nhận không hợp lệ."
	msgVerifyExpired    = "Liên kết xác nhận đã hết hạn hoặc không hợp lệ."
	msgVerified         = "Xác nhận email thành công. Bạn có thể đăng nhập ngay bây giờ."
)

const (
	purposePasswordReset = "password_reset"
)

// verificationURL is the frontend link Laravel's VerifyEmail::createUrlUsing built:
// {FRONTEND_URL}/verify-email?expires=..&signature=..&id=..&hash=..
func (m *Module) verificationURL(u *auth.User) string {
	l := m.signer.SignVerification(u.ID, u.Email, verificationExpire)
	return fmt.Sprintf("%s/verify-email?expires=%d&signature=%s&id=%d&hash=%s",
		m.cfg.App.FrontendURL, l.Expires, l.Signature, l.ID, l.Hash)
}

func (m *Module) sendVerification(ctx context.Context, u *auth.User) error {
	msg, err := mail.VerifyEmail(m.cfg.App.Name, u.Email, u.Name, m.verificationURL(u), int(verificationExpire.Minutes()))
	if err != nil {
		return err
	}
	return m.mailer.Send(ctx, msg)
}

type emailRequest struct {
	Email string `json:"email" form:"email" binding:"required,email"`
}

func (m *Module) forgotPassword(c *gin.Context) {
	ctx := c.Request.Context()
	var req emailRequest
	if !httpx.Bind(c, &req) {
		return
	}
	u, err := auth.FindUserByEmail(ctx, m.db, req.Email)
	if errors.Is(err, auth.ErrUserNotFound) {
		httpx.Abort(c, http.StatusUnprocessableEntity, msgResetNoUser)
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	now := m.now()
	var recent bool
	if err := m.db.WithContext(ctx).Raw(`
SELECT EXISTS (SELECT 1 FROM user_action_tokens
WHERE user_id = ? AND purpose = ? AND consumed_at IS NULL AND created_at > ?)`,
		u.ID, purposePasswordReset, now.Add(-passwordResetThrottle)).Scan(&recent).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if recent {
		httpx.Abort(c, http.StatusUnprocessableEntity, msgResetThrottled)
		return
	}
	plain, err := auth.GenerateToken()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Laravel's token repository keeps one pending token per user.
		if err := tx.Exec(`DELETE FROM user_action_tokens WHERE user_id = ? AND purpose = ? AND consumed_at IS NULL`,
			u.ID, purposePasswordReset).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO user_action_tokens (user_id, purpose, token_hash, email, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, u.ID, purposePasswordReset, auth.HashToken(plain), u.Email, now.Add(passwordResetExpire), now).Error
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	link := fmt.Sprintf("%s/reset-password?token=%s&email=%s", m.cfg.App.FrontendURL, plain, url.QueryEscape(u.Email))
	msg, err := mail.PasswordReset(m.cfg.App.Name, u.Email, u.Name, link, int(passwordResetExpire.Minutes()))
	if err == nil {
		err = m.mailer.Send(ctx, msg)
	}
	if err != nil {
		httpx.Fail(c, fmt.Errorf("send password reset e-mail: %w", err))
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msgResetSent})
}

type resetPasswordRequest struct {
	Token                string `json:"token" form:"token" binding:"required"`
	Email                string `json:"email" form:"email" binding:"required,email"`
	Password             string `json:"password" form:"password" binding:"required,min=6"`
	PasswordConfirmation string `json:"password_confirmation" form:"password_confirmation" binding:"eqfield=Password"`
}

func (m *Module) resetPassword(c *gin.Context) {
	ctx := c.Request.Context()
	var req resetPasswordRequest
	if !httpx.Bind(c, &req) {
		return
	}
	u, err := auth.FindUserByEmail(ctx, m.db, req.Email)
	if errors.Is(err, auth.ErrUserNotFound) {
		httpx.Abort(c, http.StatusUnprocessableEntity, msgResetNoUser)
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	now := m.now()
	var consumed bool
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Single use: the UPDATE ... RETURNING claims the token atomically.
		var ids []int64
		if err := tx.Raw(`
UPDATE user_action_tokens SET consumed_at = ?
WHERE user_id = ? AND purpose = ? AND token_hash = ? AND email = ?
  AND consumed_at IS NULL AND expires_at > ?
RETURNING id`, now, u.ID, purposePasswordReset, auth.HashToken(req.Token), u.Email, now).Scan(&ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		consumed = true
		if err := tx.Exec(`DELETE FROM user_action_tokens WHERE user_id = ? AND purpose = ? AND consumed_at IS NULL`,
			u.ID, purposePasswordReset).Error; err != nil {
			return err
		}
		if err := tx.Model(u).Update("password_hash", hash).Error; err != nil {
			return err
		}
		// Deliberate hardening: a password reset signs out every session.
		return m.tokens.RevokeAllForUser(ctx, tx, u.ID)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !consumed {
		httpx.Abort(c, http.StatusUnprocessableEntity, msgResetBadToken)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msgResetDone})
}

func (m *Module) resendVerificationEmail(c *gin.Context) {
	ctx := c.Request.Context()
	var req emailRequest
	if !httpx.Bind(c, &req) {
		return
	}
	u, err := auth.FindUserByEmail(ctx, m.db, req.Email)
	if errors.Is(err, auth.ErrUserNotFound) {
		httpx.Abort(c, http.StatusNotFound, msgResendNoUser)
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if u.HasVerifiedEmail() {
		c.JSON(http.StatusOK, gin.H{"message": msgAlreadyVerified})
		return
	}
	if err := m.sendVerification(ctx, u); err != nil {
		httpx.Fail(c, fmt.Errorf("send verification e-mail: %w", err))
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msgResent})
}

// verifyEmail is GET /auth/verify-email/{id}/{hash}?expires=&signature=.
func (m *Module) verifyEmail(c *gin.Context) {
	ctx := c.Request.Context()
	idParam, hash := c.Param("id"), c.Param("hash")
	// Laravel's `signed` middleware runs before the controller.
	if !m.signer.ValidVerification(idParam, hash, c.Query("expires"), c.Query("signature")) {
		httpx.Abort(c, http.StatusForbidden, msgInvalidSignature)
		return
	}
	id, err := strconv.ParseInt(idParam, 10, 64)
	if err != nil {
		httpx.Abort(c, http.StatusNotFound, msgVerifyBadLink)
		return
	}
	u, err := auth.FindUserByID(ctx, m.db, id)
	if errors.Is(err, auth.ErrUserNotFound) {
		httpx.Abort(c, http.StatusNotFound, msgVerifyBadLink)
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if hash != auth.EmailHash(u.Email) {
		httpx.Abort(c, http.StatusForbidden, msgVerifyExpired)
		return
	}
	if !u.HasVerifiedEmail() {
		if err := m.db.WithContext(ctx).Model(u).Update("email_verified_at", m.now().Truncate(time.Microsecond)).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": msgVerified, "verified": true})
}
