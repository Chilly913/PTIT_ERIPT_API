package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/ratelimit"
)

// Legacy response messages.
const (
	msgRegistered       = "Đăng ký thành công. Vui lòng kiểm tra email để xác nhận tài khoản."
	msgUnverifiedLogin  = "Email của bạn chưa được xác nhận. Chúng tôi đã gửi lại email xác nhận."
	msgAccountDisabled  = "Tài khoản của bạn đã bị khóa. Vui lòng liên hệ quản trị viên."
	msgInvalidLogin     = "Invalid email or password"
	msgLoginSuccess     = "Login success"
	msgLogoutSuccess    = "Logout success"
	msgProfileUpdated   = "Profile updated"
	msgWrongCurrentPass = "Current password is incorrect"
	msgPasswordChanged  = "Password changed successfully"
)

type registerRequest struct {
	Name                 string `json:"name" form:"name" binding:"required,max=255"`
	Email                string `json:"email" form:"email" binding:"required,email,max=255"`
	Password             string `json:"password" form:"password" binding:"required,min=6"`
	PasswordConfirmation string `json:"password_confirmation" form:"password_confirmation" binding:"eqfield=Password"`
}

func (m *Module) register(c *gin.Context) {
	ctx := c.Request.Context()
	var req registerRequest
	ok := bindWith(c, &req, []string{"name", "email", "password"}, func(bag *httpx.ValidationError) error {
		if req.Email == "" {
			return nil
		}
		taken, err := auth.EmailExists(ctx, m.db, req.Email)
		if taken {
			bag.AddRule("email", "unique", nil)
		}
		return err
	})
	if !ok {
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	u := auth.User{Name: req.Name, Email: req.Email, PasswordHash: &hash, UserType: "student"}
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&u).Error; err != nil {
			return err
		}
		return m.assignDefaultRole(ctx, tx, u.ID, nil)
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		httpx.Fail(c, httpx.NewValidationError().AddRule("email", "unique", nil))
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := m.sendVerification(ctx, &u); err != nil {
		logx.FromContext(ctx).ErrorContext(ctx, "send verification e-mail failed", "user_id", u.ID, "error", err.Error())
	}
	c.JSON(http.StatusCreated, gin.H{
		"message":               msgRegistered,
		"email":                 u.Email,
		"requires_verification": true,
	})
}

// assignDefaultRole: users without admin/instructor/student get "student"
// when that role exists. roles may be nil (loaded on demand).
func (m *Module) assignDefaultRole(ctx context.Context, tx *gorm.DB, userID int64, roles []string) error {
	if roles == nil {
		var err error
		if roles, _, err = m.roles.UserRolesPermissions(ctx, userID); err != nil {
			return err
		}
	}
	if auth.HasRole(roles, auth.RoleAdmin, auth.RoleInstructor, auth.RoleStudent) {
		return nil
	}
	var exists bool
	if err := tx.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM roles WHERE name = ?)`, auth.RoleStudent).Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return m.roles.AssignRole(ctx, tx, userID, auth.RoleStudent)
}

type loginRequest struct {
	Email    string `json:"email" form:"email" binding:"required,email"`
	Password string `json:"password" form:"password" binding:"required"`
}

func (m *Module) login(c *gin.Context) {
	ctx := c.Request.Context()
	var req loginRequest
	if !httpx.Bind(c, &req) {
		return
	}
	throttleKey := strings.ToLower(req.Email) + "|" + c.ClientIP()
	tooMany, retry, err := m.limiter.TooMany(ctx, "auth.login", throttleKey, m.limits.LoginFailures)
	ratelimit.LogError(ctx, "auth.login", err)
	if tooMany {
		ratelimit.Reject(c, m.limits.LoginFailures, retry)
		return
	}
	failed := func() {
		_, err := m.limiter.Hit(ctx, "auth.login", throttleKey, m.limits.LoginFailures, time.Minute)
		ratelimit.LogError(ctx, "auth.login", err)
	}

	u, err := auth.FindUserByEmail(ctx, m.db, req.Email)
	if err != nil && !errors.Is(err, auth.ErrUserNotFound) {
		httpx.Fail(c, err)
		return
	}
	var roles []string
	if u != nil {
		if roles, _, err = m.roles.UserRolesPermissions(ctx, u.ID); err != nil {
			httpx.Fail(c, err)
			return
		}
		if !u.HasVerifiedEmail() && !auth.HasRole(roles, auth.RoleAdmin) {
			failed() // each attempt sends an e-mail; count it
			if err := m.sendVerification(ctx, u); err != nil {
				logx.FromContext(ctx).ErrorContext(ctx, "send verification e-mail failed", "user_id", u.ID, "error", err.Error())
			}
			c.JSON(http.StatusForbidden, gin.H{
				"message":               msgUnverifiedLogin,
				"requires_verification": true,
				"email":                 u.Email,
			})
			return
		}
	}
	if u == nil || !auth.CheckPassword(u.PasswordHashValue(), req.Password) {
		failed()
		httpx.Abort(c, http.StatusUnauthorized, msgInvalidLogin)
		return
	}
	_ = m.limiter.Clear(ctx, "auth.login", throttleKey)
	// Checked after the password so a locked account is not revealed to guessers.
	if u.IsDisabled() {
		httpx.Abort(c, http.StatusForbidden, msgAccountDisabled)
		return
	}
	m.respondWithToken(c, u, roles, msgLoginSuccess)
}

// respondWithToken assigns the default role, issues a token and renders the
// login payload {message, access_token, token_type, user}.
func (m *Module) respondWithToken(c *gin.Context, u *auth.User, roles []string, message string) {
	ctx := c.Request.Context()
	var tok auth.IssuedToken
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := m.assignDefaultRole(ctx, tx, u.ID, roles); err != nil {
			return err
		}
		var err error
		tok, err = m.tokens.Issue(ctx, tx, u.ID, auth.DefaultTokenName)
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	payload, err := m.serializeUser(ctx, u)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":      message,
		"access_token": tok.PlainText,
		"token_type":   "Bearer",
		"user":         payload,
	})
}

func (m *Module) me(c *gin.Context) {
	u, ok := m.currentUser(c)
	if !ok {
		return
	}
	payload, err := m.serializeUser(c.Request.Context(), u)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

// rawUser is GET /api/user (Route::get('/user', fn ($r) => $r->user())).
func (m *Module) rawUser(c *gin.Context) {
	u, ok := m.currentUser(c)
	if !ok {
		return
	}
	payload, err := m.rawUserJSON(c.Request.Context(), u)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (m *Module) logout(c *gin.Context) {
	p, _ := auth.PrincipalFrom(c)
	if err := m.tokens.Revoke(c.Request.Context(), p.TokenID); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msgLogoutSuccess})
}

// optString records whether a JSON key was present (null included).
type optString struct {
	Set   bool
	Value *string
}

func (o *optString) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return &json.UnmarshalTypeError{Value: "non-string", Type: reflect.TypeOf("")} // decoder fills Field
	}
	o.Value = &s
	return nil
}

type profileRequest struct {
	Name   string    `json:"name" form:"name" binding:"required,max=255"`
	Avatar optString `json:"avatar" form:"-"`
}

func (m *Module) updateProfile(c *gin.Context) {
	ctx := c.Request.Context()
	u, ok := m.currentUser(c)
	if !ok {
		return
	}
	var req profileRequest
	if !bindWith(c, &req, []string{"name", "avatar"}, func(bag *httpx.ValidationError) error {
		if req.Avatar.Value != nil {
			v := strings.TrimSpace(*req.Avatar.Value)
			if v == "" {
				req.Avatar.Value = nil // ConvertEmptyStringsToNull
			} else {
				req.Avatar.Value = &v
				if utf8.RuneCountInString(v) > 2048 {
					bag.Add("avatar", "The avatar field must not be greater than 2048 characters.")
				}
			}
		}
		return nil
	}) {
		return
	}
	updates := map[string]any{"name": req.Name}
	// Deliberate fix: legacy nulled the avatar when the key was omitted (the
	// mobile app sends only name/phone); only an explicit value changes it.
	if req.Avatar.Set {
		if req.Avatar.Value == nil {
			updates["avatar_path"] = nil
		} else {
			updates["avatar_path"] = m.media.StorageKey(*req.Avatar.Value)
		}
	}
	if err := m.db.WithContext(ctx).Model(u).Updates(updates).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	u, ok = m.currentUser(c)
	if !ok {
		return
	}
	payload, err := m.serializeUser(ctx, u)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msgProfileUpdated, "user": payload})
}

type changePasswordRequest struct {
	CurrentPassword      string `json:"current_password" form:"current_password" binding:"required"`
	Password             string `json:"password" form:"password" binding:"required,min=6"`
	PasswordConfirmation string `json:"password_confirmation" form:"password_confirmation" binding:"eqfield=Password"`
}

func (m *Module) changePassword(c *gin.Context) {
	ctx := c.Request.Context()
	u, ok := m.currentUser(c)
	if !ok {
		return
	}
	var req changePasswordRequest
	if !httpx.Bind(c, &req) {
		return
	}
	if !auth.CheckPassword(u.PasswordHashValue(), req.CurrentPassword) {
		httpx.Abort(c, http.StatusUnprocessableEntity, msgWrongCurrentPass)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := m.db.WithContext(ctx).Model(u).Update("password_hash", hash).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msgPasswordChanged})
}
